package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
	"testing"
	"time"
)

func TestVerifiedSpellRankCatalog(t *testing.T) {
	chains := map[uint32][]uint32{
		2050: {2050, 2052, 2053},
		585:  {585, 591, 598, 984, 1004, 6060, 10933, 10934, 25363, 25364, 48122, 48123},
		133:  {133, 143, 145, 3140, 8400, 8401, 8402, 10148, 10149, 10150, 10151, 25306, 27070, 38692, 42832, 42833},
		1752: {1752, 1757, 1758, 1759, 1760, 8621, 11293, 11294, 26861, 26862, 48637, 48638},
	}
	seen := map[uint32]bool{}
	for family, ids := range chains {
		known := map[uint32]bool{}
		for rank, id := range ids {
			if seen[id] {
				t.Fatal("duplicate", id)
			}
			seen[id] = true
			r := spellRankFor(id)
			if r.family != family || r.rank != uint32(rank+1) {
				t.Fatal(id, r)
			}
			if highestKnownSpell(known, family).id == id {
				t.Fatal("selected unknown", id)
			}
			known[id] = true
			if highestKnownSpell(known, family).id != id {
				t.Fatal("did not select highest rank", id)
			}
		}
		last := ids[len(ids)-1]
		delete(known, last)
		if highestKnownSpell(known, family).id != ids[len(ids)-2] {
			t.Fatal("removal did not fall back", family)
		}
	}
	if len(seen) != len(spellRankCatalog) {
		t.Fatal("unverified catalog entry")
	}
	for _, id := range []uint32{0, 29170, 42834, 2054, 1234567} {
		if spellRankFor(id).id != 0 {
			t.Fatal("unrelated/NPC spell in catalog", id)
		}
	}
}
func TestRankSpecificResourceAndCastMetadata(t *testing.T) {
	for _, tc := range []struct {
		id, pct uint32
		cast    time.Duration
	}{
		{2050, 16, 1500 * time.Millisecond}, {2052, 21, 2 * time.Second}, {2053, 27, 2500 * time.Millisecond},
		{585, 9, 1500 * time.Millisecond}, {591, 12, 2 * time.Second}, {598, 15, 2500 * time.Millisecond},
		{133, 8, 1500 * time.Millisecond}, {143, 11, 2 * time.Second}, {145, 14, 2500 * time.Millisecond},
		{3140, 16, 3 * time.Second}, {8400, 19, 3500 * time.Millisecond}, {42833, 19, 3500 * time.Millisecond},
		{1752, 0, 0}, {48638, 0, 0},
	} {
		r := spellRankFor(tc.id)
		if r.manaPercent != tc.pct || r.castTime != tc.cast {
			t.Fatal(tc, r)
		}
	}
}

