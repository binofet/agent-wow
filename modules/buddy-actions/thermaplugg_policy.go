package buddyactions

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
)

const thermapluggSelection = "thermaplugg_buttons"

type thermapluggBinding struct {
	session                               string
	self, visit, revision, clientRevision uint64
	encounter                             encounterVisit
	position, raw                         position
	independent, assisting                bool
	assistUntil                           time.Time
	worldFacts                            any
}

type thermapluggExecution struct {
	binding      thermapluggBinding
	face, button *worldv1.GameObjectState
	source       string
}

type thermapluggDecision struct {
	kind         string
	face, button *worldv1.GameObjectState
	execution    thermapluggExecution
}

func (b thermapluggBinding) identityCurrent(s *Service, ctx context.Context) bool {
	return ctx.Err() == nil && !s.closed && s.sessionID == b.session && s.selfGUID == b.self && s.encounterVisit == b.encounter
}

func (b thermapluggBinding) current(s *Service, ctx context.Context, w *worldv1.SnapshotResponse, pose bool) bool {
	return b.identityCurrent(s, ctx) && s.matchesCurrentEncounterVisitLocked(w) && w.VisitRevision == b.visit &&
		safeSelfCombat(w, b.self) == "" && w.Self.MapId == thermapluggMap && w.Self.PositionRevision == b.revision &&
		playerPosition(w.Self) == b.raw && !s.clientControlLost && !s.clientControlRestoring && !s.state.MotionUnconfirmed &&
		s.state.InteractionOwner == "" && !s.state.CastPending && !s.state.Traveling && !s.bodyMoving &&
		s.independent == b.independent && s.state.Assisting == b.assisting && s.assistUntil == b.assistUntil &&
		(!pose || s.posKnown && s.pos == b.position && s.posRevision == b.clientRevision && s.positionRevision == b.revision) &&
		reflect.DeepEqual(b.worldFacts, thermapluggStableWorld(w))
}

// Project the same raw observation rows for input and freshness comparison.
// Encounter identity and repeated-observation precedence belong to Lua.
func thermapluggCreatures(w *worldv1.SnapshotResponse) []any {
	creatures := []any{}
	for _, creature := range append([]*worldv1.CreatureState{w.OwnerTarget}, w.NearbyCreatures...) {
		if creature != nil {
			if raw, ok := starterJSON(creature); ok {
				creatures = append(creatures, raw)
			}
		}
	}
	return creatures
}

// Observation ages advance while identical cached facts are read. They are
// policy input, but do not themselves replace a body/object observation.
func thermapluggStable(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if key == "ageMs" || strings.HasSuffix(key, "AgeMs") || key == "remainingMs" {
				continue
			}
			out[key] = thermapluggStable(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = thermapluggStable(item)
		}
		return out
	default:
		return value
	}
}

func thermapluggStableWorld(w *worldv1.SnapshotResponse) any {
	if w == nil {
		return nil
	}
	self, _ := starterJSON(w.GetSelf())
	var owner any
	if w.Owner != nil {
		owner, _ = starterJSON(w.Owner)
	}
	return thermapluggStable(map[string]any{"self": self, "owner": owner, "bosses": thermapluggCreatures(w)})
}

func thermapluggSameObject(a, b *worldv1.GameObjectState) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := proto.Clone(a).(*worldv1.GameObjectState), proto.Clone(b).(*worldv1.GameObjectState)
	x.PositionAgeMs, x.ObservationAgeMs, y.PositionAgeMs, y.ObservationAgeMs = 0, 0, 0, 0
	return proto.Equal(x, y)
}

func (a *thermapluggAttempt) sameBody(s *Service, w *worldv1.SnapshotResponse) bool {
	return a.session == s.sessionID && a.self == s.selfGUID && w.GetVisitRevision() == a.visit && s.matchesCurrentEncounterVisitLocked(w)
}

func (d thermapluggDecision) matchesAttempt(a *thermapluggAttempt) bool {
	return d.face != nil && d.button != nil && d.face.Guid == a.face && d.button.Guid == a.button && d.face.Entry == a.faceEntry && d.button.Entry == a.buttonEntry
}

func (x thermapluggExecution) sameSelection(s *Service) bool {
	r := &s.thermaplugg
	return r.face == x.face.Guid && r.button == x.button.Guid && r.faceEntry == x.face.Entry && r.buttonEntry == x.button.Entry &&
		(r.attempt == nil || !r.attempt.pending)
}

