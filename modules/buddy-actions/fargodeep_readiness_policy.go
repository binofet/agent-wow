package buddyactions

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"
	"time"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const fargodeepReadinessSelection = "fargodeep_pull_readiness"

type fargodeepReadinessKey struct{}

// A single synchronous attempt owns this fence. It is neither task state nor
// a receipt: the next observed tick can read a newly accepted source.
type fargodeepReadinessScope struct {
	session, partySession, revision string
	self, target, engager, visit    uint64
	until, assistUntil              time.Time
	pulled                          bool
	authored                        *behavior.Runtime
}

type fargodeepReadinessDecision struct {
	kind, phase, revision string
	facts                 map[string]any
	standYards            float64
	pulled                bool
}

func (s *Service) fargodeepReadinessContextLocked(ctx context.Context) context.Context {
	if ctx.Value(fargodeepReadinessKey{}) != nil {
		return ctx
	}
	f := s.fargodeep
	return context.WithValue(ctx, fargodeepReadinessKey{}, &fargodeepReadinessScope{
		session: s.sessionID, self: s.selfGUID, partySession: f.session, target: f.target,
		engager: f.engager, until: f.until, assistUntil: s.assistUntil, pulled: f.pulled, authored: s.behavior})
}

func (s *Service) fargodeepAttemptCurrentLocked(ctx context.Context) bool {
	scope, _ := ctx.Value(fargodeepReadinessKey{}).(*fargodeepReadinessScope)
	if scope == nil {
		return true
	}
	f := s.fargodeep
	if ctx.Err() != nil || s.closed || s.clientControlLost || s.clientControlRestoring ||
		s.sessionID != scope.session || s.selfGUID != scope.self || !f.active || f.session != scope.partySession ||
		f.target != scope.target || f.engager != scope.engager || f.until != scope.until ||
		s.assistUntil != scope.assistUntil || s.state.InteractionOwner != "" || s.state.MotionUnconfirmed {
		return false
	}
	return true
}

func (s *Service) fargodeepReadinessCurrentLocked(ctx context.Context) bool {
	scope, _ := ctx.Value(fargodeepReadinessKey{}).(*fargodeepReadinessScope)
	if scope == nil {
		return true
	}
	if !s.fargodeepAttemptCurrentLocked(ctx) || s.behavior != scope.authored {
		return false
	}
	if scope.revision == "" {
		return true
	}
	if s.fargodeep.pulled != scope.pulled || !s.now().Before(scope.until) || !s.state.Assisting || !s.now().Before(s.assistUntil) {
		return false
	}
	if scope.authored != nil {
		return scope.authored.Status().Revision == scope.revision && s.starterSpells.failedAuthored != scope.authored
	}
	return s.starterSpells.remote != nil || s.starterSpells.runtime != nil && s.starterSpells.runtime.Status().Revision == scope.revision
}

// Every intervening spell read must belong to the same accepted revision as
// pull readiness. This does not infer a revision from a previous callback.
func (s *Service) fargodeepSelectionRevisionLocked(ctx context.Context) bool {
	scope, _ := ctx.Value(fargodeepReadinessKey{}).(*fargodeepReadinessScope)
	return scope == nil || scope.revision == "" ||
		s.starterSpells.revision == scope.revision && s.fargodeepReadinessCurrentLocked(ctx)
}

// Refusal may stop only this attempt's still-current body and visit. Resource
// loss is not a reason to leave a previously written approach moving, while a
// replacement task/visit must never receive the old task's cleanup packets.
func (s *Service) fargodeepCleanupCurrentLocked(ctx context.Context) bool {
	if !s.fargodeepAttemptCurrentLocked(ctx) {
		return false
	}
	w, err := s.snapshot(ctx, s.fargodeep.target)
	return err == nil && w != nil && s.fargodeepAttemptCurrentLocked(ctx) &&
		s.fargodeep.bodySession == s.sessionID && s.fargodeep.self == s.selfGUID &&
		w.VisitRevision == s.fargodeep.visit && fargodeepCharacter(w, s.selfGUID)
}

