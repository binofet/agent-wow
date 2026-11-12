package buddyprofessions

import (
	"context"
	"encoding/binary"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"testing"
)

func TestInspectHerbUsesActualEntryCacheAndNeverCasts(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	const guid uint64 = 0xf110000651000001 // Actual selected entry1617, counter1.
	f.mu.Lock()
	f.world.RequestedGameObjects = []*worldv1.GameObjectState{{Guid: guid, Entry: 1617, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 8, TypeKnown: true, GameObjectType: 3, FlagsKnown: true, StateKnown: true, State: 1, X: f.world.Self.X, Y: f.world.Self.Y, Z: f.world.Self.Z}}
	var lookups int
	f.worldHook = func(r *worldv1.SnapshotRequest) {
		lookups++
		if r.GameObjectEntry != 1617 || r.CreatureGuid != 0 {
			t.Errorf("wrong actual entry lookup: %v", r)
		}
	}
	f.mu.Unlock()
	state, err := s.InspectHerb(context.Background(), &professionsv1.HerbInspectRequest{SessionId: "body", TargetGuid: guid})
	if err != nil || state.Active || state.Operation != nil || len(s.requests) != 0 {
		t.Fatal(state, err)
	}
	if _, err := s.GetState(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if lookups != 2 || len(f.packets) != 1 {
		t.Fatal(lookups, len(f.packets))
	}
	p := f.packets[0]
	if p.Opcode != opcode.CMSGGameobjectQuery || len(p.Payload) != 12 || binary.LittleEndian.Uint32(p.Payload) != 1617 || binary.LittleEndian.Uint64(p.Payload[4:]) != guid {
		t.Fatal("inspection performed another action", p)
	}
}

func TestInspectHerbRejectsUnboundOrNonObjectInputBeforeLookup(t *testing.T) {
	for _, r := range []*professionsv1.HerbInspectRequest{nil, {SessionId: "foreign", TargetGuid: 0xf110000651000001}, {SessionId: "body"}, {SessionId: "body", TargetGuid: 0xf130000651000001}, {SessionId: "body", TargetGuid: 0xf110000651000000}, {SessionId: "body", TargetGuid: 0xf110000000000001}} {
		s, f := newProfessionServiceFixture(t)
		f.mu.Lock()
		f.rpcEvents = nil
		f.mu.Unlock()
		if _, err := s.InspectHerb(context.Background(), r); status.Code(err) != codes.InvalidArgument {
			t.Fatal(r, err)
		}
		f.mu.Lock()
		if len(f.rpcEvents) != 0 || len(f.packets) != 0 {
			t.Fatal("invalid inspection reached a callback")
		}
		f.mu.Unlock()
	}
}

func TestSkinningForeignWindowCannotAuthorizeStaleSlotsOrRelease(t *testing.T) {
	for _, duringLastRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_step", true: "during_last_world"}[duringLastRead], func(t *testing.T) {
			s, f := skinningFixture(t)
			op := skinningDirect(t, s, f)
			op.castReserved = true
			op.receipt.OwnGoObserved = true
			op.receipt.Status = professionsv1.AttemptStatus_WAITING_LOOT
			professionPacket(t, s, opcode.SMSGLootResponse, skinProtocolLootFixture(6, 1))
			foreign := skinProtocolLootFixture(6, 1)
			binary.LittleEndian.PutUint64(foreign, skinProtocolTarget+1)
			f.mu.Lock()
			f.packets = nil
			if duringLastRead {
				f.worldHook = func(_ *worldv1.SnapshotRequest) { professionPacket(t, s, opcode.SMSGLootResponse, foreign) }
			}
			f.mu.Unlock()
			if !duringLastRead {
				professionPacket(t, s, opcode.SMSGLootResponse, foreign)
			}
			if !s.stepSkin(op) || !op.windowInvalid || !op.receipt.CleanupUnconfirmed || op.receipt.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED {
				t.Fatal(op.receipt)
			}
			if err := s.skinCleanupWrite(context.Background(), op, opcode.CMSGLootRelease, releaseLootPacket(op.request.TargetGuid)); err == nil {
				t.Fatal("stale target release accepted after foreign window")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.packets) != 0 {
				t.Fatal("stale implicit-slot or release packet sent", f.packets)
			}
		})
	}
}

func TestSkinningMalformedOrRepeatedWindowCannotRetainSlotAuthority(t *testing.T) {
	for _, mode := range []string{"short_same_target", "bad_row", "duplicate", "foreign_after_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, f := skinningFixture(t)
			op := skinningDirect(t, s, f)
			op.castReserved = true
			op.receipt.OwnGoObserved = true
			op.receipt.Status = professionsv1.AttemptStatus_WAITING_LOOT
			good := skinProtocolLootFixture(6, 1)
			professionPacket(t, s, opcode.SMSGLootResponse, good)
			bad := append([]byte(nil), good...)
			switch mode {
			case "short_same_target":
				bad = bad[:9]
			case "bad_row":
				bad[14+21] = 1
			case "foreign_after_failure":
				binary.LittleEndian.PutUint64(bad, skinProtocolTarget+1)
				op.receipt.Status = professionsv1.AttemptStatus_SERVER_REJECTED
				op.receipt.Reason = "actual_own_failure"
				op.receipt.FailureCode = 72
			}
			professionPacket(t, s, opcode.SMSGLootResponse, bad)
			if !op.windowInvalid || !op.receipt.CleanupUnconfirmed || !s.stepSkin(op) {
				t.Fatal(op.receipt)
			}
			if mode == "foreign_after_failure" && (op.receipt.Status != professionsv1.AttemptStatus_SERVER_REJECTED || op.receipt.Reason != "actual_own_failure") {
				t.Fatal(op.receipt)
			}
			op.stored[0] = true
			op.releaseReserved = true
			professionPacket(t, s, opcode.SMSGLootRemoved, []byte{0})
			professionPacket(t, s, opcode.SMSGItemPushResult, professionPushReceipt(7, false, false, 2318, 1, 1))
			professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(skinProtocolTarget), 1))
			if op.receipt.LootSlotsRemoved != 0 || op.receipt.OwnLootedItemsObserved != 0 || op.receipt.LootReleaseObserved {
				t.Fatal("invalid-window receipts regained attribution", op.receipt)
			}
		})
	}
}
