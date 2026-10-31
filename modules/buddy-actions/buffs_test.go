package buddyactions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"os"
	"testing"
	"time"
)

func buffInitial(ids ...uint32) []byte {
	p := []byte{0, byte(len(ids)), byte(len(ids) >> 8)}
	for _, id := range ids {
		p = binary.LittleEndian.AppendUint32(p, id)
		p = append(p, 0, 0)
	}
	return append(p, 0, 0)
}
func buffAura(target, caster uint64, slot byte, spell uint32, duration uint32) []byte {
	p := append(packedGUID(target), slot)
	p = binary.LittleEndian.AppendUint32(p, spell)
	if spell == 0 {
		return p
	}
	flags := byte(0x11)
	if duration > 0 {
		flags |= 0x20
	}
	if caster == target {
		flags |= 8
	}
	p = append(p, flags, 12, 1)
	if caster != target {
		p = append(p, packedGUID(caster)...)
	}
	if duration > 0 {
		p = binary.LittleEndian.AppendUint32(p, duration)
		p = binary.LittleEndian.AppendUint32(p, duration)
	}
	return p
}
func buffGo(s *Service, target uint64) []byte {
	p := append(packedGUID(s.selfGUID), packedGUID(s.selfGUID)...)
	p = append(p, s.castCount)
	p = binary.LittleEndian.AppendUint32(p, s.buffOp.view.SpellId)
	p = binary.LittleEndian.AppendUint32(p, 0x100)
	p = binary.LittleEndian.AppendUint32(p, 17)
	p = append(p, 1)
	p = binary.LittleEndian.AppendUint64(p, target)
	p = append(p, 0)
	p = binary.LittleEndian.AppendUint32(p, 2)
	return append(p, packedGUID(target)...)
}
func buffRig(t *testing.T, class uint32, ids ...uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.Self.ClassId = class
	f.world.Self.LevelKnown = true
	f.world.Self.Level = 12
	f.world.Self.Power = 1000
	f.world.Self.MaxPower = 1000
	f.world.Self.BaseManaKnown = true
	f.world.Self.BaseMana = 1000
	f.world.Owner.Health = f.world.Owner.MaxHealth
	f.world.Owner.PowerKnown = true
	f.world.Owner.PowerType = 0
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: buffInitial(ids...)})
	s.Buffs(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
	return s, f, advance
}
func buffCasts(f *fakeSession) []*modv1.SendPacketRequest {
	var out []*modv1.SendPacketRequest
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGCastSpell {
			out = append(out, p)
		}
	}
	return out
}
func confirmBuff(t *testing.T, s *Service, auraFirst bool) {
	t.Helper()
	p := buffAura(s.buffOp.view.TargetGuid, s.selfGUID, 7, s.buffOp.view.SpellId, 600000)
	goPacket := buffGo(s, s.buffOp.view.TargetGuid)
	if auraFirst {
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: p})
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: goPacket})
	if !auraFirst {
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: p})
	}
}
func TestBuffLearnedHighestRankNormalWireAndActualAura(t *testing.T) {
	for _, auraFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(auraFirst), func(t *testing.T) {
			s, f, advance := buffRig(t, 5, 1243, 1244)
			f.world.Owner = nil
			s.Tick(context.Background())
			casts := buffCasts(f)
			if len(casts) != 1 || !bytes.Equal(casts[0].Payload, []byte{1, 0xdc, 4, 0, 0, 0, 2, 0, 0, 0, 1, 1}) {
				t.Fatal("not exact normal highest learned rank packet", casts)
			}
			if s.state.BuffsConfirmed != 0 || s.buffOp.view.AuraConfirmed {
				t.Fatal("transport counted as aura")
			}
			if !auraFirst {
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: buffGo(s, 1)})
				if s.state.BuffsConfirmed != 0 || s.buffOp.view.Phase != "waiting_server" {
					t.Fatal("GO alone counted as buff")
				}
			}
			confirmBuff(t, s, auraFirst)
			if s.state.BuffsConfirmed != 1 || s.buffOp.view.Phase != "confirmed" || !s.buffOp.view.ServerCastGo || !s.buffOp.view.AuraConfirmed || s.state.CastPending {
				t.Fatal("missing actual correlated proof", s.buffOp)
			}
			if s.state.ServerAssistGo != 0 || s.state.ServerCastGo != 0 {
				t.Fatal("buff polluted damage/healing counters")
			}
			advance(20 * time.Second)
			s.Tick(context.Background())
			if len(buffCasts(f)) != 1 {
				t.Fatal("known aura spammed")
			}
		})
	}
}
func TestBuffCurrentPartyIncludesFourthCompanionAndNoStrangers(t *testing.T) {
	s, f, advance := buffRig(t, 5, 1243)
	f.world.NearbyPlayers = nil
	for _, g := range []uint64{4, 5, 8, 99} {
		p := proto.Clone(f.world.Owner).(*worldv1.PlayerState)
		p.Guid = g
		p.X = float32(g % 10)
		f.world.NearbyPlayers = append(f.world.NearbyPlayers, p)
		if g != 99 {
			f.party.Group.Members = append(f.party.Group.Members, &partyv1.Member{Guid: g, Online: true})
		}
	}
	var targets []uint64
	for i := 0; i < 5; i++ {
		s.Tick(context.Background())
		if s.buffOp == nil || s.buffOp.view.Phase != "waiting_server" {
			t.Fatal("no next real party buff", i, s.state.BuffReason)
		}
		targets = append(targets, s.buffOp.view.TargetGuid)
		confirmBuff(t, s, false)
		advance(3 * time.Second)
	}
	if fmt.Sprint(targets) != "[1 3 4 5 8]" {
		t.Fatal(targets)
	}
	s.Tick(context.Background())
	if len(buffCasts(f)) != 5 {
		t.Fatal("arbitrary player buffed", buffCasts(f))
	}
}
func TestBuffClassAppropriateSelfArmorAndManaOnlyIntellect(t *testing.T) {
	for _, v := range []struct{ class, spell uint32 }{{5, 588}, {8, 7300}, {9, 696}} {
		t.Run(fmt.Sprint(v.class), func(t *testing.T) {
			s, f, _ := buffRig(t, v.class, v.spell)
			s.Tick(context.Background())
			if len(buffCasts(f)) != 1 || s.buffOp.view.TargetGuid != 1 || s.buffOp.view.SpellId != v.spell {
				t.Fatal(s.buffOp, s.state.BuffReason)
			}
		})
	}
	s, f, _ := buffRig(t, 8, 1459)
	f.world.Owner.PowerType = 3
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 1, 0, 1459, 600000)})
	s.Tick(context.Background())
	if len(buffCasts(f)) != 0 {
		t.Fatal("Intellect cast on energy user")
	}
	s, f, _ = buffRig(t, 4)
	s.Tick(context.Background())
	if len(buffCasts(f)) != 0 {
		t.Fatal("unlearned buff")
	}
}
func TestBuffPrioritySafetyAndNoImplicitEnable(t *testing.T) {
	cases := map[string]func(*Service, *fakeSession){
		"not enabled": func(s *Service, f *fakeSession) { s.state.Buffing = false }, "no full spellbook": func(s *Service, f *fakeSession) { s.spellbookKnown = false },
		"combat": func(s *Service, f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 }, "party combat": func(s *Service, f *fakeSession) { f.world.Owner.UnitFlags |= 0x80000 }, "injured own": func(s *Service, f *fakeSession) { f.world.Self.Health = 94 }, "party injury": func(s *Service, f *fakeSession) { f.world.Owner.Health = 80 },
		"silence": func(s *Service, f *fakeSession) { f.world.Self.UnitFlags |= 0x2000 }, "mana reserve": func(s *Service, f *fakeSession) { f.world.Self.Power = 619 }, "unknown mana": func(s *Service, f *fakeSession) { f.world.Self.PowerKnown = false },
		"lease": func(s *Service, f *fakeSession) {
			s.state.InteractionOwner = "professions"
			s.interactionUntil = s.now().Add(time.Minute)
		}, "recovery": func(s *Service, f *fakeSession) { s.recovery.state.Active = true },
		"cast pending": func(s *Service, f *fakeSession) { s.state.CastPending = true }, "auto attack": func(s *Service, f *fakeSession) { s.state.AutoAttacking = true }, "AA unknown": func(s *Service, f *fakeSession) { s.state.AutoAttackUnconfirmed = true }, "motion unknown": func(s *Service, f *fakeSession) { s.state.MotionUnconfirmed = true },
		"moving": func(s *Service, f *fakeSession) { s.state.Moving = true }, "dead": func(s *Service, f *fakeSession) { f.world.Self.Alive = false }, "unsupported flags": func(s *Service, f *fakeSession) { f.world.Self.MovementFlags = 0x200000 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, _ := buffRig(t, 5, 1243)
			mutate(s, f)
			s.lastWorld = f.world
			s.buffTickLocked(context.Background(), f.world)
			if len(buffCasts(f)) != 0 {
				t.Fatal("unsafe buff", s.state.BuffReason)
			}
		})
	}
	s, f, _ := buffRig(t, 5, 1243)
	s.Buffs(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
	s.Tick(context.Background())
	if len(buffCasts(f)) != 0 {
		t.Fatal("disable ignored")
	}
}
func TestBuffAmbiguityDoesNotReplayOrInventAura(t *testing.T) {
	for _, mode := range []string{"transport", "no GO", "no aura"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := buffRig(t, 5, 1243)
			f.world.Owner = nil
			if mode == "transport" {
				f.failSend = true
			}
			s.Tick(context.Background())
			if mode == "no aura" {
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: buffGo(s, 1)})
			}
			if mode == "no GO" {
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 1, 2, 1243, 600000)})
			}
			advance(10 * time.Second)
			s.Tick(context.Background())
			if s.buffOp.view.Phase != "outcome_unconfirmed" || s.state.BuffsConfirmed != 0 {
				t.Fatal(s.buffOp)
			}
			f.failSend = false
			s.Buffs(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
			s.Buffs(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
			advance(time.Minute)
			s.Tick(context.Background())
			if len(buffCasts(f)) != 1 {
				t.Fatal("ambiguous binding replayed")
			}
		})
	}
}
func TestBuffDefiniteRejectionCooldownAndAuraCorrelation(t *testing.T) {
	s, f, advance := buffRig(t, 5, 1243)
	f.world.Owner = nil
	s.Tick(context.Background())
	// An aura from a different caster cannot confirm this cast.
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 3, 2, 1243, 600000)})
	if s.buffOp.view.AuraConfirmed {
		t.Fatal("other caster credited")
	}
	p := healingRejectionPacket(opcode.SMSGCastFailed, 1, s.castCount, 1243)
	s.OnPacket(context.Background(), p)
	if s.buffOp.view.Phase != "server_rejected" || !s.state.Buffing || s.state.CastPending {
		t.Fatal(s.buffOp)
	}
	// Remove the other caster's actually observed aura before another attempt.
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 1, 2, 0, 0)})
	advance(29999 * time.Millisecond)
	s.Tick(context.Background())
	if len(buffCasts(f)) != 1 {
		t.Fatal("rejected buff spam")
	}
	advance(time.Millisecond)
	s.Tick(context.Background())
	if len(buffCasts(f)) != 2 {
		t.Fatal("fresh retry after definite rejection not possible", s.state.BuffReason)
	}
}
func TestBuffRemovalAndStrongerArmorSuppression(t *testing.T) {
	s, f, advance := buffRig(t, 8, 168)
	f.world.Owner = nil
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 1, 1, 7302, 600000)})
	s.Tick(context.Background())
	if len(buffCasts(f)) != 0 {
		t.Fatal("overwrote Ice Armor")
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 1, 1, 0, 0)})
	s.Tick(context.Background())
	confirmBuff(t, s, false)
	advance(6 * time.Second)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 1, 7, 0, 0)})
	s.Tick(context.Background())
	if len(buffCasts(f)) != 2 {
		t.Fatal("actual removal did not permit maintenance")
	}
}
func TestBuffCatalogAgainstActualClientDBC(t *testing.T) {
	path := os.Getenv("BUDDY_SPELL_DBC")
	if path == "" {
		t.Skip("optional independently extracted client fixture")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(b)) != "d5cce1a83550dcfa9eb2f0251dbb11fd24c272534b2b1a9b230924a44d817ab3" {
		t.Fatal("unverified DBC")
	}
	count, size := binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint32(b[12:])
	seen := map[uint32]bool{}
	for i := uint32(0); i < count; i++ {
		p := b[20+i*size:]
		id := binary.LittleEndian.Uint32(p)
		r := buffMetadata(id)
		if r.id == 0 {
			continue
		}
		seen[id] = true
		word := func(n int) uint32 { return binary.LittleEndian.Uint32(p[n*4:]) }
		if word(28) != 1 || word(42) != 0 || word(43) != 0 || word(204) != r.manaPercent {
			t.Fatal("catalog differs from actual DBC", id)
		}
		for j := 52; j < 68; j++ {
			if word(j) != 0 {
				t.Fatal("reagent", id)
			}
		}
	}
	if len(seen) < 30 {
		t.Fatal("catalog IDs absent from DBC")
	}
}
func TestBuffToolkitFullObservedSpellbookAndMetadataTruth(t *testing.T) {
	s, _, _ := buffRig(t, 5, 1243, 585, 99999)
	s.sessionID = "aw-actual-toolkit-body"
	out, err := s.SpellToolkit(context.Background(), &emptypb.Empty{})
	if err != nil || !out.SpellbookKnown || len(out.Spells) != 3 || out.SessionId != s.sessionID || out.SelfGuid != 1 {
		t.Fatal(out, err)
	}
	status := s.stateLocked()
	if status.SessionId != out.SessionId || status.SelfGuid != out.SelfGuid {
		t.Fatal("toolkit and status body identities disagree", out, status)
	}
	for _, v := range out.Spells {
		if v.SpellId == 1243 && !v.MetadataKnown || v.SpellId == 99999 && (v.MetadataKnown || v.Name != "" || v.AutomaticBuffSupported) {
			t.Fatal("invented metadata", v)
		}
	}
	before := s.spellbookRevision
	for i := 0; i < len(buffInitial(1243)); i++ {
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: buffInitial(1243)[:i]})
	}
	if len(s.knownSpells) != 3 || s.spellbookRevision != before {
		t.Fatal("partial spellbook committed")
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: buffInitial(1243, 585, 99999)})
	if s.spellbookRevision != before {
		t.Fatal("identical baseline changed revision")
	}
	learned := binary.LittleEndian.AppendUint32(nil, 696)
	learned = append(learned, 0, 0)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLearnedSpell, Payload: learned})
	if !s.knownSpells[696] || s.spellbookRevision != before+1 {
		t.Fatal("untracked learned spell missing")
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGRemovedSpell, Payload: learned[:4]})
	if s.knownSpells[696] {
		t.Fatal("removed spell retained")
	}
}

