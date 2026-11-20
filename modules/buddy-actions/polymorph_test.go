package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
)

const sheepTarget = uint64(0xf13000022b000001)

func polymorphFixture(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.PolymorphRequest) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = "mage-session"
	f.world.Self.ClassId = 8
	f.lookupCreatures = map[uint64]*worldv1.CreatureState{sheepTarget: {
		Guid: sheepTarget, Entry: 555, Present: true, MapKnown: true, PositionKnown: true,
		X: 15, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, UnitFlagsKnown: true,
	}}
	// Complete wire spellbook, not catalog membership, grants learned authority.
	book := []byte{0, 1, 0, 118, 0, 0, 0, 0, 0, 0, 0}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: book})
	return s, f, advance, &actionsv1.PolymorphRequest{SessionId: s.sessionID, SpellId: 118, TargetGuid: sheepTarget}
}

func polymorphGo(count byte, caster, target uint64, hit bool) []byte {
	p := append(packedGUID(caster), packedGUID(caster)...)
	p = append(p, count)
	p = binary.LittleEndian.AppendUint32(p, 118)
	p = binary.LittleEndian.AppendUint32(p, 0x800|0x100)
	p = binary.LittleEndian.AppendUint32(p, 0)
	if hit {
		p = append(p, 1)
		p = binary.LittleEndian.AppendUint64(p, target)
		p = append(p, 0)
	} else {
		p = append(p, 0, 1)
		p = binary.LittleEndian.AppendUint64(p, target)
		p = append(p, 7) // IMMUNE
	}
	p = binary.LittleEndian.AppendUint32(p, 2)
	p = append(p, packedGUID(target)...)
	return binary.LittleEndian.AppendUint32(p, 75)
}

func sheepAura(guid, caster uint64, spell uint32, duration uint32) []byte {
	p := buffAura(guid, caster, 2, spell, duration)
	p[len(packedGUID(guid))+5] = 0xa3 // negative, duration, effects 0 and 1
	return p
}

func emitPolymorph(s *Service, op uint32, p []byte) {
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: p})
}

func TestMalformedTransferDoesNotSettlePendingCrowdControl(t *testing.T) {
	ctx := context.Background()
	s, _, _, req := polymorphFixture(t)
	if st, err := s.Polymorph(ctx, req); err != nil || !st.GetCastPending() {
		t.Fatalf("dispatch: %v %v", st, err)
	}
	emitPolymorph(s, opcode.SMSGTransferPending, []byte{1})
	if got := state(t, s); !got.GetCastPending() || got.GetMalformedPackets() != 1 {
		t.Fatalf("malformed frame changed pending receipt: %v", got)
	}
	emitPolymorph(s, opcode.SMSGTransferPending, []byte{1, 0, 0, 0})
	if got := state(t, s); got.GetCastPending() || got.GetLastSpellOperation().GetPhase() != "world_transfer_outcome_unconfirmed" {
		t.Fatalf("valid transfer left a pending receipt: %v", got)
	}
}

func TestPolymorphSelectedCastRequiresCorrelatedGoAndFreshTargetAura(t *testing.T) {
	for _, auraFirst := range []bool{false, true} {
		s, f, _, req := polymorphFixture(t)
		ctx := context.Background()
		// Old matching full/delta packets cannot qualify the new dispatch.
		emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(sheepTarget, 1, 118, 20000))
		st, err := s.Polymorph(ctx, req)
		if err != nil || !st.CastPending || !st.LastSpellOperation.TransportWritten ||
			st.LastSpellOperation.EffectObserved || len(f.packets) != 1 || len(f.lookups) != 1 || f.lookups[0] != sheepTarget {
			t.Fatalf("dispatch: %v %v %v", st, err, f.packets)
		}
		write := f.packets[0]
		target, _, _ := readGUID(write.Payload[10:])
		if write.Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(write.Payload[1:]) != 118 || target != sheepTarget {
			t.Fatal(write)
		}
		emitGo := func() { emitPolymorph(s, opcode.SMSGSpellGo, polymorphGo(s.castCount, 1, sheepTarget, true)) }
		emitAura := func() { emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(sheepTarget, 1, 118, 20000)) }
		first, second := emitGo, emitAura
		if auraFirst {
			first, second = second, first
		}
		first()
		st, _ = s.Status(ctx, &emptypb.Empty{})
		if !st.CastPending || st.LastSpellOperation.EffectObserved || st.LastSpellOperation.ServerCastGo == auraFirst {
			t.Fatal("one packet alone settled effect", st)
		}
		second()
		st, _ = s.Status(ctx, &emptypb.Empty{})
		if st.CastPending || st.LastSpellOperation.Phase != "confirmed_aura" || !st.LastSpellOperation.EffectObserved || !st.LastSpellOperation.ServerCastGo {
			t.Fatal(st)
		}
	}
}