// Read before STOP so its ordinary correction rebase uses the same actual
// observation that authorized this choice. Never repair a changed controller
// after STOP: only that STOP's own revision advance can join the binding.
func (s *Service) stopThermapluggExecutionLocked(ctx context.Context, x *thermapluggExecution) (*worldv1.SnapshotResponse, bool) {
	w, err := s.snapshot(ctx)
	if err != nil || !x.binding.current(s, ctx, w, true) || !x.sameSelection(s) || !s.thermapluggPolicySourceCurrent(x.source) {
		return nil, false
	}
	revision := s.posRevision
	if s.state.Moving {
		revision++
	}
	if !s.stopThermapluggApproachLocked(ctx) || !s.stopLocked(ctx) || s.posRevision != revision ||
		s.pos != x.binding.position || !x.binding.current(s, ctx, s.lastWorld, false) ||
		!x.sameSelection(s) || !s.thermapluggPolicySourceCurrent(x.source) {
		return nil, false
	}
	x.binding.clientRevision = revision
	if !x.binding.current(s, ctx, s.lastWorld, true) {
		return nil, false
	}
	return s.lastWorld, true
}

func (s *Service) thermapluggPolicySourceCurrent(revision string) bool {
	if s.starterSpells.remote != nil {
		return s.starterSpells.revision == revision
	}
	if s.behavior != nil {
		return s.behavior.Status().Revision == revision
	}
	if s.starterSpells.remote == nil && s.starterSpells.runtime != nil {
		return s.starterSpells.runtime.Status().Revision == revision
	}
	return s.starterSpells.revision == revision
}

func (s *Service) thermapluggInput(w *worldv1.SnapshotResponse, known bool, candidates []any) map[string]any {
	r := &s.thermaplugg
	self, _ := starterJSON(w.Self)
	var owner any
	if w.Owner != nil {
		owner, _ = starterJSON(w.Owner)
	}
	attempt := map[string]any{"attempted": false, "pending": false, "written": false, "unknown": false, "sessionId": "", "selfGuid": "0", "visitId": "0", "sentAtMs": int64(0),
		"faceGuid": "0", "buttonGuid": "0", "faceEntry": uint32(0), "buttonEntry": uint32(0)}
	if a := r.attempt; a != nil {
		attempt = map[string]any{"attempted": a.attempted, "pending": a.pending, "written": a.written, "unknown": a.unknown, "sessionId": a.session,
			"selfGuid": strconv.FormatUint(a.self, 10), "visitId": strconv.FormatUint(a.visit, 10), "sentAtMs": a.sentAtMs,
			"faceGuid": strconv.FormatUint(a.face, 10), "buttonGuid": strconv.FormatUint(a.button, 10), "faceEntry": a.faceEntry, "buttonEntry": a.buttonEntry}
	}
	return map[string]any{"selection": thermapluggSelection, "sessionId": s.sessionID, "selfGuid": strconv.FormatUint(s.selfGUID, 10),
		"visitId": strconv.FormatUint(w.VisitRevision, 10), "nowMs": s.now().UnixMilli(), "independent": s.independent, "assisting": s.state.Assisting,
		"self": self, "owner": owner, "bosses": thermapluggCreatures(w), "observationsKnown": known, "candidates": candidates,
		"clientPosition": map[string]any{"x": s.pos.x, "y": s.pos.y, "z": s.pos.z},
		"current": map[string]any{"faceGuid": strconv.FormatUint(r.face, 10), "buttonGuid": strconv.FormatUint(r.button, 10),
			"faceEntry": r.faceEntry, "buttonEntry": r.buttonEntry, "approaching": len(r.path) != 0, "attempt": attempt}, "feedback": r.feedback, "memory": r.memory}
}

type thermapluggOutput struct {
	Kind        string `json:"kind"`
	FaceGuid    string `json:"faceGuid"`
	ButtonGuid  string `json:"buttonGuid"`
	FaceEntry   uint32 `json:"faceEntry"`
	ButtonEntry uint32 `json:"buttonEntry"`
	Memory      any    `json:"memory"`
}

func decodeThermapluggOutput(output map[string]any) (thermapluggOutput, bool) {
	var out thermapluggOutput
	raw, err := json.Marshal(output)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err != nil || output == nil || len(output) != 6 || decoder.Decode(&out) != nil {
		return out, false
	}
	for _, key := range []string{"kind", "faceGuid", "buttonGuid", "faceEntry", "buttonEntry", "memory"} {
		if _, ok := output[key]; !ok {
			return out, false
		}
	}
	switch out.Kind {
	case "observe", "idle", "wait", "clear", "choose", "confirmed":
	default:
		return out, false
	}
	if out.Kind == "choose" || out.Kind == "confirmed" || out.FaceGuid != "" && out.FaceGuid != "0" || out.ButtonGuid != "" && out.ButtonGuid != "0" || out.FaceEntry != 0 || out.ButtonEntry != 0 {
		face, fe := strconv.ParseUint(out.FaceGuid, 10, 64)
		button, be := strconv.ParseUint(out.ButtonGuid, 10, 64)
		if fe != nil || be != nil || face == 0 || button == 0 || strconv.FormatUint(face, 10) != out.FaceGuid || strconv.FormatUint(button, 10) != out.ButtonGuid {
			return out, false
		}
		wired := false
		for _, pair := range thermapluggFaceButtons {
			wired = wired || out.FaceEntry == pair.face && out.ButtonEntry == pair.button
		}
		if !wired {
			return out, false
		}
	}
	return out, true
}