// Exercise final-read changes without teaching the production fake any buff
// behavior; callbacks mutate only the actual world/party returned by the rig.
type buffSnapshotChange struct {
	*fakeSession
	reads       int
	partyReads  int
	beforeParty func()
	change      func()
}

func (f *buffSnapshotChange) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, options ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-party" && r.Method == "state" {
		f.partyReads++
		if f.partyReads == 2 && f.beforeParty != nil {
			f.beforeParty()
		}
	}
	if r.Module == "buddy-world" && r.Method == "snapshot" {
		f.reads++
		if f.reads == 2 {
			f.change()
		}
	}
	return f.fakeSession.InvokeModule(ctx, r, options...)
}
func TestBuffFinalFreshCasterTargetAndMembershipGuard(t *testing.T) {
	for _, name := range []string{"combat", "mana", "dead", "class", "target range", "lease", "intent", "spell removed", "party"} {
		t.Run(name, func(t *testing.T) {
			s, f, _ := buffRig(t, 5, 1243)
			// Own aura is already present: select actual party owner3 for this cast.
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 1, 2, 1243, 600000)})
			change := func() {
				switch name {
				case "combat":
					f.world.Self.UnitFlags |= 0x80000
				case "mana":
					f.world.Self.Power = 1
				case "dead":
					f.world.Owner.Alive = false
				case "class":
					f.world.Self.ClassId = 4
				case "target range":
					f.world.Owner.X = 50
				case "lease":
					s.state.InteractionOwner = "trade"
				case "intent":
					s.state.Buffing = false
				case "spell removed":
					delete(s.knownSpells, 1243)
				case "party":
					f.world.Owner = nil
				}
			}
			callback := &buffSnapshotChange{fakeSession: f, change: change}
			if name == "party" {
				callback.change = func() {}
				callback.beforeParty = func() { f.party.Group.Members = f.party.Group.Members[:1] }
			}
			s.session = callback
			s.Tick(context.Background())
			if len(buffCasts(f)) != 0 {
				t.Fatal("final read change permitted cast", name, s.state.BuffReason)
			}
		})
	}
}
func TestBuffExactCastCorrelationAndExplicitDisable(t *testing.T) {
	for _, mode := range []string{"count", "spell", "caster", "target", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _ := buffRig(t, 5, 1243)
			s.Tick(context.Background())
			p := buffGo(s, 1)
			switch mode {
			case "count":
				p[4]++
			case "spell":
				binary.LittleEndian.PutUint32(p[5:], 1459)
			case "caster":
				p[3] = 3
			case "target":
				p[len(p)-1] = 3
			case "truncated":
				p = p[:len(p)-1]
			}
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: p})
			if s.buffOp.view.ServerCastGo || !s.state.CastPending {
				t.Fatal("uncorrelated/malformed GO settled cast")
			}
		})
	}
	s, _, _ := buffRig(t, 5, 1243)
	s.Tick(context.Background())
	s.Buffs(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
	confirmBuff(t, s, false)
	if s.state.Buffing || s.state.BuffsConfirmed != 1 {
		t.Fatal("receipt undid explicit disable")
	}
}
func TestBuffHealingAndLeaseExpiryPriority(t *testing.T) {
	s, f, advance := buffRig(t, 5, 1243, 2050)
	f.world.Owner.Health = 20
	s.state.Healing = true
	s.healingUntil = s.now().Add(time.Hour)
	s.Tick(context.Background())
	casts := buffCasts(f)
	if len(casts) != 1 || binary.LittleEndian.Uint32(casts[0].Payload[1:]) != 2050 || s.state.BuffAttempts != 0 {
		t.Fatal("buff displaced healing", casts)
	}
	s, f, advance = buffRig(t, 8, 168)
	advance(lease)
	s.Tick(context.Background())
	if len(buffCasts(f)) != 0 || s.state.Buffing {
		t.Fatal("expired buff authorization cast")
	}
}

