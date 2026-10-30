package buddysupplies

import (
	"fmt"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
)

type skillRank struct{ value, maximum uint32 }
type worldView struct {
	raw                                              *worldv1.SnapshotResponse
	self                                             *worldv1.PlayerState
	skills                                           map[uint32]skillRank
	counts                                           map[uint32]uint32
	bag                                              [16]*worldv1.BackpackSlot
	free                                             uint32
	backpackRevision, skillsRevision, copperRevision uint64
	copper                                           uint32
}

func finite(n float32) bool {
	return !math.IsNaN(float64(n)) && !math.IsInf(float64(n), 0) && math.Abs(float64(n)) <= 40000
}
func validItemGUID(g uint64) bool { return g>>48 == 0x4000 && uint32(g) != 0 }

func validateWorld(w *worldv1.SnapshotResponse, self uint64) (worldView, error) {
	fail := func(reason string) (worldView, error) { return worldView{}, fmt.Errorf("%s", reason) }
	if self == 0 || self>>48 != 0 || w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present {
		return fail("actual_own_character_required")
	}
	p := w.Self
	if !p.AliveKnown || !p.Alive || !p.HealthKnown || p.Health == 0 || p.MaxHealth == 0 || p.Health > p.MaxHealth || !p.UnitFlagsKnown || p.UnitFlags&0x80000 != 0 || p.PlayerFlags&0x10 != 0 || p.DynamicFlags&0x20 != 0 {
		return fail("living_own_noncombat_character_required")
	}
	if !p.ClassKnown || p.ClassId == 0 || p.ClassId > 11 || p.ClassId == 10 || !p.LevelKnown || p.Level < 1 || p.Level > 60 || !p.MapKnown || !p.PositionKnown || !finite(p.X) || !finite(p.Y) || !finite(p.Z) || !finite(p.Orientation) {
		return fail("actual_class_level_and_position_required")
	}
	if !p.MovementFlagsKnown || p.MovementFlags != 0 || p.ExtraMovementFlags != 0 || !p.ChannelObjectKnown || p.ChannelObjectGuid != 0 || !p.ChannelSpellKnown || p.ChannelSpellId != 0 {
		return fail("stopped_unchannelled_ground_character_required")
	}
	if !w.BackpackKnown || w.BackpackRevision == 0 || len(w.Backpack) != 16 || !w.SelfSkillsKnown || w.SelfSkillsRevision == 0 || len(w.SelfSkills) != 128 || !w.SelfCopperKnown || w.SelfCopperRevision == 0 {
		return fail("complete_backpack_skills_and_wallet_required")
	}
	v := worldView{raw: w, self: p, skills: map[uint32]skillRank{}, counts: map[uint32]uint32{}, backpackRevision: w.BackpackRevision, skillsRevision: w.SelfSkillsRevision, copperRevision: w.SelfCopperRevision, copper: w.SelfCopper}
	seen := map[uint64]bool{}
	for _, r := range w.Backpack {
		if r == nil || r.Bag != 0 || r.Slot >= 16 || v.bag[r.Slot] != nil {
			return fail("complete_unique_backpack_slots_required")
		}
		v.bag[r.Slot] = r
		if r.ItemGuid == 0 {
			if r.Entry != 0 || r.StackCount != 0 || r.MetadataKnown {
				return fail("empty_backpack_slot_is_inconsistent")
			}
			v.free++
			continue
		}
		if !validItemGUID(r.ItemGuid) || !r.MetadataKnown || r.Entry == 0 || r.StackCount == 0 || seen[r.ItemGuid] || r.StackCount > math.MaxUint32-v.counts[r.Entry] {
			return fail("owned_backpack_metadata_required")
		}
		seen[r.ItemGuid] = true
		v.counts[r.Entry] += r.StackCount
	}
	var slots [128]bool
	for _, r := range w.SelfSkills {
		if r == nil || r.Slot >= 128 || slots[r.Slot] {
			return fail("complete_unique_skill_slots_required")
		}
		slots[r.Slot] = true
		if r.SkillId > math.MaxUint16 || r.Step > math.MaxUint16 || r.Value > r.MaxValue || r.MaxValue > math.MaxUint16 || r.TemporaryBonus < math.MinInt16 || r.TemporaryBonus > math.MaxInt16 || r.PermanentBonus < math.MinInt16 || r.PermanentBonus > math.MaxInt16 {
			return fail("invalid_skill_wire_fields")
		}
		if r.SkillId == 0 {
			if r.Step != 0 || r.Value != 0 || r.MaxValue != 0 || r.TemporaryBonus != 0 || r.PermanentBonus != 0 {
				return fail("inconsistent_empty_skill_slot")
			}
			continue
		}
		if _, ok := v.skills[r.SkillId]; ok || r.Value == 0 || r.MaxValue == 0 {
			return fail("invalid_or_duplicate_skill_rank")
		}
		v.skills[r.SkillId] = skillRank{r.Value, r.MaxValue}
	}
	if p.Mainhand == nil || !p.Mainhand.SlotKnown {
		return fail("actual_mainhand_slot_required")
	}
	if h := p.Mainhand; h.ItemGuid != 0 && (!validItemGUID(h.ItemGuid) || !h.MetadataKnown || h.Entry == 0 || seen[h.ItemGuid]) {
		return fail("actual_mainhand_metadata_required")
	}
	if p.Mainhand.ItemGuid == 0 && (p.Mainhand.Entry != 0 || p.Mainhand.MetadataKnown) {
		return fail("inconsistent_empty_mainhand")
	}
	return v, nil
}

// Missing offhand evidence never means an empty slot.
func observedOffhand(p *worldv1.PlayerState) *worldv1.EquippedWeapon {
	if p == nil {
		return nil
	}
	return p.Offhand
}

