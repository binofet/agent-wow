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

const practiceLifecycleSelection = "practice_lifecycle"

type practiceLifecycleBinding struct {
	session                            string
	self, visit, target                uint64
	until                              time.Time
	encounter                          encounterVisit
	enabled, pulled, quest             bool
	starting                           bool
	host                               string
	interactionOwner, interactionToken string
	position                           position
	positionRevision, clientRevision   uint64
}

type practiceLifecycleDecision struct {
	kind, phase, revision string
	target                uint64
	questGoal, finishing  bool
	leaseMs               uint32
	facts                 map[string]any
	stage                 string
	binding               practiceLifecycleBinding
	motionSources         any
}

func (s *Service) practiceLifecycleBindingLocked(w *worldv1.SnapshotResponse) practiceLifecycleBinding {
	return practiceLifecycleBinding{session: s.sessionID, self: s.selfGUID, visit: w.GetVisitRevision(), target: s.practiceTarget,
		until: s.practiceUntil, encounter: s.encounterVisit, enabled: s.state.Practicing, pulled: s.practicePullStarted,
		quest: s.practiceQuestGoal, host: s.hostEncounterKey, interactionOwner: s.state.InteractionOwner, interactionToken: s.interactionToken,
		position: s.pos, positionRevision: s.positionRevision, clientRevision: s.posRevision}
}

func (b practiceLifecycleBinding) sameTask(s *Service, ctx context.Context) bool {
	return ctx.Err() == nil && !s.closed && s.sessionID == b.session && s.selfGUID == b.self && s.hostEncounterKey == b.host && s.encounterVisit == b.encounter &&
		s.state.Practicing == b.enabled && s.practiceTarget == b.target && s.practiceUntil == b.until &&
		s.practicePullStarted == b.pulled && s.practiceQuestGoal == b.quest && s.state.InteractionOwner == b.interactionOwner && s.interactionToken == b.interactionToken
}

func (b practiceLifecycleBinding) current(s *Service, ctx context.Context, w *worldv1.SnapshotResponse) bool {
	now := s.now() // Local callbacks precede every pure identity/admission check.
	return b.sameTask(s, ctx) && (!b.enabled || b.starting || now.Before(b.until)) && w.GetVisitRevision() == b.visit &&
		(!b.encounter.bound || s.matchesCurrentEncounterVisitLocked(w)) && safePracticeCharacter(w, b.self) == "" &&
		!s.fargodeep.active && !s.questApproaching && s.state.InteractionOwner == "" && !s.state.MotionUnconfirmed &&
		!s.clientControlLost && !s.clientControlRestoring && !s.recovery.state.Active && !s.state.Traveling && !s.bodyMoving &&
		s.posKnown && s.pos == b.position && s.positionRevision == b.positionRevision && s.posRevision == b.clientRevision &&
		w.GetSelf().GetPositionRevision() == b.positionRevision && w.GetSelf().GetMapId() == s.posMap
}

func practiceLifecycleStable(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if key == "attackAgeMs" {
				// Preserve the actual owned-fight freshness boundary while harmless
				// elapsed milliseconds advance between the two cached reads.
				if raw, ok := item.(string); ok {
					if age, err := strconv.ParseUint(raw, 10, 64); err == nil && strconv.FormatUint(age, 10) == raw {
						out[key] = age <= 30000
						continue
					}
				} else if age, ok := item.(float64); ok && age >= 0 && age <= 9007199254740991 && age == float64(uint64(age)) {
					out[key] = age <= 30000
					continue
				}
				out[key] = item
				continue
			}
			if key == "nowMs" || key == "remainingMs" || key == "ageMs" || strings.HasSuffix(key, "AgeMs") || key == "motionPosition" || key == "motionPositionKnown" {
				continue
			}
			out[key] = practiceLifecycleStable(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = practiceLifecycleStable(item)
		}
		return out
	default:
		return value
	}
}