func TestBuffSharedGlobalCooldownWithHealingAndOffense(t *testing.T) {
	s, f, advance := buffRig(t, 5, 1243, 2050)
	s.Tick(context.Background())
	confirmBuff(t, s, false)
	s.state.Healing = true
	s.healingUntil = s.now().Add(time.Hour)
	f.world.Owner.Health = 20
	advance(1499 * time.Millisecond)
	s.Tick(context.Background())
	if len(buffCasts(f)) != 1 {
		t.Fatal("heal entered instant buff GCD")
	}
	advance(time.Millisecond)
	s.Tick(context.Background())
	if len(buffCasts(f)) != 2 || binary.LittleEndian.Uint32(buffCasts(f)[1].Payload[1:]) != 2050 {
		t.Fatal("fresh heal after GCD missing", s.state.Reason)
	}
	for _, name := range []string{"heal", "offense"} {
		t.Run(name, func(t *testing.T) {
			s, f, advance := buffRig(t, 8, 168)
			if name == "heal" {
				s.nextHeal = s.now().Add(3 * time.Second)
			} else {
				s.nextAssist = s.now().Add(3 * time.Second)
			}
			s.Tick(context.Background())
			if len(buffCasts(f)) != 0 {
				t.Fatal("buff ignored prior shared throttle")
			}
			advance(3 * time.Second)
			s.Tick(context.Background())
			if len(buffCasts(f)) != 1 {
				t.Fatal("fresh buff after shared throttle missing", s.state.BuffReason)
			}
		})
	}
}

