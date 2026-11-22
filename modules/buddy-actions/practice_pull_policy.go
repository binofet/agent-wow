package buddyactions

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strconv"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

const practicePullSelection = "practice_pull_readiness"

// Only observed body, resource and pose facts cross this target-free read.
// The shared Lua export owns the percentages, class policy and comparisons.
func (s *Service) starterPracticePullFactsLocked(w *worldv1.SnapshotResponse) (map[string]any, bool) {
	self := w.GetSelf()
	if self == nil || s.sessionID == "" || s.selfGUID == 0 || self.Guid != s.selfGUID || !self.Present ||
		!self.MapKnown || !self.PositionKnown || !s.posKnown || s.posMap != self.MapId ||
		s.positionRevision != self.PositionRevision || !finite(s.pos) {
		return nil, false
	}
	guid := strconv.FormatUint(s.selfGUID, 10)
	player := map[string]any{"guid": guid, "present": self.Present,
		"classKnown": self.ClassKnown, "classId": self.ClassId,
		"healthKnown": self.HealthKnown, "health": self.Health, "maxHealth": self.MaxHealth,
		"powerKnown": self.PowerKnown, "powerType": self.PowerType, "power": self.Power, "maxPower": self.MaxPower,
		"baseManaKnown": self.BaseManaKnown, "baseMana": self.BaseMana,
		"unitFlagsKnown": self.UnitFlagsKnown, "unitFlags": self.UnitFlags,
		"mapKnown": self.MapKnown, "mapId": self.MapId, "positionKnown": self.PositionKnown,
		"positionRevision": strconv.FormatUint(self.PositionRevision, 10), "x": s.pos.x, "y": s.pos.y, "z": s.pos.z}
	return map[string]any{"sessionId": s.sessionID, "selfGuid": guid, "selection": practicePullSelection,
		"autoAttackKnown": s.state.KnownAutoAttack,
		"observation": map[string]any{"sessionId": s.sessionID, "selfGuid": guid,
			"visitId": strconv.FormatUint(w.VisitRevision, 10), "players": map[string]any{guid: player}}}, true
}

func (s *Service) starterPracticePullDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse, current func() bool) (starterSpellDecision, *worldv1.SnapshotResponse) {
	input, known := s.starterPracticePullFactsLocked(w)
	if !known || !current() {
		return starterSpellDecision{}, nil
	}
	output := s.starterSpells.decide(ctx, input, s.behavior)
	if !current() {
		return starterSpellDecision{}, nil
	}
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
			s.starterSpells.lastError = "invalid current Practice pull readiness decision"
		}
		return starterSpellDecision{}, nil
	}
	decision := starterSpellDecision{kind: selected.Kind, valid: true, facts: input, selection: practicePullSelection}
	if selected.Kind == "wait" {
		return decision, nil // A refusal authorizes no offense or new movement.
	}
	fresh, err := s.snapshot(ctx, s.practiceTarget)
	if err != nil || !current() || safePracticeCharacter(fresh, s.selfGUID) != "" || !s.refreshPositionLocked(fresh) {
		return starterSpellDecision{}, nil
	}
	after, known := s.starterPracticePullFactsLocked(fresh)
	if !known || !reflect.DeepEqual(input, after) || !current() {
		s.starterSpells.lastError = "Practice pull observations changed after policy read"
		return starterSpellDecision{}, nil
	}
	return decision, fresh
}