func idleGate(v worldView, a *actionsv1.State, l *lootv1.State, t *tradev1.State, p *professionsv1.State, session string, leased bool) string {
	if owner := v.raw.Owner; owner != nil && owner.Present && owner.UnitFlagsKnown && owner.UnitFlags&0x80000 != 0 {
		return "observed_owner_combat_has_priority"
	}
	if a == nil || a.Position == nil || !a.Position.Known || !a.Position.MapKnown || a.Position.MapId != v.self.MapId || !finite(a.Position.X) || !finite(a.Position.Y) || !finite(a.Position.Z) || a.Moving || a.MotionUnconfirmed || a.CastPending || a.AutoAttacking || a.AutoAttackRequested || a.AutoAttackUnconfirmed || a.Traveling || a.Practicing {
		return "own_actions_must_be_idle"
	}
	if leased {
		if a.InteractionOwner != "trade" || a.InteractionLeaseRemainingMs < 11000 {
			return "fresh_owned_trade_lease_required"
		}
	} else if a.InteractionOwner != "" {
		return "another_interaction_owns_the_controller"
	}
	if l == nil || l.SessionId != session || l.Enabled || l.PendingCorpseGuid != 0 || l.PendingGameObjectGuid != 0 || (l.Phase != "disabled" && l.Phase != "idle") {
		return "own_loot_must_be_inactive"
	}
	if t == nil || t.SessionId != session || t.Phase != "IDLE" || t.PartnerGuid != 0 || t.AcceptTransportWritten || t.JunkDisposalEnabled {
		return "own_trade_must_be_idle_and_disposal_disabled"
	}
	if p == nil || p.SessionId != session || p.SelfGuid != v.self.Guid || p.Active || p.Closing {
		return "own_profession_practice_must_be_inactive"
	}
	if o := p.Operation; o != nil && (o.CleanupUnconfirmed || o.Status == professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED) {
		return "profession_cleanup_is_unconfirmed"
	}
	return ""
}

func vendorGate(v worldView, a *actionsv1.State, c *worldv1.CreatureState) string {
	if c == nil || !creatureGUID(c.Guid) || uint32(c.Guid>>24&0xffffff) != c.Entry || !c.Present || !c.MapKnown || c.MapId != v.self.MapId || !c.PositionKnown || !finite(c.X) || !finite(c.Y) || !finite(c.Z) || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || c.NpcFlags&0x80 == 0 || c.UnitFlags&0x80000 != 0 {
		return "actual_living_vendor_required"
	}
	if c.MovementFlags != 0 || c.Trajectory != nil && c.Trajectory.Known && (!c.Trajectory.Stopped || c.Trajectory.TransportRelative) {
		return "stationary_vendor_required"
	}
	if a == nil || a.Position == nil {
		return "controller_position_required"
	}
	dx, dy, dz := float64(c.X-a.Position.X), float64(c.Y-a.Position.Y), float64(c.Z-a.Position.Z)
	if dx*dx+dy*dy+dz*dz > 16 {
		return "vendor_outside_four_yards"
	}
	return ""
}

func itemRequirements(v worldView, i *tradev1.ItemDetails) string {
	if i == nil || !i.Available || i.Pending {
		return "complete_actual_item_metadata_required"
	}
	if i.AllowableClass == 0 || i.AllowableClass&(1<<(v.self.ClassId-1)) == 0 || i.RequiredLevel > v.self.Level {
		return "class_or_level_requirement_not_met"
	}
	if i.AllowableRace != math.MaxUint32 {
		return "race_restriction_cannot_be_verified"
	}
	if i.RequiredSpell != 0 || i.RequiredReputationFaction != 0 || i.RequiredReputationRank != 0 || i.RequiredHonorRank != 0 || i.RequiredCityRank != 0 {
		return "additional_item_requirements_unverified"
	}
	if i.RequiredSkill != 0 {
		r, ok := v.skills[i.RequiredSkill]
		if !ok || r.value < i.RequiredSkillRank {
			return "actual_pure_skill_requirement_not_met"
		}
	}
	return ""
}

func exactPurchaseIncrease(before, after worldView, entry, count uint32) bool {
	if after.backpackRevision <= before.backpackRevision || uint64(after.counts[entry]) != uint64(before.counts[entry])+uint64(count) {
		return false
	}
	for slot, b := range before.bag {
		a := after.bag[slot]
		if b.Entry != entry && b.ItemGuid != 0 && !proto.Equal(a, b) {
			return false
		}
		if b.Entry == entry && (a.ItemGuid != b.ItemGuid || a.Entry != entry || !a.MetadataKnown || a.StackCount < b.StackCount) {
			return false
		}
		if b.ItemGuid == 0 && a.ItemGuid != 0 && a.Entry != entry {
			return false
		}
	}
	return true
}

func exactEquipmentSwap(before, after worldView, slot uint32, equipped, stowed uint64, equippedEntry, stowedEntry uint32) bool {
	if slot >= 16 || after.backpackRevision <= before.backpackRevision || after.self.Mainhand == nil || !after.self.Mainhand.SlotKnown || !after.self.Mainhand.MetadataKnown || after.self.Mainhand.ItemGuid != equipped || after.self.Mainhand.Entry != equippedEntry {
		return false
	}
	off := observedOffhand(after.self)
	if off == nil || !off.SlotKnown || off.ItemGuid != 0 || off.Entry != 0 || off.MetadataKnown {
		return false
	}
	for n, b := range before.bag {
		a := after.bag[n]
		if uint32(n) == slot {
			if a.ItemGuid != stowed || a.Entry != stowedEntry || a.StackCount != 1 || !a.MetadataKnown {
				return false
			}
		} else if !proto.Equal(a, b) {
			return false
		}
	}
	return true
}
