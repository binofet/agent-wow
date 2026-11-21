package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"sort"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Player::SendInitialSpells writes an exact active spell count, followed by a
// cooldown-map count. It can omit cooldown records marked needSendToClient=false
// or whose spell metadata is missing, without updating that cooldown count.
// Every serialized cooldown record must still be complete (16 bytes).
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/Player.cpp
func completeSpellbook(p []byte) (map[uint32]bool, error) {
	bad := errors.New("malformed complete initial spellbook")
	if len(p) < 5 || p[0] != 0 {
		return nil, bad
	}
	count := int(binary.LittleEndian.Uint16(p[1:]))
	end := 3 + count*6
	if count > 4096 || len(p) < end+2 {
		return nil, bad
	}
	const cooldownRecordSize = 16
	declaredCooldowns := int(binary.LittleEndian.Uint16(p[end:]))
	cooldownBytes := len(p) - end - 2
	if declaredCooldowns > 4096 || cooldownBytes%cooldownRecordSize != 0 || cooldownBytes/cooldownRecordSize > declaredCooldowns {
		return nil, bad
	}
	out := make(map[uint32]bool, count)
	for i := 0; i < count; i++ {
		at := 3 + i*6
		id := binary.LittleEndian.Uint32(p[at:])
		if id == 0 || id > 0x7fffffff || binary.LittleEndian.Uint16(p[at+4:]) != 0 {
			return nil, bad
		}
		out[id] = true
	}
	return out, nil
}

// Read the cooldown section only after the same complete spellbook packet has
// been validated. The client learns the remaining durations, not game time.
func (s *Service) observeInitialCooldownsLocked(p []byte) {
	end := 3 + int(binary.LittleEndian.Uint16(p[1:]))*6
	s.spellCooldownBaselineKnown = true
	if s.spellReady == nil {
		s.spellReady = make(map[uint32]time.Time)
	}
	if s.categoryReady == nil {
		s.categoryReady = make(map[uint32]time.Time)
	}
	if s.serverSpellReady == nil {
		s.serverSpellReady = map[uint32]time.Time{}
	}
	if s.serverCategoryReady == nil {
		s.serverCategoryReady = map[uint32]time.Time{}
	}
	now := s.now()
	for at := end + 2; at < len(p); at += 16 {
		id := binary.LittleEndian.Uint32(p[at:])
		category := uint32(binary.LittleEndian.Uint16(p[at+6:]))
		spellMs, categoryMs := binary.LittleEndian.Uint32(p[at+8:]), binary.LittleEndian.Uint32(p[at+12:])
		expires := now.Add(time.Duration(max(spellMs, categoryMs)) * time.Millisecond)
		if id != 0 && expires.After(s.serverSpellReady[id]) {
			s.serverSpellReady[id] = expires
		}
		if categoryEnd := now.Add(time.Duration(categoryMs) * time.Millisecond); category != 0 && categoryMs != 0 && categoryEnd.After(s.serverCategoryReady[category]) {
			s.serverCategoryReady[category] = categoryEnd
		}
		if id != 0 && expires.After(s.spellReady[id]) {
			s.spellReady[id] = expires
		}
		if categoryEnd := now.Add(time.Duration(categoryMs) * time.Millisecond); category != 0 && categoryMs != 0 && categoryEnd.After(s.categoryReady[category]) {
			s.categoryReady[category] = categoryEnd
		}
	}
}

func (s *Service) spellOnCooldownLocked(spell spellcatalog.Spell) bool {
	now := s.now()
	return now.Before(s.spellReady[spell.ID]) || now.Before(s.categoryReady[spell.Category])
}
func (s *Service) applySpellbookLocked(known map[uint32]bool) {
	revision := s.spellbookRevision
	changed := !s.spellbookKnown || len(known) != len(s.knownSpells)
	for id := range known {
		if !s.knownSpells[id] {
			changed = true
		}
	}
	s.knownSpells = make(map[uint32]bool, len(known))
	s.state.KnownLesserHeal, s.state.KnownSmite, s.state.KnownFireball, s.state.KnownShadowBolt, s.state.KnownSinisterStrike = false, false, false, false, false
	s.state.KnownAutoAttack, s.improvedSinister1, s.improvedSinister2 = false, false, false
	for id := range known {
		s.setKnownSpell(id, true)
	}
	s.spellbookKnown = true
	s.spellbookRevision = revision
	if changed {
		s.spellbookRevision++
	}
}
func (s *Service) SpellToolkit(ctx context.Context, _ *emptypb.Empty) (*actionsv1.SpellToolkitState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buffTimeoutLocked()
	w, err := s.snapshot(ctx)
	if err == nil {
		s.pruneBuffAurasLocked(w)
	}
	out := &actionsv1.SpellToolkitState{SessionId: s.sessionID, SelfGuid: s.selfGUID, SpellbookKnown: s.spellbookKnown, SpellbookRevision: s.spellbookRevision, TargetedResurrectionSupported: true}
	ids := make([]uint32, 0, len(s.knownSpells))
	for id, known := range s.knownSpells {
		if known {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		v := &actionsv1.KnownSpell{SpellId: id}
		if r, ok := s.learnedContractsLocked()[id]; ok {
			cap := r.Capability()
			v.MetadataKnown = true
			v.Name = r.Name
			v.FamilyId = r.Family
			v.Rank = r.Rank
			v.ManaPercent = r.CostPercent
			v.FlatCost = r.Cost
			v.PowerType = r.Power
			v.CastTimeMs = r.CastMs
			v.SelfCastSupported = supportsSelfCast(r)
			v.Channeled = isChanneledSpell(r)
			v.Capability = string(cap.Kind)
			if cap.Kind == spellcatalog.CreateItem {
				v.CreatedItemEntry = r.CreatedItem()
			}
			v.TargetRule = cap.Target
			v.BlockedReason = cap.Blocked
			v.SelfOnly = cap.Target == "self"
			v.RangeYards = float32(r.MaxHostile)
			if cap.Target == "ally" || cap.Target == "dead_ally" {
				v.RangeYards = float32(r.MaxFriendly)
			}
			v.RequiredItemClass = r.ItemClass
			v.RequiredSubclassMask = r.SubclassMask
			v.RequiresStealth = r.Attrs[0]&0x20000 != 0
			v.RequiresBehind = r.Behind
		} else {
			v.BlockedReason = "metadata_unknown"
		}
		if r := buffMetadata(id); r.id != 0 {
			v.MetadataKnown = true
			v.Name = r.name
			v.FamilyId = r.family
			v.Rank = r.rank
			v.ManaPercent = r.manaPercent
			v.SelfOnly = !r.party
			v.AutomaticBuffSupported = err == nil && w.Self != nil && w.Self.Guid == s.selfGUID && w.Self.Present && w.Self.ClassKnown
			if r.party {
				v.RangeYards = 30
			}
		}
		out.Spells = append(out.Spells, v)
	}
	if err == nil {
		out.PlayerAuras = s.observedPlayerAurasLocked(w)
	}
	if s.buffOp != nil {
		out.LastBuff = proto.Clone(&s.buffOp.view).(*actionsv1.BuffOperation)
	}
	return out, nil
}
func (s *Service) buffLeaseRemainingLocked() uint64 {
	if s.state.Buffing && s.now().Before(s.buffUntil) {
		return uint64(s.buffUntil.Sub(s.now()) / time.Millisecond)
	}
	return 0
}