func TestBuffUnknownAuraBaselinePermitsOneNormalAttemptWithoutAbsenceClaim(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			s, f, _ := buffRig(t, 5, 1243)
			f.world.Owner = nil
			if partial {
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 1, 2, 588, 600000)})
			}
			s.Tick(context.Background())
			if len(buffCasts(f)) != 1 || s.state.BuffsConfirmed != 0 {
				t.Fatal("empty baseline omission blocked normal cast or invented confirmation")
			}
			if set := s.buffAuras[1]; set != nil && set.complete {
				t.Fatal("partial absence promoted to full baseline")
			}
		})
	}
}
func TestBuffLearnedStrongerArmorPreventsUnobservedDowngrade(t *testing.T) {
	for _, v := range []struct{ class, starter, stronger uint32 }{{8, 168, 7302}, {8, 168, 30482}, {8, 168, 43045}, {8, 168, 43046}, {9, 687, 706}, {9, 687, 28176}, {9, 687, 28189}, {9, 687, 47892}, {9, 687, 47893}} {
		s, f, _ := buffRig(t, v.class, v.starter, v.stronger)
		s.Tick(context.Background())
		for _, cast := range buffCasts(f) {
			if binary.LittleEndian.Uint32(cast.Payload[1:]) == v.starter {
				t.Fatal("starter cast despite known stronger self-only armor")
			}
		}
		s.setKnownSpell(v.stronger, false)
		before := len(buffCasts(f))
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(s.selfGUID, s.selfGUID, 2, v.stronger, 600000)})
		s.Tick(context.Background())
		if len(buffCasts(f)) != before {
			t.Fatal("starter cast despite observed stronger self-only armor")
		}
	}
}
func TestBuffAuraBounceRequiresActualTargetContextChange(t *testing.T) {
	for _, change := range []string{"none", "unrelated delta", "full baseline", "new rank"} {
		t.Run(change, func(t *testing.T) {
			s, f, advance := buffRig(t, 5, 1243)
			f.world.Owner = nil
			s.Tick(context.Background())
			rejection := healingRejectionPacket(opcode.SMSGCastFailed, 1, s.castCount, 1243)
			rejection.Payload[5] = 9
			s.OnPacket(context.Background(), rejection)
			advance(time.Minute)
			switch change {
			case "unrelated delta":
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 1, 2, 588, 600000)})
			case "full baseline":
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdateAll, Payload: packedGUID(1)})
			case "new rank":
				p := binary.LittleEndian.AppendUint32(nil, 1244)
				p = append(p, 0, 0)
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLearnedSpell, Payload: p})
			}
			s.Tick(context.Background())
			want := 1
			if change == "full baseline" || change == "new rank" {
				want = 2
			}
			if len(buffCasts(f)) != want {
				t.Fatal("bounce rearmed without exact positive context or did not rearm after fresh proof", change, len(buffCasts(f)))
			}
		})
	}
}
