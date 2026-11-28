package buddyactions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
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
	s.sessionID = f.party.SessionId
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

func castBuff(t *testing.T, s *Service, target uint64, spell uint32) {
	t.Helper()
	_, err := s.CastBuff(context.Background(), &actionsv1.CastBuffRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: target, SpellId: spell})
	if err != nil {
		t.Fatal(err)
	}
}
func TestBuffExactLearnedRankNormalWireAndActualAura(t *testing.T) {
	for _, auraFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(auraFirst), func(t *testing.T) {
			s, f, advance := buffRig(t, 5, 1243, 1244)
			if !f.world.Self.ChannelSpellKnown || !f.world.Self.ChannelObjectKnown {
				t.Fatal("fixture must represent a complete own CREATE's omitted-zero baseline")
			}
			// The harness honors the caller's lower learned rank; rank policy is Lua.
			castBuff(t, s, s.selfGUID, 1243)
			if got := buffCasts(f); len(got) != 1 || !bytes.Equal(got[0].Payload, spellPacket(s.castCount, 1243, s.selfGUID)) {
				t.Fatal("selected rank packet changed", got)
			}
			if !auraFirst {
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: buffGo(s, s.selfGUID)})
				if s.state.BuffsConfirmed != 0 || s.buffOp.view.Phase != "waiting_server" {
					t.Fatal("GO invented aura")
				}
			}
			confirmBuff(t, s, auraFirst)
			if s.state.BuffsConfirmed != 1 || s.buffOp.view.Phase != "confirmed" || !s.buffOp.view.AuraConfirmed || s.state.CastPending {
				t.Fatal("missing fresh correlated aura", s.buffOp)
			}
			advance(time.Minute)
			s.Tick(context.Background())
			if len(buffCasts(f)) != 1 {
				t.Fatal("native Tick chose another buff")
			}
		})
	}
}
func TestBuffNativeTickNeverChoosesOrRetries(t *testing.T) {
	for _, toggle := range []bool{false, true} {
		s, f, advance := buffRig(t, 9, 5697, 687)
		s.Buffs(context.Background(), &actionsv1.ToggleRequest{Enabled: toggle})
		for range 3 {
			s.Tick(context.Background())
			advance(time.Minute)
		}
		if len(buffCasts(f)) != 0 {
			t.Fatal("native automatic buff decision remained", toggle)
		}
		castBuff(t, s, ownerGUID, 5697)
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: buffGo(s, ownerGUID)})
		advance(4 * time.Second)
		s.Tick(context.Background())
		if s.buffOp.view.Phase != "outcome_unconfirmed" || s.state.BuffsConfirmed != 0 {
			t.Fatal("missing aura claimed effect", s.buffOp)
		}
		advance(time.Hour)
		s.Tick(context.Background())
		if len(buffCasts(f)) != 1 {
			t.Fatal("native replayed unverified buff")
		}
	}
}
func TestBuffAcceptsObservedPassengerAndMountedAlly(t *testing.T) {
	for _, tc := range []struct {
		name                                   string
		class, spell, movementFlags, unitFlags uint32
	}{{"Fortitude on passenger", 5, 2791, 0x200, 8}, {"Arcane Intellect on mounted ally", 8, 10156, 0, 0x08000008}} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := buffRig(t, tc.class, tc.spell)
			f.world.Self.Level, f.world.Self.UnitFlags = 45, 8
			ally := proto.Clone(f.world.Owner).(*worldv1.PlayerState)
			ally.Guid, ally.MovementFlags, ally.UnitFlags = 8, tc.movementFlags, tc.unitFlags
			f.world.NearbyPlayers = []*worldv1.PlayerState{ally}
			f.party.Group.Members = append(f.party.Group.Members, &partyv1.Member{Guid: 8, Online: true})
			castBuff(t, s, 8, tc.spell)
			if casts := buffCasts(f); len(casts) != 1 || !bytes.Equal(casts[0].Payload, spellPacket(s.castCount, tc.spell, 8)) || s.state.BuffsConfirmed != 0 {
				t.Fatal("expected one attempted cast on the observed ally, pending the realm's effect", casts, s.buffOp)
			}
			confirmBuff(t, s, false)
			if s.state.BuffsConfirmed != 1 || !s.buffOp.view.AuraConfirmed {
				t.Fatal("matching spell GO and aura failed to confirm the buff", s.buffOp)
			}
		})
	}
}