// Every caller that ignores derived motionPosition in the stable comparison
// must also compare this ordered proof. The complete actual trajectory source
// is retained privately, without increasing the unchanged Lua frame budget.
func practiceLifecycleMotionSources(w *worldv1.SnapshotResponse, targetGUID uint64) any {
	type source struct {
		guid              uint64
		movementFlags     uint32
		trajectoryPresent bool
		trajectory        string
	}
	project := func(c *worldv1.CreatureState) *source {
		if c == nil {
			return nil
		}
		out := &source{guid: c.Guid, movementFlags: c.MovementFlags, trajectoryPresent: c.Trajectory != nil}
		if c.Trajectory != nil {
			t := proto.Clone(c.Trajectory).(*worldv1.CreatureTrajectory)
			t.AgeMs = 0
			// This message has only primitive fields, so marshaling cannot fail.
			raw, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(t)
			out.trajectory = string(raw)
		}
		return out
	}
	if w == nil {
		return nil
	}
	nearby := make([]*source, len(w.NearbyCreatures))
	for i, c := range w.NearbyCreatures {
		nearby[i] = project(c)
	}
	return struct {
		selected *source
		nearby   []*source
	}{project(practiceTargetObservation(w, targetGUID)), nearby}
}

func practiceLifecycleMotionPositions(input map[string]any) any {
	project := func(value any) any {
		row, ok := value.(map[string]any)
		if !ok || row == nil {
			return nil
		}
		return []any{row["motionPositionKnown"], row["motionPosition"]}
	}
	rows, _ := input["nearby"].([]any)
	nearby := make([]any, len(rows))
	for i, row := range rows {
		nearby[i] = project(row)
	}
	return []any{project(input["selected"]), nearby}
}

// Explicit factual projections retain every candidate within the existing32-row
// observation bound. They avoid repeating unrelated casts/trajectory metadata
// in this selector's unchanged32KiB frame; native execution keeps full World.
func practiceLifecyclePlayerFacts(p *worldv1.PlayerState) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{"guid": strconv.FormatUint(p.Guid, 10), "present": p.Present,
		"mapKnown": p.MapKnown, "mapId": p.MapId, "positionKnown": p.PositionKnown,
		"x": p.X, "y": p.Y, "z": p.Z, "orientation": p.Orientation, "positionRevision": strconv.FormatUint(p.PositionRevision, 10),
		"classKnown": p.ClassKnown, "classId": p.ClassId, "levelKnown": p.LevelKnown, "level": p.Level,
		"aliveKnown": p.AliveKnown, "alive": p.Alive, "healthKnown": p.HealthKnown, "health": p.Health, "maxHealth": p.MaxHealth,
		"powerKnown": p.PowerKnown, "power": p.Power, "maxPower": p.MaxPower, "powerType": p.PowerType,
		"baseManaKnown": p.BaseManaKnown, "baseMana": p.BaseMana, "unitFlagsKnown": p.UnitFlagsKnown, "unitFlags": p.UnitFlags,
		"movementFlagsKnown": p.MovementFlagsKnown, "movementFlags": p.MovementFlags, "extraMovementFlags": p.ExtraMovementFlags,
		"speedsKnown": p.SpeedsKnown, "runSpeed": p.RunSpeed}
}

func practiceLifecycleCreatureFacts(c *worldv1.CreatureState) map[string]any {
	if c == nil {
		return nil
	}
	row := map[string]any{"guid": strconv.FormatUint(c.Guid, 10), "entry": c.Entry, "present": c.Present,
		"mapKnown": c.MapKnown, "mapId": c.MapId, "positionKnown": c.PositionKnown,
		"x": c.X, "y": c.Y, "z": c.Z, "orientation": c.Orientation, "positionRevision": strconv.FormatUint(c.PositionRevision, 10),
		"levelKnown": c.LevelKnown, "level": c.Level, "aliveKnown": c.AliveKnown, "alive": c.Alive,
		"healthKnown": c.HealthKnown, "health": c.Health, "maxHealth": c.MaxHealth, "npcFlags": c.NpcFlags,
		"unitFlagsKnown": c.UnitFlagsKnown, "unitFlags": c.UnitFlags, "unitFlags2": c.UnitFlags_2,
		"dynamicFlagsKnown": c.DynamicFlagsKnown, "dynamicFlags": c.DynamicFlags,
		"targetKnown": c.TargetKnown, "targetGuid": strconv.FormatUint(c.TargetGuid, 10),
		"movementFlags":     c.MovementFlags,
		"attackVictimKnown": c.AttackVictimKnown, "attackVictimGuid": strconv.FormatUint(c.AttackVictimGuid, 10), "attackAgeMs": strconv.FormatUint(c.AttackAgeMs, 10)}
	point, known, _ := creaturePosition(c)
	row["motionPositionKnown"] = known
	row["motionPosition"] = map[string]any{"x": point.x, "y": point.y, "z": point.z}
	return row
}