func TestPolymorphLetsServerAdjudicateTargetMovementAndRange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*worldv1.CreatureState)
	}{
		{name: "elapsed"},
		{"raw position out of range", func(c *worldv1.CreatureState) { c.X = 31 }},
		{"terminal out of range", func(c *worldv1.CreatureState) { c.Trajectory.DestinationX = 31 }},
		{"terminal unknown", func(c *worldv1.CreatureState) { c.Trajectory.DestinationKnown = false }},
		{"active and recent, future point out of range", func(c *worldv1.CreatureState) {
			c.Trajectory.AgeMs, c.Trajectory.DestinationX = 300, 31
		}},
		{"active older raw sample", func(c *worldv1.CreatureState) {
			c.Trajectory.AgeMs, c.Trajectory.DurationMs, c.PositionAgeMs = 1600, 2000, 1600
		}},
		{"cyclic", func(c *worldv1.CreatureState) { c.Trajectory.Cyclic = true }},
		{"transport relative", func(c *worldv1.CreatureState) {
			c.Trajectory.AgeMs, c.Trajectory.TransportRelative = 300, true
		}},
		{"zero duration", func(c *worldv1.CreatureState) { c.Trajectory.DurationMs = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _, req := polymorphFixture(t)
			c := f.lookupCreatures[sheepTarget]
			c.Trajectory = &worldv1.CreatureTrajectory{
				Known: true, DurationMs: 1000, AgeMs: 1000, DestinationKnown: true, DestinationX: 20,
			}
			if tc.change != nil {
				tc.change(c)
			}
			ctx := context.Background()
			st, err := s.Polymorph(ctx, req)
			if err != nil || !st.CastPending || countPackets(f, opcode.CMSGCastSpell) != 1 || len(f.packets) != 1 {
				t.Fatal("expected exactly one cast and no movement", st, err, f.packets)
			}
			if tc.name == "raw position out of range" {
				failure := binary.LittleEndian.AppendUint32([]byte{s.castCount}, 118)
				emitPolymorph(s, opcode.SMSGCastFailed, append(failure, 1))
				st, _ = s.Status(ctx, &emptypb.Empty{})
				if st.CastPending || st.LastSpellOperation.Phase != "server_rejected_no_replay" || len(f.packets) != 1 {
					t.Fatal("server rejection did not settle the single attempt", st, f.packets)
				}
				return
			}
			emitPolymorph(s, opcode.SMSGSpellGo, polymorphGo(s.castCount, 1, sheepTarget, true))
			emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(sheepTarget, 1, 118, 20000))
			st, _ = s.Status(ctx, &emptypb.Empty{})
			if st.CastPending || st.LastSpellOperation.Phase != "confirmed_aura" || !st.LastSpellOperation.EffectObserved {
				t.Fatal("exact hit and aura did not confirm the cast", st)
			}
		})
	}
}

