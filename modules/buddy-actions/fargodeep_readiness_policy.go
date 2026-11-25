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
)

const fargodeepReadinessSelection = "fargodeep_pull_readiness"

type fargodeepReadinessKey struct{}

// A single synchronous attempt owns this fence. It is neither task state nor
// a receipt: the next observed tick can read a newly accepted source.
type fargodeepReadinessScope struct {
	session, partySession, revision string
	self, target, engager, visit    uint64
	until, assistUntil              time.Time
	started                         time.Time
	pulled                          bool
	authored                        *behavior.Runtime
}

type fargodeepReadinessDecision struct {
	kind, phase, revision string
	facts                 map[string]any
	standYards            float64
	pulled                bool
	validForMs            uint32
	motionSources         any
	ages                  []uint64
}

func (s *Service) fargodeepReadinessContextLocked(ctx context.Context) context.Context {
	if ctx.Value(fargodeepReadinessKey{}) != nil {
		return ctx
	}
	f := s.fargodeep
	return context.WithValue(ctx, fargodeepReadinessKey{}, &fargodeepReadinessScope{
		session: s.sessionID, self: s.selfGUID, partySession: f.session, target: f.target,
		engager: f.engager, until: f.until, assistUntil: s.assistUntil, pulled: f.pulled, authored: s.behavior, started: s.now()})
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
		w.VisitRevision == s.fargodeep.visit && safeSelfCombat(w, s.selfGUID) == ""
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
		row := practiceLifecyclePlayerFacts(v)
		row["playerFlags"] = v.PlayerFlags
		players[key] = row
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
	players := input["observation"].(map[string]any)["players"].(map[string]any)
	safe := map[string]any{}
	for _, guid := range []uint64{1, 4, 5} {
		safe[strconv.FormatUint(guid, 10)] = safeHealingPlayer(observedHealingPlayer(w, guid), 0)
	}
	point := func(p position) map[string]any { return map[string]any{"x": p.x, "y": p.y, "z": p.z} }
	creature := func(c *worldv1.CreatureState) any {
		row := practiceLifecycleCreatureFacts(c)
		if row == nil {
			return nil
		}
		pos, known := fargodeepCreaturePosition(c)
		row["motionPositionKnown"], row["motionPosition"], row["patrolKnown"] = known, point(pos), fargodeepPatrol(c)
		row["positionAgeMs"] = strconv.FormatUint(c.PositionAgeMs, 10)
		if c.Trajectory != nil {
			value, valid := starterJSON(c.Trajectory)
			if !valid {
				return nil
			}
			row["trajectory"] = value
		}
		return row
	}
	nearby, quests := []any{}, []any{}
	for _, c := range w.NearbyCreatures {
		nearby = append(nearby, creature(c))
	}
	for _, q := range w.SelfQuestLog {
		if q == nil {
			quests = append(quests, nil)
			continue
		}
		value, valid := starterJSON(q)
		if !valid {
			return nil
		}
		quests = append(quests, value)
	}
	party, valid := starterJSON(p)
	if !valid {
		return nil
	}
	target := practiceTargetObservation(w, s.fargodeep.target)
	targetPos, known := fargodeepCreaturePosition(target)
	if !known && fargodeepPatrol(target) {
		targetPos = position{target.X, target.Y, target.Z, target.Orientation}
	}
	f := s.fargodeep
	routeKnown := f.route != nil && f.routeIndex >= 0 && f.routeIndex <= len(f.route) && f.routeRevision == s.positionRevision &&
		distance(f.routeExpected, s.pos) <= .05 && distance(f.routeTarget, targetPos) <= .25
	remaining, direct := 0.0, false
	if routeKnown {
		remaining, direct = routeRemaining(s.pos, f.route, f.routeIndex), directRemainingRoute(s.pos, targetPos, f.route, f.routeIndex)
	}
	input["mission"].(map[string]any)["pulled"] = f.pulled
	input["workflow"] = map[string]any{"stage": stage, "party": party, "safePlayers": safe, "nearbyPlayerCount": len(w.NearbyPlayers),
		"scene": map[string]any{"self": players[strconv.FormatUint(s.selfGUID, 10)], "owner": map[string]any{"present": w.GetOwner().GetPresent()},
			"nearbyCreatures": nearby, "requestedCreature": creature(target), "selfQuestLogKnown": w.SelfQuestLogKnown,
			"selfQuestLogRevision": strconv.FormatUint(w.SelfQuestLogRevision, 10), "selfQuestLog": quests},
		"clientPosition": point(s.pos), "selfSafe": safeSelfCombat(w, s.selfGUID) == "", "targetPosition": point(targetPos),
		"targetPositionKnown": known, "targetPatrol": fargodeepPatrol(target), "targetGuidType": f.target >> 48, "targetGuidEntry": uint32(f.target >> 24 & 0xffffff),
		"failedTarget": s.failedAssistTarget == f.target, "authorized": s.now().Before(f.until) && s.state.Assisting && s.now().Before(s.assistUntil),
		"learnedHealing": s.hasLearnedHealingLocked(), "castPending": s.state.CastPending, "autoAttackUnconfirmed": s.state.AutoAttackUnconfirmed,
		"routeKnown": routeKnown, "routeRemaining": remaining, "routeDirect": direct}
	return input
}