func (s *Service) practiceLifecycleFactsLocked(w *worldv1.SnapshotResponse, stage string) (map[string]any, bool) {
	if w == nil || w.Self == nil || w.Self.Guid != s.selfGUID || s.sessionID == "" || len(w.NearbyCreatures) > 32 || !s.posKnown || !finite(s.pos) {
		return nil, false
	}
	self := practiceLifecyclePlayerFacts(w.Self)
	nearby := []any{}
	for _, c := range w.NearbyCreatures {
		if c == nil {
			nearby = append(nearby, nil)
			continue
		}
		nearby = append(nearby, practiceLifecycleCreatureFacts(c))
	}
	var selected any
	if c := practiceTargetObservation(w, s.practiceTarget); c != nil {
		selected = practiceLifecycleCreatureFacts(c)
	}
	quests := []any{}
	for _, q := range w.SelfQuestLog {
		if q == nil {
			quests = append(quests, nil)
			continue
		}
		raw, valid := starterJSON(q)
		if !valid {
			return nil, false
		}
		quests = append(quests, raw)
	}
	now := s.now()
	remaining := int64(0)
	if now.Before(s.practiceUntil) {
		remaining = s.practiceUntil.Sub(now).Milliseconds()
	}
	return map[string]any{"selection": practiceLifecycleSelection, "stage": stage, "sessionId": s.sessionID,
		"selfGuid": strconv.FormatUint(s.selfGUID, 10), "visitId": strconv.FormatUint(w.VisitRevision, 10), "nowMs": now.UnixMilli(),
		"self": self, "selected": selected, "nearby": nearby, "questLogKnown": w.SelfQuestLogKnown, "questLog": quests,
		"clientPosition": map[string]any{"x": s.pos.x, "y": s.pos.y, "z": s.pos.z},
		"practice": map[string]any{"enabled": s.state.Practicing, "targetGuid": strconv.FormatUint(s.practiceTarget, 10),
			"pullStarted": s.practicePullStarted, "questGoal": s.practiceQuestGoal, "phase": s.state.PracticePhase, "remainingMs": remaining},
		"spells": map[string]any{"fireball": s.state.KnownFireball, "smite": s.state.KnownSmite, "autoAttack": s.state.KnownAutoAttack, "sinisterStrike": s.state.KnownSinisterStrike},
		"attack": map[string]any{"active": s.autoTarget != 0 || s.state.AutoAttacking || s.state.AutoAttackRequested,
			"unconfirmed": s.state.AutoAttackUnconfirmed, "failedTargetGuid": strconv.FormatUint(s.failedAssistTarget, 10)}}, true
}

func (s *Service) practiceLifecycleSourceCurrentLocked(revision string) bool {
	if s.starterSpells.remote != nil {
		return s.starterSpells.revision == revision
	}
	if s.behavior != nil {
		return s.behavior.Status().Revision == revision && s.starterSpells.failedAuthored != s.behavior
	}
	return s.starterSpells.runtime != nil && s.starterSpells.runtime.Status().Revision == revision
}

func (s *Service) practiceLifecycleFactsMatchLocked(ctx context.Context, selected practiceLifecycleDecision, w *worldv1.SnapshotResponse) bool {
	_, valid := s.validatePracticeLifecycleFactsLocked(ctx, selected, w)
	return valid
}