func (s *Service) fargodeepReadinessFactsLocked(w *worldv1.SnapshotResponse, p *partyv1.StateResponse) map[string]any {
	if w == nil || p == nil || w.VisitRevision != s.fargodeep.visit {
		return nil
	}
	players := map[string]any{}
	for _, guid := range []uint64{1, 4, 5} {
		v := observedHealingPlayer(w, guid)
		if v == nil {
			return nil
		}
		key := strconv.FormatUint(guid, 10)
		players[key] = map[string]any{"guid": key, "classKnown": v.ClassKnown, "classId": v.ClassId,
			"healthKnown": v.HealthKnown, "health": v.Health, "maxHealth": v.MaxHealth,
			"powerKnown": v.PowerKnown, "powerType": v.PowerType, "power": v.Power, "maxPower": v.MaxPower,
			"baseManaKnown": v.BaseManaKnown, "baseMana": v.BaseMana}
	}
	self := strconv.FormatUint(s.selfGUID, 10)
	return map[string]any{"sessionId": s.sessionID, "selfGuid": self, "selection": fargodeepReadinessSelection,
		"autoAttackKnown": s.state.KnownAutoAttack,
		"observation": map[string]any{"sessionId": s.sessionID, "selfGuid": self,
			"visitId": strconv.FormatUint(w.VisitRevision, 10), "players": players},
		"mission": map[string]any{"partySessionId": s.fargodeep.session, "targetGuid": strconv.FormatUint(s.fargodeep.target, 10),
			"engagerGuid": strconv.FormatUint(s.fargodeep.engager, 10)}}
}

func (s *Service) fargodeepWorkflowFactsLocked(w *worldv1.SnapshotResponse, p *partyv1.StateResponse, stage string) map[string]any {
	input := s.fargodeepReadinessFactsLocked(w, p)
	if input == nil {
		return nil
	}
	players := []*worldv1.PlayerState{}
	safe := map[string]any{}
	for _, guid := range []uint64{1, 4, 5} {
		v := observedHealingPlayer(w, guid)
		players = append(players, v)
		safe[strconv.FormatUint(guid, 10)] = safeHealingPlayer(v, 0)
	}
	target := practiceTargetObservation(w, s.fargodeep.target)
	view := &worldv1.SnapshotResponse{Self: w.Self, Owner: w.Owner, NearbyPlayers: players,
		NearbyCreatures: w.NearbyCreatures, RequestedCreature: target,
		SelfQuestLogKnown: w.SelfQuestLogKnown, SelfQuestLogRevision: w.SelfQuestLogRevision, SelfQuestLog: w.SelfQuestLog}
	jsonView := func(message proto.Message) map[string]any {
		raw, err := protojson.Marshal(message)
		if err != nil {
			return nil
		}
		var result map[string]any
		if json.Unmarshal(raw, &result) != nil {
			return nil
		}
		return result
	}
	scene, party := jsonView(view), jsonView(p)
	if scene == nil || party == nil {
		return nil
	}
	fullPlayers := map[string]any{}
	for _, v := range players {
		if v != nil {
			fullPlayers[strconv.FormatUint(v.Guid, 10)] = jsonView(v)
		}
	}
	point := func(p position) map[string]any { return map[string]any{"x": p.x, "y": p.y, "z": p.z} }
	targetPos, known := fargodeepCreaturePosition(target)
	armPatrol := false
	if !known && fargodeepPatrol(target) {
		targetPos = position{target.X, target.Y, target.Z, target.Orientation}
		end := position{target.Trajectory.DestinationX, target.Trajectory.DestinationY, target.Trajectory.DestinationZ, target.Orientation}
		armPatrol = distance(s.pos, end) <= 35
	}
	nearby, _ := scene["nearbyCreatures"].([]any)
	for i, creature := range w.NearbyCreatures {
		if i >= len(nearby) || creature == nil {
			continue
		}
		row, _ := nearby[i].(map[string]any)
		pos, visible := fargodeepCreaturePosition(creature)
		row["motionPositionKnown"], row["motionPosition"], row["patrolKnown"] = visible, point(pos), fargodeepPatrol(creature)
	}
	f := s.fargodeep
	input["mission"].(map[string]any)["pulled"] = f.pulled
	input["workflow"] = map[string]any{"stage": stage, "scene": scene, "party": party, "players": fullPlayers, "safePlayers": safe,
		"clientPosition": point(s.pos), "selfSafe": safeSelfCombat(w, s.selfGUID) == "", "targetPosition": point(targetPos),
		"targetPositionKnown": known, "targetPatrol": fargodeepPatrol(target), "armPatrolKnown": armPatrol,
		"targetGuidType": f.target >> 48, "targetGuidEntry": uint32(f.target >> 24 & 0xffffff), "failedTarget": s.failedAssistTarget == f.target,
		"authorized": s.now().Before(f.until) && s.state.Assisting && s.now().Before(s.assistUntil), "learnedHealing": s.hasLearnedHealingLocked(),
		"castPending": s.state.CastPending, "autoAttackUnconfirmed": s.state.AutoAttackUnconfirmed,
		"routeKnown":     f.route != nil && f.routeRevision == s.positionRevision && distance(f.routeExpected, s.pos) <= .05 && distance(f.routeTarget, targetPos) <= .25,
		"routeRemaining": routeRemaining(s.pos, f.route, f.routeIndex), "routeDirect": directRemainingRoute(s.pos, targetPos, f.route, f.routeIndex)}
	return input
}

