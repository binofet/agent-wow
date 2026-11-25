package buddyactions

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"time"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
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
	stance                          *fargodeepReadinessDecision
}

type fargodeepReadinessDecision struct {
	kind, revision string
	facts          map[string]any
	standYards     float64
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
		"mission": map[string]any{"partySessionId": p.SessionId, "targetGuid": strconv.FormatUint(s.fargodeep.target, 10),
			"engagerGuid": strconv.FormatUint(s.fargodeep.engager, 10)}}
}

func (s *Service) fargodeepReadinessDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse, p *partyv1.StateResponse) (fargodeepReadinessDecision, bool) {
	input := s.fargodeepReadinessFactsLocked(w, p)
	if input == nil || !s.fargodeepReadinessCurrentLocked(ctx) {
		return fargodeepReadinessDecision{}, false
	}
	authored := s.behavior
	pulled := s.fargodeep.pulled
	output := s.starterSpells.decide(ctx, input, authored)
	revision := s.starterSpells.revision
	var selected struct {
		Kind       string  `json:"kind"`
		SpellID    uint32  `json:"spellId"`
		Selection  string  `json:"selection"`
		StandYards float64 `json:"standYards"`
	}
	raw, err := json.Marshal(output)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	digest, revisionErr := hex.DecodeString(revision)
	if err != nil || output == nil || len(output) != 4 || decoder.Decode(&selected) != nil ||
		(selected.Kind != "ready" && selected.Kind != "wait") || selected.SpellID != 0 || selected.Selection != fargodeepReadinessSelection ||
		math.IsNaN(selected.StandYards) || math.IsInf(selected.StandYards, 0) || selected.StandYards <= 0 || selected.StandYards > 240 ||
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
	decision := fargodeepReadinessDecision{kind: selected.Kind, revision: revision, facts: input, standYards: selected.StandYards}
	if scope.stance == nil {
		scope.stance = &decision
	}
	return decision, true
}

func (s *Service) fargodeepReadinessMatchesLocked(ctx context.Context, selected fargodeepReadinessDecision, w *worldv1.SnapshotResponse, p *partyv1.StateResponse, requireReady bool) bool {
	scope, _ := ctx.Value(fargodeepReadinessKey{}).(*fargodeepReadinessScope)
	return scope != nil && (!requireReady || selected.kind == "ready") && selected.revision == scope.revision && w.VisitRevision == scope.visit &&
		s.fargodeepReadinessCurrentLocked(ctx) && reflect.DeepEqual(selected.facts, s.fargodeepReadinessFactsLocked(w, p))
}

func (s *Service) fargodeepPartyReadyLocked(ctx context.Context, w *worldv1.SnapshotResponse, p *partyv1.StateResponse, session string, target position) string {
	scope, _ := ctx.Value(fargodeepReadinessKey{}).(*fargodeepReadinessScope)
	if scope == nil {
		return "readiness_policy_unavailable_or_changed"
	}
	input := s.fargodeepReadinessFactsLocked(w, p)
	group := proto.Clone(p.Group)
	selected, valid := s.fargodeepReadinessDecisionLocked(ctx, w, p)
	freshParty := s.readFargodeepPartyLocked(ctx)
	fresh, err := s.snapshot(ctx, s.fargodeep.target)
	if err != nil || freshParty == nil || !s.fargodeepAttemptCurrentLocked(ctx) || fresh.VisitRevision != w.VisitRevision || !proto.Equal(group, freshParty.Group) {
		return "readiness_observations_changed"
	}
	if _, reason := s.fargodeepPartyObservationLocked(fresh, freshParty, session, true, target); reason != "" {
		return reason
	}
	if !reflect.DeepEqual(input, s.fargodeepReadinessFactsLocked(fresh, freshParty)) {
		return "readiness_observations_changed"
	}
	if !valid || !s.fargodeepReadinessCurrentLocked(ctx) {
		return "readiness_policy_unavailable_or_changed"
	}
	if selected.kind == "wait" {
		return "companions_recovering_before_pull"
	}
	return ""
}
