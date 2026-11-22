package buddyactions

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

const practiceTargetSelection = "practice_target"

// These are observed candidates, not a native shortlist. The existing World
// list bound is retained; the policy's search distance is never a query cap.
func (s *Service) practiceTargetFactsLocked(w *worldv1.SnapshotResponse) (map[string]any, bool) {
	self := w.GetSelf()
	if self == nil || self.Guid != s.selfGUID || s.sessionID == "" || !self.Present || !self.MapKnown ||
		!self.PositionKnown || !s.posKnown || !finite(s.pos) || s.posMap != self.MapId ||
		s.positionRevision != self.PositionRevision || len(w.NearbyCreatures) > 32 {
		return nil, false
	}
	rows := map[uint64]*worldv1.CreatureState{}
	for _, c := range w.NearbyCreatures {
		if c != nil && c.Guid != 0 {
			rows[c.Guid] = c
		}
	}
	if c := w.RequestedCreature; c != nil && c.Guid == s.practiceTarget && c.Guid != 0 {
		rows[c.Guid] = c
	}
	ids := make([]uint64, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] }) // Stable observation serialization, not ranking.
	candidates := make([]any, 0, len(ids))
	for _, id := range ids {
		c := rows[id]
		p, known, _ := creaturePosition(c)
		candidates = append(candidates, map[string]any{
			"guid": strconv.FormatUint(c.Guid, 10), "creatureObject": c.Guid>>48 == 0xf130,
			"entry": c.Entry, "present": c.Present, "mapKnown": c.MapKnown, "mapId": c.MapId,
			"positionKnown": known, "positionRevision": strconv.FormatUint(c.PositionRevision, 10), "x": p.x, "y": p.y, "z": p.z,
			"aliveKnown": c.AliveKnown, "alive": c.Alive, "healthKnown": c.HealthKnown, "health": c.Health, "maxHealth": c.MaxHealth,
			"unitFlagsKnown": c.UnitFlagsKnown, "levelKnown": c.LevelKnown, "level": c.Level, "protected": s.targetProtection.blocks(c.Guid, s.now()), "npcFlags": c.NpcFlags, "unitFlags": c.UnitFlags, "unitFlags2": c.UnitFlags_2,
			"dynamicFlags": c.DynamicFlags, "attackVictimKnown": c.AttackVictimKnown, "attackVictimGuid": strconv.FormatUint(c.AttackVictimGuid, 10),
			"attackAgeMs": c.AttackAgeMs, "targetKnown": c.TargetKnown, "targetGuid": strconv.FormatUint(c.TargetGuid, 10),
		})
	}
	guid := strconv.FormatUint(s.selfGUID, 10)
	return map[string]any{"sessionId": s.sessionID, "selfGuid": guid, "selection": practiceTargetSelection,
		"autoAttackKnown": s.state.KnownAutoAttack, "currentTargetGuid": strconv.FormatUint(s.practiceTarget, 10),
		"failedTargetGuid": strconv.FormatUint(s.failedAssistTarget, 10), "candidates": candidates,
		"observation": map[string]any{"sessionId": s.sessionID, "selfGuid": guid, "mapId": self.MapId,
			"visitId": strconv.FormatUint(w.VisitRevision, 10), "players": map[string]any{guid: map[string]any{
				"guid": guid, "present": self.Present, "mapKnown": self.MapKnown, "mapId": self.MapId,
				"positionKnown": true, "x": s.pos.x, "y": s.pos.y, "z": s.pos.z,
			}}},
		"baseline": map[string]any{"revision": strconv.FormatUint(self.PositionRevision, 10), "x": self.X, "y": self.Y, "z": self.Z},
	}, true
}

// Elapsed observation age advances without a different server attack. Keep the
// raw age available to Lua; compare the actual victim/spawn facts rather than
// treating another millisecond as a new object or a changed observation event.
func samePracticeTargetFacts(before, after map[string]any) bool {
	oldRows, newRows := before["candidates"].([]any), after["candidates"].([]any)
	if len(oldRows) != len(newRows) {
		return false
	}
	for i, value := range oldRows {
		oldAge, oldKnown := value.(map[string]any)["attackAgeMs"].(uint64)
		newAge, newKnown := newRows[i].(map[string]any)["attackAgeMs"].(uint64)
		// A reset can be a new attack against the same victim. That event
		// needs a new Lua decision; no player freshness threshold lives here.
		if !oldKnown || !newKnown || newAge < oldAge {
			return false
		}
	}
	stable := func(input map[string]any) map[string]any {
		out := make(map[string]any, len(input))
		for k, v := range input {
			out[k] = v
		}
		rows := []any{}
		for _, value := range input["candidates"].([]any) {
			row := map[string]any{}
			for k, v := range value.(map[string]any) {
				if k != "attackAgeMs" {
					row[k] = v
				}
			}
			rows = append(rows, row)
		}
		out["candidates"] = rows
		return out
	}
	return reflect.DeepEqual(stable(before), stable(after))
}

// The same accepted Lua selector chooses a new target and retains/rejects the
// current target at physical write boundaries. No native nearest fallback.
func (s *Service) practiceTargetDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse, current func() bool) (*worldv1.SnapshotResponse, *worldv1.CreatureState, string, bool) {
	input, known := s.practiceTargetFactsLocked(w)
	if !known || !current() {
		return nil, nil, "", false
	}
	output := s.starterSpells.decide(ctx, input, s.behavior)
	if !current() {
		return nil, nil, "", false
	}
	var chosen struct {
		Kind       string `json:"kind"`
		TargetGuid string `json:"targetGuid"`
	}
	raw, err := json.Marshal(output)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err != nil || output == nil || len(output) != 2 || decoder.Decode(&chosen) != nil ||
		(chosen.Kind != "target" && chosen.Kind != "wait" && chosen.Kind != "stop") {
		if s.starterSpells.lastError == "" {
			s.starterSpells.lastError = "invalid current practice target decision"
		}
		return nil, nil, "", false
	}
	guid, err := strconv.ParseUint(chosen.TargetGuid, 10, 64)
	if err != nil || chosen.TargetGuid != strconv.FormatUint(guid, 10) || (chosen.Kind == "target") != (guid != 0) ||
		s.practiceTarget != 0 && chosen.Kind == "target" && guid != s.practiceTarget {
		return nil, nil, "", false
	}
	observedGuid := guid
	if s.practiceTarget != 0 {
		observedGuid = s.practiceTarget
	}
	// A stop/wait decision still binds the previously selected exact lookup;
	// asking for zero would lose it when discovery is truncated.
	fresh, err := s.snapshot(ctx, observedGuid)
	if err != nil || !current() || safePracticeCharacter(fresh, s.selfGUID) != "" || !s.refreshPositionLocked(fresh) {
		return nil, nil, "", false
	}
	after, known := s.practiceTargetFactsLocked(fresh)
	if !known || !samePracticeTargetFacts(input, after) || !current() {
		return nil, nil, "", false
	}
	if guid == 0 {
		return fresh, nil, chosen.Kind, true
	}
	target := practiceTargetObservation(fresh, guid)
	if !exactOffenseTarget(fresh, target, guid, nil) {
		return nil, nil, "", false
	}
	// Only an actually supplied observation can become the chosen target.
	supplied := false
	for _, c := range w.NearbyCreatures {
		if c != nil && c.Guid == guid {
			supplied = true
		}
	}
	if c := w.RequestedCreature; c != nil && c.Guid == guid && guid == s.practiceTarget {
		supplied = true
	}
	if !supplied {
		return nil, nil, "", false
	}
	return fresh, target, chosen.Kind, true
}
