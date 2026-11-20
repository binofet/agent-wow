package buddyactions

import (
	"context"
	"slices"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *Service) UseHostSelfMana() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostSelfMana = true
}

func (s *Service) selfManaThreatLocked(ctx context.Context, w *worldv1.SnapshotResponse) *actionsv1.SelfManaThreat {
	out := &actionsv1.SelfManaThreat{}
	party := s.readPartyStateLocked(ctx)
	group := party.GetGroup()
	members := s.onlineHealingGroup(group)
	if party.GetSessionId() != s.sessionID || !group.GetKnown() || group.GetInGroup() && members == nil {
		return out
	}
	// The local observer caps creatures at 32. At the cap a missing attack
	// is unknown, but any retained positive attack is still represented.
	out.Known = len(w.NearbyCreatures) < 32
	self, partyAttack := false, false
	for _, creatures := range [][]*worldv1.CreatureState{w.NearbyCreatures, {w.OwnerTarget}} {
		for _, c := range creatures {
			if !c.GetPresent() || !c.GetAliveKnown() || !c.GetAlive() || !c.GetMapKnown() || c.GetMapId() != w.Self.MapId || !c.GetAttackVictimKnown() {
				continue
			}
			victim := c.AttackVictimGuid
			attacking := ongoingAttack(c, observedHealingPlayer(w, victim)) ||
				c.AttackAgeMs <= 5000 && (!c.TargetKnown || c.TargetGuid == victim)
			if attacking {
				self = self || victim == s.selfGUID
				partyAttack = partyAttack || victim != s.selfGUID && members[victim]
			}
		}
	}
	if out.Known || self {
		out.AttackingSelf = proto.Bool(self)
	}
	if out.Known || partyAttack {
		out.AttackingParty = proto.Bool(partyAttack)
	}
	return out
}

// SelfManaObservation projects same-session observations and learned rank
// mechanics. The caller owns spell families, mana thresholds and decisions.
func (s *Service) SelfManaObservation(ctx context.Context, _ *emptypb.Empty) (*actionsv1.SelfManaObservationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &actionsv1.SelfManaObservationResponse{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, HostOwned: s.hostSelfMana, SpellbookKnown: s.spellbookKnown,
		Caster: &actionsv1.SelfManaCaster{Guid: s.selfGUID}, Activity: &actionsv1.SelfManaActivity{}, Threat: &actionsv1.SelfManaThreat{},
	}
	w, err := s.readWorldSnapshot(ctx)
	self := w.GetSelf()
	if err != nil || self.GetGuid() != s.selfGUID || !self.GetPresent() || s.closed {
		return out, nil
	}
	caster := out.Caster
	caster.Present, caster.AliveKnown, caster.Alive = true, self.AliveKnown, self.Alive
	caster.MapKnown, caster.PositionKnown = self.MapKnown, self.PositionKnown && finite(playerPosition(self))
	caster.ClassKnown, caster.ClassId = self.ClassKnown, self.ClassId
	caster.PowerKnown = self.PowerKnown
	if self.PowerKnown {
		caster.PowerType, caster.Power, caster.MaxPower = proto.Uint32(self.PowerType), proto.Uint32(self.Power), proto.Uint32(self.MaxPower)
	}
	caster.CombatKnown = self.UnitFlagsKnown
	if caster.CombatKnown {
		caster.InCombat = proto.Bool(self.UnitFlags&0x80000 != 0)
	}
	caster.MovementKnown = self.MovementFlagsKnown
	if caster.MovementKnown {
		caster.Moving = proto.Bool(self.MovementFlags&0xff != 0 || s.state.Moving)
	}
	caster.ChannelKnown = self.ChannelSpellKnown
	if caster.ChannelKnown {
		caster.ChannelSpellId = proto.Uint32(self.ChannelSpellId)
	}
	out.Activity.Known = caster.MapKnown && caster.PositionKnown && caster.AliveKnown && caster.MovementKnown && caster.CombatKnown && caster.ChannelKnown
	if out.Activity.Known {
		out.Activity.Idle = proto.Bool(safeSelfCombat(w, s.selfGUID) == "" && !caster.GetMoving() && caster.GetChannelSpellId() == 0 && !s.selfCastBusyLocked() &&
			!s.clientControlLost && !s.clientControlRestoring && !s.state.AutoAttacking && !s.state.AutoAttackRequested && s.autoTarget == 0 && !s.autoStopping)
	}
	if caster.MapKnown && caster.PositionKnown {
		out.Threat = s.selfManaThreatLocked(ctx, w)
	}
	ids := make([]uint32, 0, len(s.knownSpells))
	for id, learned := range s.knownSpells {
		if learned {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		row := &actionsv1.SelfManaSpell{SpellId: id}
		if spell, known := s.learnedContractsLocked()[id]; known {
			row.FamilyId, row.Rank, row.MetadataKnown = spell.Family, proto.Uint32(spell.Rank), true
			row.SelfCastSupported, row.Channeled = supportsSelfCast(spell), isChanneledSpell(spell)
			row.ReadyKnown = s.spellCooldownBaselineKnown
			if row.ReadyKnown {
				row.Ready = proto.Bool(!s.spellOnCooldownLocked(spell))
			}
		}
		out.Spells = append(out.Spells, row)
	}
	return out, nil
}