func (s *Service) thermapluggDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse) (thermapluggDecision, *worldv1.SnapshotResponse, bool) {
	r := &s.thermaplugg
	if !s.matchesCurrentEncounterVisitLocked(w) || safeSelfCombat(w, s.selfGUID) != "" || w.Self.MapId != thermapluggMap ||
		!s.refreshPositionLocked(w) || s.state.MotionUnconfirmed || s.state.InteractionOwner != "" || s.state.CastPending || s.state.Traveling || s.bodyMoving {
		return thermapluggDecision{}, nil, false
	}
	b := thermapluggBinding{session: s.sessionID, self: s.selfGUID, visit: w.VisitRevision, revision: w.Self.PositionRevision,
		clientRevision: s.posRevision, encounter: s.encounterVisit, position: s.pos, raw: playerPosition(w.Self), independent: s.independent,
		assisting: s.state.Assisting, assistUntil: s.assistUntil, worldFacts: thermapluggStableWorld(w)}
	first := s.starterSpells.decide(ctx, s.thermapluggInput(w, false, []any{}), s.behavior)
	out, valid := decodeThermapluggOutput(first)
	source := s.starterSpells.revision
	if !valid || !b.current(s, ctx, w, true) || !s.thermapluggPolicySourceCurrent(source) {
		return thermapluggDecision{}, nil, false
	}
	if out.Kind != "observe" {
		if out.Kind == "choose" || out.Kind == "confirmed" {
			return thermapluggDecision{}, nil, false
		}
		r.memory = out.Memory
		return thermapluggDecision{kind: out.Kind}, w, true
	}
	var faces, buttons []*worldv1.GameObjectState
	candidates := []any{}
	observe, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	latest := w
	for _, pair := range thermapluggFaceButtons {
		face, wf := s.thermapluggObject(observe, pair.face)
		button, wb := s.thermapluggObject(observe, pair.button)
		if !b.current(s, ctx, wf, true) || !b.current(s, ctx, wb, true) {
			return thermapluggDecision{}, nil, false
		}
		var rf, rb any
		if face != nil {
			rf, _ = starterJSON(face)
		}
		if button != nil {
			rb, _ = starterJSON(button)
		}
		candidates = append(candidates, map[string]any{"face": rf, "button": rb})
		faces, buttons = append(faces, face), append(buttons, button)
		latest = wb
	}
	input := s.thermapluggInput(latest, true, candidates)
	input["memory"] = out.Memory
	second := s.starterSpells.decide(ctx, input, s.behavior)
	out, valid = decodeThermapluggOutput(second)
	if !valid || out.Kind == "observe" || source != s.starterSpells.revision || !b.current(s, ctx, latest, true) || !s.thermapluggPolicySourceCurrent(source) {
		return thermapluggDecision{}, nil, false
	}
	d := thermapluggDecision{kind: out.Kind}
	if out.Kind == "choose" || out.Kind == "confirmed" {
		for i, face := range faces {
			button := buttons[i]
			if face != nil && strconv.FormatUint(face.Guid, 10) == out.FaceGuid && face.Entry == out.FaceEntry {
				if button != nil && strconv.FormatUint(button.Guid, 10) == out.ButtonGuid && button.Entry == out.ButtonEntry {
					d.face, d.button = face, button
				} else if a := r.attempt; out.Kind == "confirmed" && a != nil && out.ButtonGuid == strconv.FormatUint(a.button, 10) && out.ButtonEntry == a.buttonEntry {
					// Confirmation concerns the actual face. The button identity is
					// retained delivery history, not a fabricated current observation.
					d.face, d.button = face, &worldv1.GameObjectState{Guid: a.button, Entry: a.buttonEntry}
				}
			}
		}
		if d.face == nil || out.Kind == "choose" && (d.face.State != 0 || !thermapluggButtonUsable(d.button)) {
			return thermapluggDecision{}, nil, false
		}
		d.execution = thermapluggExecution{binding: b, face: proto.Clone(d.face).(*worldv1.GameObjectState), button: proto.Clone(d.button).(*worldv1.GameObjectState), source: source}
	}
	r.memory = out.Memory
	return d, latest, true
}