func (s *Service) validatePracticeLifecycleFactsLocked(ctx context.Context, selected practiceLifecycleDecision, w *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool) {
	current, valid := s.practiceLifecycleFactsLocked(w, selected.stage)
	unchanged := func(frame map[string]any, world *worldv1.SnapshotResponse) bool {
		return selected.binding.current(s, ctx, world) && s.practiceLifecycleSourceCurrentLocked(selected.revision) &&
			reflect.DeepEqual(selected.motionSources, practiceLifecycleMotionSources(world, selected.binding.target)) &&
			reflect.DeepEqual(practiceLifecycleStable(selected.facts), practiceLifecycleStable(frame))
	}
	if !valid || !unchanged(current, w) {
		return nil, false
	}
	if reflect.DeepEqual(practiceLifecycleMotionPositions(selected.facts), practiceLifecycleMotionPositions(current)) {
		return w, true
	}
	// Age can advance an estimate while its server source is unchanged. Ask the
	// same accepted Lua policy about the latest point, once. No native ranking,
	// source fallback, recursion or automatic choice replacement is introduced.
	output := s.starterSpells.decide(ctx, current, s.behavior)
	reread, valid := decodePracticeLifecycleChoice(output, selected.stage, selected.binding.target)
	if !valid || s.starterSpells.revision != selected.revision || !s.practiceLifecycleSourceCurrentLocked(selected.revision) ||
		reread.kind != selected.kind || reread.target != selected.target || reread.questGoal != selected.questGoal ||
		reread.phase != selected.phase || reread.leaseMs != selected.leaseMs || reread.finishing != selected.finishing {
		return nil, false
	}
	final, err := s.snapshot(ctx, selected.binding.target)
	frame, valid := s.practiceLifecycleFactsLocked(final, selected.stage)
	if err != nil || !valid || !unchanged(frame, final) {
		return nil, false
	}
	if selected.kind == "select" || selected.kind == "pull" {
		var target *worldv1.CreatureState
		if selected.kind == "pull" {
			target = practiceTargetObservation(final, selected.target)
		} else {
			for _, c := range final.NearbyCreatures {
				if c.GetGuid() == selected.target {
					if target != nil {
						return nil, false
					}
					target = c
				}
			}
		}
		point, known, _ := creaturePosition(target)
		if !practiceCreatureAvailable(target, final.Self.MapId) || !known || !northshire(point) {
			return nil, false
		}
	}
	return final, true
}

func decodePracticeLifecycleChoice(output map[string]any, stage string, currentTarget uint64) (practiceLifecycleDecision, bool) {
	var out struct {
		Kind       string `json:"kind"`
		TargetGuid string `json:"targetGuid"`
		QuestGoal  bool   `json:"questGoal"`
		Phase      string `json:"phase"`
		LeaseMs    uint32 `json:"leaseMs"`
		Finishing  bool   `json:"finishing"`
	}
	raw, err := json.Marshal(output)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err != nil || output == nil || len(output) != 6 || decoder.Decode(&out) != nil {
		return practiceLifecycleDecision{}, false
	}
	for _, key := range []string{"kind", "targetGuid", "questGoal", "phase", "leaseMs", "finishing"} {
		if _, found := output[key]; !found {
			return practiceLifecycleDecision{}, false
		}
	}
	target, err := strconv.ParseUint(out.TargetGuid, 10, 64)
	if err != nil || strconv.FormatUint(target, 10) != out.TargetGuid || len(out.Phase) == 0 || len(out.Phase) > 128 ||
		strings.IndexFunc(out.Phase, func(r rune) bool { return r < 32 || r > 126 }) >= 0 || (out.Kind == "start") != (out.LeaseMs > 0) || out.Kind == "start" && out.LeaseMs > 600000 {
		return practiceLifecycleDecision{}, false
	}
	switch stage {
	case "start":
		if out.Kind != "start" && out.Kind != "wait" && out.Kind != "stop" || out.Kind == "start" && target != 0 {
			return practiceLifecycleDecision{}, false
		}
	case "tick":
		if out.Kind != "wait" && out.Kind != "stop" && out.Kind != "select" && out.Kind != "continue" && out.Kind != "target_dead" {
			return practiceLifecycleDecision{}, false
		}
	case "pull":
		if out.Kind != "wait" && out.Kind != "stop" && out.Kind != "pull" && out.Kind != "continue" {
			return practiceLifecycleDecision{}, false
		}
	default:
		return practiceLifecycleDecision{}, false
	}
	if (out.Kind == "select" || out.Kind == "continue" || out.Kind == "pull" || out.Kind == "target_dead") && target == 0 ||
		(out.Kind == "continue" || out.Kind == "pull" || out.Kind == "target_dead") && target != currentTarget {
		return practiceLifecycleDecision{}, false
	}
	return practiceLifecycleDecision{kind: out.Kind, target: target, questGoal: out.QuestGoal, phase: out.Phase, leaseMs: out.LeaseMs, finishing: out.Finishing}, true
}

