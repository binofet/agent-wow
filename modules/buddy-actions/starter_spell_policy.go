package buddyactions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type starterSpellPolicy struct {
	runtime                *behavior.Runtime
	remote                 func(context.Context, map[string]any) (bool, string, map[string]any, error)
	revision               string
	lastError              string
	failedAuthored         *behavior.Runtime
	authoredError          string
	authoredFailedRevision string
	// Retain one unresolved native receipt across task resets and Lua edits.
	// New named strikes cannot overwrite it; ordinary healing remains available.
	uncertainStrike *supportAttempt
}

// A missing optional export in an attached policy is not an older default.
// Only a body without an attached source uses the shared embedded selectors.
func (p *starterSpellPolicy) decide(ctx context.Context, input map[string]any, authored *behavior.Runtime) map[string]any {
	p.lastError = ""
	if p.remote != nil {
		available, revision, output, err := p.remote(ctx, input)
		p.revision = revision
		if err != nil {
			p.lastError = err.Error()
			return nil
		}
		if available {
			return output
		}
		if revision != "" {
			p.lastError = "current starter spell export unavailable"
			return nil
		}
	}
	runtime := authored
	if authored != nil && p.failedAuthored == authored {
		p.lastError = p.authoredError
		p.revision = p.authoredFailedRevision
		return nil
	}
	if runtime == nil {
		if p.runtime == nil {
			p.runtime = behavior.New()
			if _, err := p.runtime.Reload(behaviors.StarterSpellSource, ""); err != nil {
				p.lastError = err.Error()
				p.runtime = nil
				return nil
			}
		}
		runtime = p.runtime
	}
	before := runtime.Status()
	p.revision = before.Revision
	output, err := runtime.EvaluateContext(ctx, input)
	after := runtime.Status()
	quarantined := after.Revision != before.Revision || after.Failures != before.Failures
	if err != nil || quarantined {
		p.lastError = after.LastError
		if err != nil {
			p.lastError = err.Error()
		}
		if p.lastError == "" {
			p.lastError = "starter source changed during evaluation"
		}
		// Runtime may restore a last-good source. A named-task decision must
		// remain refused until the body owner explicitly accepts another edit.
		if authored != nil && quarantined {
			p.failedAuthored, p.authoredError = authored, p.lastError
			p.authoredFailedRevision = before.Revision
		}
		return nil
	}
	return output
}

func (p *starterSpellPolicy) acceptedAuthoredSource() {
	p.failedAuthored, p.authoredError = nil, ""
	p.authoredFailedRevision = ""
}

func (s *Service) UseHostStarterSpellPolicy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		call, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		return modcb.StarterSpellDecision(call, s.session, s.sessionID, s.selfGUID, input)
	}
}

type starterSpellDecision struct {
	kind      string
	spell     spellcatalog.Spell
	valid     bool
	facts     map[string]any
	selection string
}