func rankLearn(s *Service, id uint32) {
	p := binary.LittleEndian.AppendUint32(nil, id)
	p = append(p, 0, 0)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLearnedSpell, Payload: p})
}
func rankEvent(spell uint32, heal bool) []byte {
	p := smiteEvent(true, 1, enemyGUID)
	if heal {
		p = serverSpellPacket(true, 1)
	}
	_, _, _, off, _ := castEvent(p)
	binary.LittleEndian.PutUint32(p[off-4:], spell)
	return p
}
func TestRankKnowledgeUsesAtomicNormalReceiptsAndRemoval(t *testing.T) {
	s, _, _ := rig(t)
	learnSpells(s, lesserHeal, smite, fireball, sinisterStrike)
	before := s.selectedSpellLocked(lesserHeal)
	bad := []byte{4, 8, 0, 0}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLearnedSpell, Payload: bad})
	if s.selectedSpellLocked(lesserHeal) != before || s.state.MalformedPackets != 1 {
		t.Fatal("truncated learn changed knowledge")
	}
	rankLearn(s, 2052)
	if s.selectedSpellLocked(lesserHeal).id != 2052 || !s.state.KnownLesserHeal {
		t.Fatal("real rank2 not selected")
	}
	// Malformed initial snapshot must retain the last complete spell book.
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: []byte{0, 1, 0}})
	if s.selectedSpellLocked(lesserHeal).id != 2052 {
		t.Fatal("malformed snapshot cleared knowledge")
	}
	removed := binary.LittleEndian.AppendUint32(nil, 2052)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGRemovedSpell, Payload: removed})
	if s.selectedSpellLocked(lesserHeal).id != 2050 {
		t.Fatal("did not select remaining known rank")
	}
	// A replacement packet is old rank then newly active rank, never an invented chain unlock.
	replacement := binary.LittleEndian.AppendUint32(nil, 2050)
	replacement = binary.LittleEndian.AppendUint32(replacement, 2053)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSupercededSpell, Payload: replacement})
	if s.selectedSpellLocked(lesserHeal).id != 2053 || s.knownSpells[2050] || s.knownSpells[2052] {
		t.Fatal("superseded knowledge not exact")
	}
	learnSpells(s, 585)
	if s.state.KnownLesserHeal || s.selectedSpellLocked(lesserHeal).id != 0 || !s.state.KnownSmite {
		t.Fatal("new complete snapshot retained absent spell")
	}
	rankLearn(s, 29170)
	if s.state.KnownLesserHeal {
		t.Fatal("NPC same-name spell unlocked healing")
	}
}
func TestRankKnowledgePacketPrefixesAndExactBodies(t *testing.T) {
	cases := []struct {
		op          uint32
		wire        string
		remove, add uint32
	}{
		{opcode.SMSGLearnedSpell, "040800000000", 0, 2052},
		{opcode.SMSGRemovedSpell, "04080000", 2052, 0},
		{opcode.SMSGSupercededSpell, "0208000004080000", 2050, 2052},
	}
	for _, tc := range cases {
		p, _ := hex.DecodeString(tc.wire)
		remove, add, e := spellKnowledgePacket(tc.op, p)
		if e != nil || remove != tc.remove || add != tc.add {
			t.Fatal(remove, add, e)
		}
		for i := 0; i < len(p); i++ {
			if _, _, e := spellKnowledgePacket(tc.op, p[:i]); e == nil {
				t.Fatal("accepted truncated knowledge", i)
			}
		}
		if _, _, e := spellKnowledgePacket(tc.op, append(p, 0)); e == nil {
			t.Fatal("accepted trailing knowledge")
		}
	}
}
func TestHighestKnownHealUsesActualCostAndExactPendingRank(t *testing.T) {
	s, f, _ := rig(t)
	learnSpells(s, 2050, 2052)
	f.world.Self.BaseManaKnown = true
	f.world.Self.BaseMana = 100
	f.world.Self.Power = 20
	out, _ := s.HealOnce(context.Background(), &emptypb.Empty{})
	if lastCast(f) != 0 || out.Reason != "mana_unknown_or_insufficient" {
		t.Fatal("rank1 cost used for rank2", out)
	}
	f.world.Self.Power = 21
	out, _ = s.HealOnce(context.Background(), &emptypb.Empty{})
	if lastCast(f) != 2052 || out.LastCastSpellId != 2052 || !out.CastPending {
		t.Fatal(out)
	}
	// Training another rank during an outstanding cast cannot change receipt correlation.
	rankLearn(s, 2053)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: rankEvent(2053, true)})
	if !s.state.CastPending {
		t.Fatal("different rank consumed pending cast")
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: rankEvent(2052, true)})
	if s.state.CastPending || s.state.ServerCastGo != 1 || s.state.ServerAssistGo != 0 {
		t.Fatal(s.stateLocked())
	}
}
func TestOffensiveRankCostAndSelectedHealReserve(t *testing.T) {
	for _, class := range []uint32{8, 5} {
		s, f, _ := assistRig(t)
		f.world.Self.ClassId = class
		f.world.Self.BaseManaKnown = true
		f.world.Self.BaseMana = 100
		need, spell := uint32(11), uint32(143)
		if class == 8 {
			learnSpells(s, 133, 143)
		} else {
			learnSpells(s, 585, 591, 2050, 2052)
			need, spell = 33, 591
		}
		f.world.Self.Power = need - 1
		s.Tick(context.Background())
		if lastCast(f) != 0 {
			t.Fatal("used lower-rank resource budget", s.stateLocked())
		}
		f.world.Self.Power = need
		s.Tick(context.Background())
		if lastCast(f) != spell || s.stateLocked().CombatSpellId != spell {
			t.Fatal(s.stateLocked(), f.packets)
		}
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: rankEvent(spell, false)})
		if s.state.ServerAssistGo != 1 || s.state.ServerCastGo != 0 {
			t.Fatal("rank classified as heal", s.stateLocked())
		}
	}
}
func TestRogueSelectsKnownRankWithoutChangingVerifiedEnergyCost(t *testing.T) {
	s, f, _ := rogueRig(t)
	learnSpells(s, autoAttack, 1752, 1757)
	verifyWeapon(t, s, f)
	f.world.Self.Power = 44
	s.Tick(context.Background())
	if lastCast(f) != 0 {
		t.Fatal("rank bypassed energy gate")
	}
	f.world.Self.Power = 45
	s.Tick(context.Background())
	if lastCast(f) != 1757 || s.stateLocked().SinisterStrikeEnergyCost != 45 {
		t.Fatal(s.stateLocked())
	}
}
func TestHigherRankDamageRetainsActualFamilyReceipts(t *testing.T) {
	s, _, _ := rig(t)
	p, _ := hex.DecodeString(smiteDamageGolden)
	_, n, _ := readGUID(p)
	_, m, _ := readGUID(p[n:])
	binary.LittleEndian.PutUint32(p[n+m:], 591)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellnonmeleedamagelog, Payload: p})
	if s.state.SmiteDamageEvents != 1 || s.state.SmiteDamageTotal != 9 || s.state.AssistDamageEvents != 1 {
		t.Fatal(s.stateLocked())
	}
}
func TestPracticeUsesKnownTrainedRankWithExistingToggle(t *testing.T) {
	s, f, _ := practiceRig(t, 8)
	rankLearn(s, 143)
	s.Tick(context.Background())
	if lastCast(f) != 143 {
		t.Fatal(s.stateLocked(), f.packets)
	}
	if s.state.PracticePulls != 1 {
		t.Fatal("rank changed pull behavior", s.stateLocked())
	}
}