func TestPolymorphDoesNotConfirmForeignRemovedMissedOrLateEffects(t *testing.T) {
	for _, scenario := range []string{"wrong-caster", "wrong-target", "wrong-spell", "wrong-count", "foreign-go", "wrong-go-target", "friendly-aura", "full-snapshot", "removed", "malformed", "miss", "expired", "transfer"} {
		t.Run(scenario, func(t *testing.T) {
			s, f, advance, req := polymorphFixture(t)
			ctx := context.Background()
			if _, err := s.Polymorph(ctx, req); err != nil {
				t.Fatal(err)
			}
			goPacket := polymorphGo(s.castCount, 1, sheepTarget, true)
			aura := sheepAura(sheepTarget, 1, 118, 20000)
			auraOpcode := uint32(opcode.SMSGAuraUpdate)
			switch scenario {
			case "wrong-caster":
				aura = sheepAura(sheepTarget, 3, 118, 20000)
			case "wrong-target":
				aura = sheepAura(sheepTarget+1, 1, 118, 20000)
			case "wrong-spell":
				aura = sheepAura(sheepTarget, 1, 12824, 20000)
			case "wrong-count":
				goPacket = polymorphGo(s.castCount+1, 1, sheepTarget, true)
			case "foreign-go":
				goPacket = polymorphGo(s.castCount, 3, sheepTarget, true)
			case "wrong-go-target":
				goPacket = polymorphGo(s.castCount, 1, sheepTarget+1, true)
			case "friendly-aura":
				aura = buffAura(sheepTarget, 1, 2, 118, 20000)
			case "full-snapshot":
				auraOpcode = opcode.SMSGAuraUpdateAll
			case "malformed":
				aura = aura[:len(aura)-1]
			case "miss":
				goPacket = polymorphGo(s.castCount, 1, sheepTarget, false)
			case "transfer":
				emitPolymorph(s, opcode.SMSGNewWorld, make([]byte, 20))
			case "expired":
				advance(5 * time.Second)
			}
			emitPolymorph(s, auraOpcode, aura)
			if scenario == "removed" {
				emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(sheepTarget, 1, 2, 0, 0))
			}
			emitPolymorph(s, opcode.SMSGSpellGo, goPacket)
			advance(5 * time.Second)
			st, _ := s.Status(ctx, &emptypb.Empty{})
			if st.CastPending || st.LastSpellOperation.EffectObserved || st.LastSpellOperation.Phase == "confirmed_aura" || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal(st)
			}
		})
	}
}

func TestPolymorphBlocksUnlearnedForeignOrUnobservedRequests(t *testing.T) {
	for _, scenario := range []string{"rank", "sap", "session", "no-exact-observation", "unknown-position",
		"invalid-position", "transport-local-position", "wrong-map", "dead", "unattackable", "moving", "mana", "busy"} {
		t.Run(scenario, func(t *testing.T) {
			s, f, _, req := polymorphFixture(t)
			c := f.lookupCreatures[sheepTarget]
			switch scenario {
			case "rank":
				req.SpellId = 12824
			case "sap":
				s.setKnownSpell(6770, true)
				req.SpellId = 6770
			case "session":
				req.SessionId = "prior"
			case "no-exact-observation":
				f.world.NearbyCreatures = []*worldv1.CreatureState{c}
				delete(f.lookupCreatures, sheepTarget)
			case "unknown-position":
				c.PositionKnown = false
			case "invalid-position":
				c.X = float32(math.NaN())
			case "transport-local-position":
				c.PositionKnown = false
				c.Trajectory = &worldv1.CreatureTrajectory{Known: true, TransportRelative: true}
			case "wrong-map":
				c.MapId = f.world.Self.MapId + 1
			case "dead":
				c.Alive = false
			case "unattackable":
				c.UnitFlags = 0x2
			case "moving":
				f.world.Self.MovementFlags = 1
			case "mana":
				f.world.Self.Power = 0
			case "busy":
				s.state.InteractionOwner = "loot"
			}
			if _, err := s.Polymorph(context.Background(), req); err == nil || len(f.packets) != 0 {
				t.Fatal("unsafe dispatch", err, f.packets)
			}
		})
	}
}