func (s *Service) fargodeepReadinessDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse, p *partyv1.StateResponse, stages ...string) (fargodeepReadinessDecision, bool) {
	stage := "tick"
	if len(stages) == 1 {
		stage = stages[0]
	}
	input := s.fargodeepWorkflowFactsLocked(w, p, stage)
	if input == nil || !s.fargodeepReadinessCurrentLocked(ctx) {
		return fargodeepReadinessDecision{}, false
	}
	authored := s.behavior
	pulled := s.fargodeep.pulled
	output := s.starterSpells.decide(ctx, input, authored)
	revision := s.starterSpells.revision
	var selected struct {
		Kind       string  `json:"kind"`
		Phase      string  `json:"phase"`
		Pulled     bool    `json:"pulled"`
		Selection  string  `json:"selection"`
		StandYards float64 `json:"standYards"`
	}
	raw, err := json.Marshal(output)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	digest, revisionErr := hex.DecodeString(revision)
	if err != nil || output == nil || len(output) != 5 || decoder.Decode(&selected) != nil ||
		(selected.Kind != "arm" && selected.Kind != "hold" && selected.Kind != "stop" && selected.Kind != "approach" && selected.Kind != "attack") ||
		len(selected.Phase) == 0 || len(selected.Phase) > 128 || selected.Selection != fargodeepReadinessSelection ||
		!(selected.StandYards >= 0 && selected.StandYards <= 240) || selected.Kind != "stop" && selected.StandYards == 0 ||
		revisionErr != nil || len(digest) != 32 || hex.EncodeToString(digest) != revision || s.behavior != authored || s.fargodeep.pulled != pulled {
		return fargodeepReadinessDecision{}, false
	}
	scope, _ := ctx.Value(fargodeepReadinessKey{}).(*fargodeepReadinessScope)
	if scope == nil {
		return fargodeepReadinessDecision{}, false
	}
	if scope.revision == "" {
		scope.revision, scope.visit = revision, w.VisitRevision
		scope.pulled = pulled
	}
	if revision != scope.revision || w.VisitRevision != scope.visit || !s.fargodeepReadinessCurrentLocked(ctx) {
		return fargodeepReadinessDecision{}, false
	}
	decision := fargodeepReadinessDecision{kind: selected.Kind, phase: selected.Phase, pulled: selected.Pulled, revision: revision, facts: input, standYards: selected.StandYards}
	return decision, true
}

func (s *Service) fargodeepReadinessMatchesLocked(ctx context.Context, selected fargodeepReadinessDecision, w *worldv1.SnapshotResponse, p *partyv1.StateResponse, requireReady bool) bool {
	scope, _ := ctx.Value(fargodeepReadinessKey{}).(*fargodeepReadinessScope)
	workflow, _ := selected.facts["workflow"].(map[string]any)
	stage, _ := workflow["stage"].(string)
	return scope != nil && selected.revision == scope.revision && w.VisitRevision == scope.visit &&
		s.fargodeepReadinessCurrentLocked(ctx) && reflect.DeepEqual(selected.facts, s.fargodeepWorkflowFactsLocked(w, p, stage))
}

// A source decision is not dispatch authority until the same complete facts
// have been read again after its callback. A refusal never consumes a write.
func (s *Service) fargodeepPolicySnapshotLocked(ctx context.Context, w *worldv1.SnapshotResponse, p *partyv1.StateResponse, stage string) (fargodeepReadinessDecision, *worldv1.SnapshotResponse, bool) {
	selected, valid := s.fargodeepReadinessDecisionLocked(ctx, w, p, stage)
	if !valid {
		return selected, nil, false
	}
	freshParty := s.readFargodeepPartyLocked(ctx)
	fresh, err := s.snapshot(ctx, s.fargodeep.target)
	return selected, fresh, err == nil && fresh != nil && s.fargodeepAttemptCurrentLocked(ctx) &&
		fresh.VisitRevision == w.VisitRevision && s.refreshPositionLocked(fresh) &&
		s.fargodeepReadinessMatchesLocked(ctx, selected, fresh, freshParty, false)
}