func (s *Service) practiceLifecycleDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse, stage string) (practiceLifecycleDecision, *worldv1.SnapshotResponse, bool) {
	input, valid := s.practiceLifecycleFactsLocked(w, stage)
	b := s.practiceLifecycleBindingLocked(w)
	b.starting = stage == "start"
	if !valid || !b.current(s, ctx, w) {
		return practiceLifecycleDecision{}, nil, false
	}
	sources := practiceLifecycleMotionSources(w, b.target)
	output := s.starterSpells.decide(ctx, input, s.behavior)
	revision := s.starterSpells.revision
	selected, valid := decodePracticeLifecycleChoice(output, stage, b.target)
	if !valid {
		return practiceLifecycleDecision{}, nil, false
	}
	selected.facts, selected.revision, selected.stage, selected.binding, selected.motionSources = input, revision, stage, b, sources
	fresh, snapshotErr := s.snapshot(ctx, b.target)
	if snapshotErr != nil {
		return practiceLifecycleDecision{}, nil, false
	}
	final, valid := s.validatePracticeLifecycleFactsLocked(ctx, selected, fresh)
	if !valid {
		return practiceLifecycleDecision{}, nil, false
	}
	return selected, final, true
}

// Cache an actual exact-binding snapshot before STOP, then account only for a
// known STOP's expected local revision increment. Neither a stale lastWorld
// rebase nor an unrelated callback may replace the selected pose or task.
func (s *Service) stopForPracticeLifecycleLocked(ctx context.Context, selected *practiceLifecycleDecision) bool {
	w, err := s.snapshot(ctx, selected.binding.target)
	if err != nil || !s.practiceLifecycleFactsMatchLocked(ctx, *selected, w) {
		return false
	}
	revision := selected.binding.clientRevision
	if s.state.Moving {
		revision++
	}
	if !s.stopAutoAttackLocked(ctx) || !s.stopLocked(ctx) || !selected.binding.sameTask(s, ctx) ||
		s.pos != selected.binding.position || s.positionRevision != selected.binding.positionRevision || s.posRevision != revision {
		return false
	}
	selected.binding.clientRevision = revision
	fresh, err := s.snapshot(ctx, selected.binding.target)
	return err == nil && s.practiceLifecycleFactsMatchLocked(ctx, *selected, fresh)
}

func (s *Service) pausePracticeLifecycleLocked(ctx context.Context, b practiceLifecycleBinding) {
	current := func() bool {
		return b.sameTask(s, ctx) && s.state.InteractionOwner == "" && !s.bodyMoving && !s.state.Traveling && !s.fargodeep.active && !s.questApproaching
	}
	if !current() {
		return
	}
	w, err := s.snapshot(ctx, b.target)
	if err != nil || !current() || w.GetVisitRevision() != b.visit ||
		w.GetSelf().GetGuid() != b.self || !w.GetSelf().GetPresent() || !w.GetSelf().GetMapKnown() {
		return
	}
	s.stopLocked(ctx)
	if !current() {
		return
	}
	s.stopAutoAttackLocked(ctx)
	if current() {
		s.state.PracticePhase = "waiting_for_current_practice_lifecycle"
		s.state.Reason = "practice_lifecycle_policy_unavailable_or_changed"
	}
}
