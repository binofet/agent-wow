package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/creaturequery"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func sapFixture(t *testing.T) (*Service, *fakeSession, *actionsv1.SapRequest) {
	t.Helper()
	s, f, _, _ := polymorphFixture(t)
	self := f.world.Self
	self.ClassId, self.PowerType, self.Power, self.ShapeshiftForm = 4, 3, 100, 30
	self.Mainhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: 11, MetadataKnown: true, Entry: 1000, DurabilityKnown: true}
	s.weapons[1000] = weaponProof{entry: 1000, class: 2, subclass: 15, inventory: 13, valid: true}
	f.lookupCreatures[sheepTarget].X = 4
	s.setKnownSpell(6770, true)
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(1, 1, 1, 1784, 20000))
	return s, f, &actionsv1.SapRequest{SessionId: s.sessionID, SpellId: 6770, TargetGuid: sheepTarget}
}

func sapTemplateReply(entry, creatureType uint32) []byte {
	p := binary.LittleEndian.AppendUint32(nil, entry)
	p = append(p, "Creature\x00\x00\x00\x00\x00\x00"...)
	tail := make([]byte, 77)
	binary.LittleEndian.PutUint32(tail[4:], creatureType)
	return append(p, tail...)
}

func TestSapRequiresCreatureTypeAndCorrelatedCrowdControlEffect(t *testing.T) {
	s, f, req := sapFixture(t)
	ctx := context.Background()
	st, err := s.Sap(ctx, req)
	if err != nil || st.Reason != "sap_target_type_pending" || st.CastPending || len(f.packets) != 1 ||
		f.packets[0].Opcode != opcode.CMSGCreatureQuery || !bytes.Equal(f.packets[0].Payload, creaturequery.Request(555, sheepTarget)) {
		t.Fatalf("creature query: %v %v %v", st, err, f.packets)
	}
	// A reply for a different entry cannot enable this cast, and we only
	// send one query per wait window.
	emitPolymorph(s, opcode.SMSGCreatureQueryResponse, sapTemplateReply(556, 7))
	st, err = s.Sap(ctx, req)
	if err != nil || st.Reason != "sap_target_type_pending" || len(f.packets) != 1 {
		t.Fatalf("unrelated query: %v %v %v", st, err, f.packets)
	}
	emitPolymorph(s, opcode.SMSGCreatureQueryResponse, sapTemplateReply(555, 7))
	st, err = s.Sap(ctx, req)
	if err != nil || !st.CastPending || !st.LastSpellOperation.TransportWritten ||
		st.LastSpellOperation.Capability != "sap" || st.LastSpellOperation.EffectObserved || len(f.packets) != 2 {
		t.Fatalf("cast: %v %v %v", st, err, f.packets)
	}
	write := f.packets[1]
	target, _, _ := readGUID(write.Payload[10:])
	if write.Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(write.Payload[1:]) != 6770 || target != sheepTarget {
		t.Fatal(write)
	}
	goPacket := polymorphGo(s.castCount, 1, sheepTarget, true)
	binary.LittleEndian.PutUint32(goPacket[2*len(packedGUID(1))+1:], 6770)
	emitPolymorph(s, opcode.SMSGSpellGo, goPacket)
	st = state(t, s)
	if !st.CastPending || !st.LastSpellOperation.ServerCastGo || st.LastSpellOperation.EffectObserved {
		t.Fatal("GO alone settled effect", st)
	}
	aura := sheepAura(sheepTarget, 1, 6770, 20000)
	aura[len(packedGUID(sheepTarget))+5] = 0xa1 // Negative, duration, effect 0.
	emitPolymorph(s, opcode.SMSGAuraUpdate, aura)
	st = state(t, s)
	if st.CastPending || st.LastSpellOperation.Phase != "confirmed_aura" || !st.LastSpellOperation.EffectObserved {
		t.Fatal(st)
	}
	if len(st.ProtectedTargets) != 1 || st.ProtectedTargets[0].TargetGuid != sheepTarget || st.ProtectedTargets[0].Capability != "sap" {
		t.Fatal("Sap target is not protected", st.ProtectedTargets)
	}
}

func TestSapRejectsUnobservedStealthCombatAndIneligibleType(t *testing.T) {
	for _, scenario := range []string{"stealth", "caster-combat", "target-combat", "energy", "undead"} {
		t.Run(scenario, func(t *testing.T) {
			s, f, req := sapFixture(t)
			switch scenario {
			case "stealth":
				emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(1, 1, 1, 0, 0))
			case "caster-combat":
				f.world.Self.UnitFlags = 0x80000
			case "target-combat":
				f.lookupCreatures[sheepTarget].UnitFlags = 0x80000
			case "energy":
				f.world.Self.Power = 64
			case "undead":
				s.controlTemplates = map[uint32]controlTemplate{555: {creatureType: 6}}
			}
			if _, err := s.Sap(context.Background(), req); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
				t.Fatalf("unsafe dispatch: %v %v", err, f.packets)
			}
		})
	}
}

func TestSapUsesCompletedMovementWhenBothObservedPositionsAreInRange(t *testing.T) {
	for _, endpoint := range []float32{4.5, 6} {
		s, f, req := sapFixture(t)
		s.controlTemplates = map[uint32]controlTemplate{555: {creatureType: 7}}
		f.lookupCreatures[sheepTarget].Trajectory = &worldv1.CreatureTrajectory{
			Known: true, DurationMs: 150, AgeMs: 400, DestinationKnown: true, DestinationX: endpoint,
		}
		st, err := s.Sap(context.Background(), req)
		if endpoint < 5 {
			if err != nil || st.GetLastSpellOperation().GetCapability() != "sap" || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatalf("in-range observed trajectory did not allow Sap: %v %v", st, err)
			}
		} else if status.Convert(err).Message() != "selected NPC is outside crowd-control spell range" || len(f.packets) != 0 {
			t.Fatalf("out-of-range endpoint should return its exact refusal without casting: %v %v", st, err)
		}
	}
}

func TestExactSapNeverChoosesOrPreparesLearnedStealth(t *testing.T) {
	s, f, req := sapFixture(t)
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(1, 1, 1, 0, 0))
	f.world.Self.ShapeshiftForm = 0
	learnSpells(s, 6770, 1784, 1785)
	s.controlTemplates = map[uint32]controlTemplate{555: {creatureType: 7}}
	if _, err := s.Sap(context.Background(), req); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
		t.Fatal("exact Sap chose a Stealth rank or moved", err, f.packets)
	}
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("native tick prepared Stealth")
	}
}