func starterJSON(value proto.Message) (map[string]any, bool) {
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(value)
	if err != nil {
		return nil, false
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil {
		return nil, false
	}
	return result, true
}

// This projection exposes mechanics under the existing named-task custody.
// Ordinary offense readiness deliberately excludes these legacy task owners.
// No candidate is ranked, scored or given a healing reserve in this adapter.
func (s *Service) starterSpellFactsLocked(w *worldv1.SnapshotResponse, target *worldv1.CreatureState, targetPosition position) (map[string]any, map[uint32]spellcatalog.Spell, bool) {
	return s.starterSelectionFactsLocked(w, target, targetPosition, "")
}

func (s *Service) starterSelectionFactsLocked(w *worldv1.SnapshotResponse, target *worldv1.CreatureState, targetPosition position, selection string) (map[string]any, map[uint32]spellcatalog.Spell, bool) {
	if selection == coldridgePullSelection {
		input, valid := s.starterColdridgePullFactsLocked(w)
		return input, nil, valid
	}
	self := w.GetSelf()
	if self == nil || self.Guid != s.selfGUID || s.selfGUID == 0 || s.sessionID == "" || target == nil || target.Guid == 0 || !self.Present || !self.MapKnown ||
		!self.PositionKnown || !s.posKnown || !finite(s.pos) || !finite(targetPosition) ||
		s.posMap != self.MapId || s.positionRevision != self.PositionRevision ||
		!target.Present || !target.MapKnown || !target.PositionKnown || target.MapId != self.MapId || !s.spellbookKnown {
		return nil, nil, false
	}
	player := &actionsv1.EncounterPlayer{Guid: self.Guid, Present: self.Present, MapKnown: self.MapKnown,
		MapId: proto.Uint32(self.MapId), PositionKnown: true, PositionRevision: self.PositionRevision,
		X: s.pos.x, Y: s.pos.y, Z: s.pos.z, Orientation: s.pos.o,
		AliveKnown: self.AliveKnown, HealthKnown: self.HealthKnown, Health: self.Health, MaxHealth: self.MaxHealth,
		ClassKnown: self.ClassKnown, ClassId: self.ClassId, RaceKnown: self.RaceKnown, RaceId: self.RaceId,
		LevelKnown: self.LevelKnown, Level: self.Level,
		PowerKnown: self.PowerKnown, PowerType: self.PowerType, Power: self.Power, MaxPower: self.MaxPower,
		MovementKnown: self.MovementFlagsKnown, Moving: s.state.Moving,
		ComboKnown: s.weaponCombo.known, ComboTargetGuid: s.weaponCombo.target, ComboPoints: s.weaponCombo.points}
	if self.AliveKnown {
		player.Alive = proto.Bool(self.Alive)
	}
	creature := &actionsv1.EncounterCreature{Guid: target.Guid, Entry: target.Entry, Present: target.Present,
		MapKnown: target.MapKnown, MapId: proto.Uint32(target.MapId), PositionKnown: true,
		PositionRevision: target.PositionRevision, X: targetPosition.x, Y: targetPosition.y,
		Z: targetPosition.z, Orientation: targetPosition.o,
		AliveKnown: target.AliveKnown, HealthKnown: target.HealthKnown, Health: target.Health, MaxHealth: target.MaxHealth,
		UnitFlagsKnown: target.UnitFlagsKnown, UnitFlags: target.UnitFlags,
		OwnDamageFamilies: s.encounterOwnDamageFamilies(target.Guid)}
	creature.LevelKnown = target.LevelKnown
	if target.LevelKnown {
		creature.Level = proto.Uint32(target.Level)
	}
	if target.AliveKnown {
		creature.Alive = proto.Bool(target.Alive)
	}
	selfJSON, a := starterJSON(player)
	targetJSON, b := starterJSON(creature)
	if !a || !b {
		return nil, nil, false
	}
	if selection == coldridgeCasterSelection {
		selfJSON["baseManaKnown"], selfJSON["baseMana"] = self.BaseManaKnown, self.BaseMana
	}
	spells := map[string]any{}
	ranks := map[uint32]spellcatalog.Spell{}
	for id, rank := range s.learnedContractsLocked() {
		kind := rank.Capability().Kind
		if (selection != starterWeaponSelection && kind != spellcatalog.Damage && kind != spellcatalog.Heal ||
			selection == starterWeaponSelection && !instantWeaponStrike(rank) && kind != spellcatalog.Stealth) || !casterRequirementsObserved(rank, self) {
			continue
		}
		view := encounterSpellMechanics(rank, self)
		view.Capability = "offense"
		if kind == spellcatalog.Heal {
			view.Capability = "heal"
		} else {
			ranks[id] = rank
		}
		view.Available = true
		view.Ready = s.encounterRankResourcesAvailable(rank, self)
		if kind == spellcatalog.Damage && s.starterSpells.uncertainStrike != nil {
			view.Ready = false
		}
		if selection == starterWeaponSelection && kind == spellcatalog.Stealth {
			view.Capability = string(kind)
			view.Ready = view.Ready && view.SelfCastSupported && s.starterSelfSpellAvailableLocked(w)
		} else if selection == starterWeaponSelection {
			s.reflectWeaponStrike(view, rank, self)
			view.Ready = s.starterSpells.uncertainStrike == nil && s.offenseRankReadyLocked(self, rank)
			view.Available = view.Ready
		}
		encoded, valid := starterJSON(view)
		if !valid {
			return nil, nil, false
		}
		spells[strconv.FormatUint(uint64(id), 10)] = encoded
	}
	guid := strconv.FormatUint(s.selfGUID, 10)
	observed := map[string]any{"sessionId": s.sessionID, "selfGuid": guid,
		"visitId":                  strconv.FormatUint(w.VisitRevision, 10),
		"worldObservationRevision": strconv.FormatUint(w.ObservedPackets, 10),
		"spellbookRevision":        strconv.FormatUint(s.spellbookRevision, 10),
		"players":                  map[string]any{guid: selfJSON}, "spells": spells,
		"lastCast": map[string]any{"targetGuid": strconv.FormatUint(s.state.LastCastTargetGuid, 10), "failure": s.state.LastCastFailure}}
	input := map[string]any{"sessionId": s.sessionID, "selfGuid": guid, "observation": observed,
		"target": targetJSON, "autoAttackKnown": s.state.KnownAutoAttack}
	if selection != "" {
		input["selection"] = selection
	}
	if selection == coldridgeCasterSelection {
		input["pullRequired"] = !s.practicePullStarted
	}
	if selection == starterWeaponSelection {
		weapon, valid := starterJSON(self.GetMainhand())
		if !valid {
			return nil, nil, false
		}
		observed["mainhand"] = weapon
		observed["stealthed"] = s.stealthedLocked()
		selfJSON["inCombat"] = self.UnitFlagsKnown && self.UnitFlags&0x80000 != 0
		if self.CombatReachKnown && target.CombatReachKnown {
			observed["meleeInRange"] = distance(s.pos, targetPosition) <= math.Max(5, float64(self.CombatReach+target.CombatReach)+4.0/3.0)-0.5
		}
		observed["activeMeleeTargetGuid"] = strconv.FormatUint(s.autoTarget, 10)
		observed["castPending"], observed["autoAttackUnconfirmed"] = s.state.CastPending, s.state.AutoAttackUnconfirmed
	}
	if op := s.starterSpells.uncertainStrike; op != nil {
		receipt, valid := starterJSON(&op.view)
		if !valid {
			return nil, nil, false
		}
		observed["strikeOperation"] = receipt
	}
	return input, ranks, true
}

func (s *Service) starterSpellDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse, target *worldv1.CreatureState, targetPosition position) starterSpellDecision {
	return s.starterSelectionDecisionLocked(ctx, w, target, targetPosition, "")
}