func (s *Service) fargodeepReadinessDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse, p *partyv1.StateResponse, stage string) (fargodeepReadinessDecision, bool) {
	input := s.fargodeepWorkflowFactsLocked(w, p, stage)
	if input == nil || !s.fargodeepReadinessCurrentLocked(ctx) {
		return fargodeepReadinessDecision{}, false
	}
	authored := s.behavior
	pulled := s.fargodeep.pulled
	sources, ages := practiceLifecycleMotionSources(w, s.fargodeep.target), fargodeepObservationAges(w, s.fargodeep.target)
	output := s.starterSpells.decide(ctx, input, authored)
	revision := s.starterSpells.revision
	var selected struct {
		Kind       string  `json:"kind"`
		Phase      string  `json:"phase"`
		Pulled     bool    `json:"pulled"`
		Selection  string  `json:"selection"`
		StandYards float64 `json:"standYards"`
		ValidForMs uint32  `json:"validForMs"`
	}
	raw, err := json.Marshal(output)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	digest, revisionErr := hex.DecodeString(revision)
	if err != nil || output == nil || len(output) != 6 || decoder.Decode(&selected) != nil ||
		(selected.Kind != "arm" && selected.Kind != "hold" && selected.Kind != "stop" && selected.Kind != "approach" && selected.Kind != "attack") ||
		len(selected.Phase) == 0 || len(selected.Phase) > 128 || selected.Selection != fargodeepReadinessSelection ||
		!(selected.StandYards >= 0 && selected.StandYards <= 240) || selected.Kind != "stop" && selected.StandYards == 0 ||
		selected.ValidForMs > 30000 ||
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
	decision := fargodeepReadinessDecision{kind: selected.Kind, phase: selected.Phase, pulled: selected.Pulled, revision: revision,
		facts: input, standYards: selected.StandYards, validForMs: selected.ValidForMs, motionSources: sources, ages: ages}
	return decision, true
}

func (s *Service) fargodeepReadinessMatchesLocked(ctx context.Context, selected fargodeepReadinessDecision, w *worldv1.SnapshotResponse, p *partyv1.StateResponse) bool {
	scope, _ := ctx.Value(fargodeepReadinessKey{}).(*fargodeepReadinessScope)
	workflow, _ := selected.facts["workflow"].(map[string]any)
	stage, _ := workflow["stage"].(string)
	if scope == nil || (selected.kind == "arm" || selected.kind == "approach" || selected.kind == "attack") &&
		!s.now().Before(scope.started.Add(time.Duration(selected.validForMs)*time.Millisecond)) {
		return false
	}
	elapsed := s.now().Sub(scope.started).Milliseconds()
	ages := fargodeepObservationAges(w, s.fargodeep.target)
	if elapsed < 0 || len(ages) != len(selected.ages) {
		return false
	}
	for i, age := range ages {
		if age < selected.ages[i] || age-selected.ages[i] > uint64(elapsed)+1 {
			return false
		}
	}
	return scope != nil && w != nil && selected.revision == scope.revision && w.VisitRevision == scope.visit &&
		s.fargodeepReadinessCurrentLocked(ctx) && reflect.DeepEqual(selected.motionSources, practiceLifecycleMotionSources(w, s.fargodeep.target)) &&
		reflect.DeepEqual(thermapluggStable(selected.facts), thermapluggStable(s.fargodeepWorkflowFactsLocked(w, p, stage)))
}

// Elapsed ages may advance without replacing their packet source. A reset or
// unexplained jump is a different observation; Lua's returned deadline keeps
// every age-dependent policy boundary out of this mechanical comparison.
func fargodeepObservationAges(w *worldv1.SnapshotResponse, target uint64) []uint64 {
	if w == nil {
		return nil
	}
	ages := []uint64{}
	for _, c := range append([]*worldv1.CreatureState{practiceTargetObservation(w, target)}, w.NearbyCreatures...) {
		if c == nil {
			ages = append(ages, 0, 0, 0)
			continue
		}
		ages = append(ages, c.AttackAgeMs, c.PositionAgeMs, c.GetTrajectory().GetAgeMs())
	}
	return ages
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
		s.fargodeepReadinessMatchesLocked(ctx, selected, fresh, freshParty)
}
