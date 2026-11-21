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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func genericCastRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	s, f, advance := rig(t)
	s.sessionID = "generic-live-body"
	s.encounterVisit = encounterVisit{revision: 1, bound: true, mapID: f.world.Self.MapId}
	f.world.VisitRevision = 1
	// Spellbook baseline exists BEFORE later learned events, like a running actor.
	s.applySpellbookLocked(map[uint32]bool{133: true})
	return s, f, advance
}
func genericRequest(s *Service, id uint32) *actionsv1.CastSpellRequest {
	return &actionsv1.CastSpellRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SpellId: id}
}
func genericGo(s *Service, id uint32, target *actionsv1.SpellCastTargets) []byte {
	p := append(packedGUID(s.selfGUID), packedGUID(s.selfGUID)...)
	p = append(p, s.castCount)
	p = binary.LittleEndian.AppendUint32(p, id)
	p = binary.LittleEndian.AppendUint32(p, 0)
	p = binary.LittleEndian.AppendUint32(p, 1)
	p = append(p, 0, 0) // zero hits, zero misses is valid for area/implicit casts
	if target != nil && target.Mask&0x40 != 0 {
		p = binary.LittleEndian.AppendUint32(p, 0x40)
		p = append(p, 0)
		p = binary.LittleEndian.AppendUint32(p, math.Float32bits(target.Destination.X))
		p = binary.LittleEndian.AppendUint32(p, math.Float32bits(target.Destination.Y))
		p = binary.LittleEndian.AppendUint32(p, math.Float32bits(target.Destination.Z))
	} else {
		p = binary.LittleEndian.AppendUint32(p, 0)
	}
	return p
}

func TestCastSpellLearnsNewGroundImplicitAndUncataloguedSpellsInRunningActor(t *testing.T) {
	s, f, _ := genericCastRig(t)
	ctx := context.Background()
	for _, id := range []uint32{2120, 10, 1449, 4000000000} {
		if _, err := s.CastSpell(ctx, genericRequest(s, id)); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("unlearned cast admitted", err)
		}
		payload := binary.LittleEndian.AppendUint32(nil, id)
		payload = append(payload, 0, 0)
		if _, err := s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGLearnedSpell, Payload: payload}); err != nil {
			t.Fatal(err)
		}
		r := genericRequest(s, id)
		if id == 2120 || id == 10 {
			r.Targets = &actionsv1.SpellCastTargets{Mask: 64, Destination: &actionsv1.SpellCastLocation{X: 1, Y: 2, Z: 3}}
		}
		before := len(f.packets)
		st, err := s.CastSpell(ctx, r)
		if err != nil || len(f.packets) != before+1 || !st.CastPending || !st.LastSpellOperation.TransportWritten {
			t.Fatal(id, st, err)
		}
		p := f.packets[before]
		wantLen := 10
		if r.Targets != nil {
			wantLen = 23
		}
		if p.Opcode != opcode.CMSGCastSpell || len(p.Payload) != wantLen || binary.LittleEndian.Uint32(p.Payload[1:]) != id {
			t.Fatal("wrong learned transport", p)
		}
		// Every actual learned cast is cancellable even before any server response.
		if st.LastSpellOperation.OperationRevision == 0 || st.LastSpellOperation.VisitId != 1 {
			t.Fatal("missing exact lifecycle identity", st)
		}
		if _, err = s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: genericGo(s, id, r.Targets)}); err != nil {
			t.Fatal(err)
		}
		if id == 10 { // Blizzard keeps channel custody after GO, then observed end resolves.
			_, _ = s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.MSGChannelStart, Payload: selfCastChannelPacket(s.selfGUID, id, 8000)})
			_, _ = s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.MSGChannelUpdate, Payload: selfCastChannelPacket(s.selfGUID, 0)})
		}
		st = state(t, s)
		if st.CastPending || st.LastSpellOperation.EffectObserved || !st.LastSpellOperation.ServerCastGo {
			t.Fatal("GO invented effect or retained ordinary custody", id, st)
		}
	}
}

func TestCastSpellDoesNotApplySpecializedGameplayAdmission(t *testing.T) {
	s, f, _ := genericCastRig(t)
	r := genericRequest(s, 133)
	f.world.Self.Alive = false
	f.world.Self.Health = 0
	f.world.Self.Power = 0
	f.world.Self.ShapeshiftForm = 31
	f.world.Self.MovementFlags = 1
	s.state.Moving = true
	s.state.InteractionOwner = "training"
	s.interactionUntil = s.now().Add(time.Minute)
	s.nextHeal = s.now().Add(time.Minute)
	s.nextAssist = s.nextHeal
	if _, err := s.CastSpell(context.Background(), r); err != nil || len(f.packets) != 1 {
		t.Fatal("native gameplay pre-judgment", err, f.packets)
	}
	if _, err := s.CastSpell(context.Background(), r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("pending write replayed", err)
	}
}

func TestCastSpellBindsSessionVisitObservedTargetsAndUnknownWrite(t *testing.T) {
	s, f, _ := genericCastRig(t)
	base := genericRequest(s, 133)
	for _, mutate := range []func(*actionsv1.CastSpellRequest){
		func(r *actionsv1.CastSpellRequest) { r.SessionId = "old" }, func(r *actionsv1.CastSpellRequest) { r.SelfGuid = 3 }, func(r *actionsv1.CastSpellRequest) { r.VisitId = 2 },
		func(r *actionsv1.CastSpellRequest) { r.Targets = &actionsv1.SpellCastTargets{Mask: 2, ObjectGuid: 999} },
		func(r *actionsv1.CastSpellRequest) {
			r.Targets = &actionsv1.SpellCastTargets{Mask: 16, ItemGuid: 0x4000000000000099}
		},
	} {
		r := proto.Clone(base).(*actionsv1.CastSpellRequest)
		mutate(r)
		if _, err := s.CastSpell(context.Background(), r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
			t.Fatal(r, err, f.packets)
		}
	}
	// Dead units are still valid explicit targets; server decides the spell's requirements.
	f.world.NearbyPlayers = []*worldv1.PlayerState{{Guid: 4, Present: true, MapKnown: true, MapId: f.world.Self.MapId}}
	base.Targets = &actionsv1.SpellCastTargets{Mask: 2, ObjectGuid: 4}
	f.failSend = true
	st, err := s.CastSpell(context.Background(), base)
	if err != nil || !st.CastPending || st.LastSpellOperation.Phase != "transport_unconfirmed_no_retry" {
		t.Fatal(st, err)
	}
	if _, err = s.CastSpell(context.Background(), base); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("ambiguous write replayed", err)
	}
	// Correlated definitive rejection ends this operation, without attributing an effect.
	failed := []byte{s.castCount}
	failed = binary.LittleEndian.AppendUint32(failed, 133)
	failed = append(failed, 47)
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: failed})
	if st = state(t, s); st.CastPending || st.LastSpellOperation.Phase != "server_rejected_no_replay" {
		t.Fatal(st)
	}
}