func (s *Service) starterWeaponDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse, target *worldv1.CreatureState, targetPosition position) starterSpellDecision {
	return s.starterSelectionDecisionLocked(ctx, w, target, targetPosition, starterWeaponSelection)
}

func (s *Service) starterColdridgeSpellDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse, target *worldv1.CreatureState, targetPosition position) starterSpellDecision {
	return s.starterSelectionDecisionLocked(ctx, w, target, targetPosition, coldridgeCasterSelection)
}

func (s *Service) starterSelectionDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse, target *worldv1.CreatureState, targetPosition position, selection string) starterSpellDecision {
	input, ranks, known := s.starterSelectionFactsLocked(w, target, targetPosition, selection)
	if !known {
		return starterSpellDecision{}
	}
	output := s.starterSpells.decide(ctx, input, s.behavior)
	if !s.fargodeepSelectionRevisionLocked(ctx) {
		s.starterSpells.lastError = "Fargodeep policy revision changed after readiness"
		return starterSpellDecision{}
	}
	var selected struct {
		Kind    string `json:"kind"`
		SpellID uint32 `json:"spellId"`
	}
	raw, err := json.Marshal(output)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err != nil || output == nil || len(output) != 2 || decoder.Decode(&selected) != nil ||
		(selected.Kind != "cast" && selected.Kind != "melee" && selected.Kind != "wait" &&
			!(selection == starterWeaponSelection && selected.Kind == "cast_self") &&
			!(selection == coldridgeCasterSelection && selected.Kind == "pull_wait")) ||
		(selected.Kind == "cast" || selected.Kind == "cast_self") != (selected.SpellID != 0) {
		if s.starterSpells.lastError == "" {
			s.starterSpells.lastError = "invalid starter spell decision"
		}
		return starterSpellDecision{}
	}
	// A first-pull refusal grants no offensive dispatch. Keep that decision
	// about observed resources even if they recover before the read returns;
	// the Coldridge caller still checks its original task binding before stop.
	if selection == coldridgeCasterSelection && selected.Kind == "pull_wait" {
		return starterSpellDecision{kind: selected.Kind, valid: true, facts: input, selection: selection}
	}
	rank := ranks[selected.SpellID]
	if selected.Kind == "cast_self" && (rank.ID == 0 || rank.Capability().Kind != spellcatalog.Stealth ||
		!supportsSelfCast(rank) || !s.starterSelfSpellAvailableLocked(w) || !s.encounterRankResourcesAvailable(rank, w.Self) ||
		input["observation"].(map[string]any)["meleeInRange"] != false) {
		s.starterSpells.lastError = "starter self spell is not mechanically available"
		return starterSpellDecision{}
	}
	if selected.Kind == "cast" && selection == starterWeaponSelection &&
		(!s.offenseRankReadyLocked(w.Self, rank) || !s.strikeTargetReady(target.Guid, rank) ||
			!offenseLineInRange(s.pos, targetPosition, rank) || rank.Behind && !behindEnemy(s.pos, targetPosition)) {
		s.starterSpells.lastError = "starter strike is not mechanically available"
		return starterSpellDecision{}
	}
	if selected.Kind == "cast" && (rank.ID == 0 || s.starterSpells.uncertainStrike != nil || selection != starterWeaponSelection && !s.encounterRankResourcesAvailable(rank, w.Self) ||
		distance(s.pos, targetPosition) < rank.MinHostile || distance(s.pos, targetPosition) > rank.MaxHostile) {
		s.starterSpells.lastError = "starter spell is not mechanically available"
		return starterSpellDecision{}
	}
	fresh, err := s.snapshot(ctx, target.Guid)
	if err != nil {
		return starterSpellDecision{}
	}
	current := practiceTargetObservation(fresh, target.Guid)
	point, valid, _ := creaturePosition(current)
	if s.fargodeep.active {
		point, valid = fargodeepCreaturePosition(current)
	}
	if !valid {
		return starterSpellDecision{}
	}
	now, _, valid := s.starterSelectionFactsLocked(fresh, current, point, selection)
	if !valid || !reflect.DeepEqual(input, now) {
		s.starterSpells.lastError = "starter spell observations changed after policy read"
		return starterSpellDecision{}
	}
	return starterSpellDecision{kind: selected.Kind, spell: rank, valid: true, facts: input, selection: selection}
}

func (s *Service) starterSpellFactsMatchLocked(decision starterSpellDecision, w *worldv1.SnapshotResponse, target *worldv1.CreatureState, point position) bool {
	current, _, valid := s.starterSelectionFactsLocked(w, target, point, decision.selection)
	return valid && decision.valid && reflect.DeepEqual(decision.facts, current)
}

func (p *starterSpellPolicy) failure() string {
	if p.lastError == "" {
		return "starter_spell_policy_unavailable"
	}
	return fmt.Sprintf("starter_spell_policy_unavailable: %.160s", p.lastError)
}