func TestBuffExactAdmissionRejectsChangedBodyTargetAndResources(t *testing.T) {
	for _, name := range []string{"session", "body", "unlearned", "dead", "absent", "inferred", "other_map", "range", "enemy", "self_only", "mana", "silenced", "fear", "channel", "channel_object", "channel_spell_unknown", "channel_object_unknown", "interaction", "gcd"} {
		t.Run(name, func(t *testing.T) {
			s, f, _ := buffRig(t, 5, 1243, 588)
			r := &actionsv1.CastBuffRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: ownerGUID, SpellId: 1243}
			switch name {
			case "session":
				r.SessionId = "foreign"
			case "body":
				r.SelfGuid = 99
			case "unlearned":
				r.SpellId = 1244
			case "dead":
				f.world.Owner.Alive = false
			case "absent":
				f.world.Owner.Present = false
			case "inferred":
				f.world.Owner.PositionInferred = true
			case "other_map":
				f.world.Owner.MapId++
			case "range":
				f.world.Owner.X = 60
			case "enemy":
				f.party.Group.Members = f.party.Group.Members[:1]
				f.world.Self.RaceKnown = true
				f.world.Self.RaceId = 1
				f.world.Owner.RaceKnown = true
				f.world.Owner.RaceId = 2
			case "self_only":
				r.SpellId = 588
			case "mana":
				f.world.Self.Power = 0
			case "silenced":
				f.world.Self.UnitFlags |= 0x2000
			case "fear":
				f.world.Self.UnitFlags |= 0x800000
			case "channel":
				f.world.Self.ChannelSpellId = 5143
			case "channel_object":
				f.world.Self.ChannelObjectGuid = 99
			case "channel_spell_unknown":
				f.world.Self.ChannelSpellKnown = false
			case "channel_object_unknown":
				f.world.Self.ChannelObjectKnown = false
			case "interaction":
				s.state.InteractionOwner = "trade"
				s.interactionUntil = s.now().Add(time.Hour)
			case "gcd":
				s.nextAssist = s.now().Add(time.Second)
			}
			_, err := s.CastBuff(context.Background(), r)
			if status.Code(err) != codes.FailedPrecondition || len(buffCasts(f)) != 0 {
				t.Fatal("invalid exact buff wrote packet", name, err)
			}
		})
	}
}
func TestBuffUnknownTransportAndRejectedCastNeverReplayed(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		s, f, advance := buffRig(t, 5, 1243)
		f.failSend = unknown
		castBuff(t, s, s.selfGUID, 1243)
		if !unknown {
			failed := append([]byte{s.castCount}, binary.LittleEndian.AppendUint32(nil, 1243)...)
			failed = append(failed, 9)
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: failed})
			if s.buffOp.view.Phase != "server_rejected" {
				t.Fatal(s.buffOp)
			}
		} else if s.buffOp.view.Phase != "outcome_unconfirmed" {
			t.Fatal(s.buffOp)
		}
		advance(time.Minute)
		s.Tick(context.Background())
		if len(buffCasts(f)) != 1 || s.state.BuffsConfirmed != 0 {
			t.Fatal("invented success or native replay")
		}
	}
}
func TestBuffSharedCatalogGlobalCooldown(t *testing.T) {
	s, f, advance := buffRig(t, 5, 1243, 1244)
	castBuff(t, s, s.selfGUID, 1243)
	confirmBuff(t, s, false)
	advance(1499 * time.Millisecond)
	_, err := s.CastBuff(context.Background(), &actionsv1.CastBuffRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: ownerGUID, SpellId: 1244})
	if status.Code(err) != codes.FailedPrecondition || len(buffCasts(f)) != 1 {
		t.Fatal("buff ignored real GCD", err)
	}
	advance(time.Millisecond)
	castBuff(t, s, ownerGUID, 1244)
	if len(buffCasts(f)) != 2 {
		t.Fatal("exact later cast blocked by policy timer")
	}
}
func TestBuffExactCastCorrelationAndExplicitDisable(t *testing.T) {
	for _, mode := range []string{"count", "spell", "caster", "target", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _ := buffRig(t, 5, 1243)
			castBuff(t, s, s.selfGUID, 1243)
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
	castBuff(t, s, s.selfGUID, 1243)
	s.Buffs(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
	confirmBuff(t, s, false)
	if s.state.Buffing || s.state.BuffsConfirmed != 1 {
		t.Fatal("receipt undid explicit disable")
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
