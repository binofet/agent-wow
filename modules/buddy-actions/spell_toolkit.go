package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"sort"
	"time"
)

// Player::SendInitialSpells: complete active own spellbook + fixed16B cooldowns.
// No partial initial packet, fabricated passive, or unknown metadata is promoted.
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
	n := int(binary.LittleEndian.Uint16(p[end:]))
	if n > 4096 || len(p) != end+2+n*16 {
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
func (s *Service) applySpellbookLocked(known map[uint32]bool) {
	revision := s.spellbookRevision
	changed := !s.spellbookKnown || len(known) != len(s.knownSpells)
	for id := range known {
		if !s.knownSpells[id] {
			changed = true
		}
	}
	s.knownSpells = make(map[uint32]bool, len(known))
	for _, id := range trackedSpellIDs() {
		s.setKnownSpell(id, false)
	}
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
	out := &actionsv1.SpellToolkitState{SessionId: s.sessionID, SelfGuid: s.selfGUID, SpellbookKnown: s.spellbookKnown, SpellbookRevision: s.spellbookRevision}
	ids := make([]uint32, 0, len(s.knownSpells))
	for id, known := range s.knownSpells {
		if known {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		v := &actionsv1.KnownSpell{SpellId: id}
		if r := buffMetadata(id); r.id != 0 {
			v.MetadataKnown = true
			v.Name = r.name
			v.FamilyId = r.family
			v.Rank = r.rank
			v.ManaPercent = r.manaPercent
			v.SelfOnly = !r.party
			v.AutomaticBuffSupported = err == nil && w.Self != nil && w.Self.Guid == s.selfGUID && w.Self.Present && w.Self.ClassKnown && w.Self.ClassId == r.class
			if r.party {
				v.RangeYards = 30
			}
		}
		out.Spells = append(out.Spells, v)
	}
	if err == nil {
		targets := make([]uint64, 0, len(s.buffAuras))
		for guid := range s.buffAuras {
			if p := observedHealingPlayer(w, guid); p != nil && p.Present {
				targets = append(targets, guid)
			}
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
		for _, guid := range targets {
			set := s.buffAuras[guid]
			v := &actionsv1.PlayerAuras{TargetGuid: guid, Complete: set.complete, Revision: set.revision}
			for slot, a := range set.slots {
				if a.spell == 0 {
					continue
				}
				remaining := a.remaining(s.now())
				if a.durationKnown && remaining == 0 {
					continue
				}
				v.Auras = append(v.Auras, &actionsv1.ObservedAura{Slot: uint32(slot), SpellId: a.spell, CasterGuid: a.caster, Positive: a.positive, DurationKnown: a.durationKnown, RemainingMs: remaining, Revision: a.revision})
			}
			out.PlayerAuras = append(out.PlayerAuras, v)
		}
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
