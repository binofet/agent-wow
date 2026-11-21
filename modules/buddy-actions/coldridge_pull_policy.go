package buddyactions

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strconv"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

const coldridgePullSelection = "coldridge_pull_readiness"
const coldridgeCasterSelection = "coldridge_cast"

// The same task binding protects target-free policy reads and selected casts.
func (s *Service) coldridgePracticeCurrentLocked(ctx context.Context, guid uint64) func() bool {
	session, self, until, quest := s.sessionID, s.selfGUID, s.practiceUntil, s.coldridgeGoal.questID
	pulled := s.practicePullStarted
	return func() bool {
		return ctx.Err() == nil && s.sessionID == session && s.selfGUID == self && !s.closed &&
			!s.clientControlLost && !s.clientControlRestoring && s.state.Practicing &&
			s.practiceUntil == until && s.now().Before(until) && s.coldridgeGoal.questID == quest &&
			s.practiceTarget == guid && s.state.PracticeTargetGuid == guid && s.practicePullStarted == pulled &&
			!s.fargodeep.active && !s.questApproaching && !s.recovery.state.Active && s.state.InteractionOwner == ""
	}
}

// Reflect only actual resources. This mode needs neither a target nor a spell.
// Known, living, internally consistent observations remain native prerequisites;
// the percentages and resource comparison belong solely to the accepted Lua.
func (s *Service) starterColdridgePullFactsLocked(w *worldv1.SnapshotResponse) (map[string]any, bool) {
	self := w.GetSelf()
	if self == nil || s.selfGUID == 0 || self.Guid != s.selfGUID || s.sessionID == "" || !self.Present ||
		!self.ClassKnown || self.ClassId != 9 || !self.HealthKnown || self.Health == 0 || self.MaxHealth == 0 || self.Health > self.MaxHealth ||
		!self.PowerKnown || self.PowerType != 0 || !self.BaseManaKnown || self.BaseMana == 0 {
		return nil, false
	}
	guid := strconv.FormatUint(s.selfGUID, 10)
	player := map[string]any{"guid": guid, "classKnown": self.ClassKnown, "classId": self.ClassId,
		"healthKnown": self.HealthKnown, "health": self.Health, "maxHealth": self.MaxHealth,
		"powerKnown": self.PowerKnown, "powerType": self.PowerType, "power": self.Power,
		"baseManaKnown": self.BaseManaKnown, "baseMana": self.BaseMana}
	return map[string]any{"sessionId": s.sessionID, "selfGuid": guid, "selection": coldridgePullSelection,
		"autoAttackKnown": s.state.KnownAutoAttack,
		"observation": map[string]any{"sessionId": s.sessionID, "selfGuid": guid,
			"visitId": strconv.FormatUint(w.VisitRevision, 10), "worldObservationRevision": strconv.FormatUint(w.ObservedPackets, 10),
			"players": map[string]any{guid: player}}}, true
}

func (s *Service) starterColdridgePullDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse) (starterSpellDecision, *worldv1.SnapshotResponse) {
	input, known := s.starterColdridgePullFactsLocked(w)
	if !known {
		return starterSpellDecision{}, nil
	}
	output := s.starterSpells.decide(ctx, input, s.behavior)
	var selected struct {
		Kind    string `json:"kind"`
		SpellID uint32 `json:"spellId"`
	}
	raw, err := json.Marshal(output)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err != nil || output == nil || len(output) != 2 || decoder.Decode(&selected) != nil ||
		(selected.Kind != "ready" && selected.Kind != "wait") || selected.SpellID != 0 {
		if s.starterSpells.lastError == "" {
			s.starterSpells.lastError = "invalid Coldridge pull readiness decision"
		}
		return starterSpellDecision{}, nil
	}
	decision := starterSpellDecision{kind: selected.Kind, valid: true, facts: input, selection: coldridgePullSelection}
	// A refusal based on actual observed resources authorizes no offense.
	// Retain it even if those resources recover while the policy read returns;
	// the caller still checks the bound task before recovering or stopping it.
	if selected.Kind == "wait" {
		return decision, nil
	}
	fresh, err := s.snapshot(ctx, s.practiceTarget)
	now, valid := s.starterColdridgePullFactsLocked(fresh)
	if err != nil || !valid || !reflect.DeepEqual(input, now) {
		s.starterSpells.lastError = "Coldridge pull observations changed after policy read"
		return starterSpellDecision{}, nil
	}
	return decision, fresh
}

// A changed spell-selection frame cannot dispatch. Classify only whether its
// fresh resources also require stopping an unpulled task, using the same Lua.
func (s *Service) stopColdridgeUnreadyPullLocked(ctx context.Context, observed *worldv1.SnapshotResponse, current func() bool) {
	if !current() || s.practicePullStarted {
		return
	}
	if observed == nil {
		var err error
		observed, err = s.snapshot(ctx, s.practiceTarget)
		if err != nil || !current() {
			return
		}
	}
	readiness, _ := s.starterColdridgePullDecisionLocked(ctx, observed)
	if current() && readiness.valid && readiness.kind == "wait" {
		s.stopPracticeLocked(ctx, "coldridge_precast_fresh_pull_authority_changed")
	}
}
