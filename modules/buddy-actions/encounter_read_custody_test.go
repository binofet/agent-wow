package buddyactions

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type encounterReadHook struct {
	*fakeSession
	module    string
	hook      func()
	completed bool
	finished  chan struct{}
}

func (f *encounterReadHook) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == f.module {
		done := make(chan struct{})
		f.finished = done
		go func() { f.hook(); close(done) }()
		select {
		case <-done:
			f.completed = true
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.fakeSession.InvokeModule(ctx, r, opts...)
}

// A World/party callback may depend on the Actions packet queue completing.
// This exercises the actual callback and visit parser, without a sleep or any
// external transport. Holding Actions.mu across the callback deadlocks until
// the existing read deadline and returns a partial frame on the baseline.
func TestEncounterObservationRemoteReadsAllowPacketProgress(t *testing.T) {
	for _, module := range []string{"buddy-world", "buddy-party"} {
		t.Run(module, func(t *testing.T) {
			s, f, _ := rig(t)
			s.sessionID = f.party.SessionId
			f.world.VisitRevision = 1
			entry := make([]byte, 20)
			binary.LittleEndian.PutUint32(entry, f.world.Self.MapId)
			h := &encounterReadHook{fakeSession: f, module: module}
			h.hook = func() {
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLoginVerifyWorld, Payload: entry})
			}
			s.session = h
			out, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
			if h.finished != nil {
				<-h.finished
			}
			if err != nil || !h.completed || out.Reason != "" || out.VisitId != 1 || len(out.Players) == 0 {
				t.Fatalf("packet queue could not bind current World during %s read: completed=%v out=%v err=%v", module, h.completed, out, err)
			}
			if len(f.packets) != 0 {
				t.Fatal("observation sent gameplay packets")
			}
		})
	}
}

func TestEncounterObservationRevalidatesAfterRemoteRead(t *testing.T) {
	for _, mode := range []string{"transfer", "closed", "body", "session", "wrong_world"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			s.sessionID = f.party.SessionId
			f.world.VisitRevision = 1
			entry := make([]byte, 20)
			binary.LittleEndian.PutUint32(entry, f.world.Self.MapId)
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLoginVerifyWorld, Payload: entry})
			h := &encounterReadHook{fakeSession: f, module: "buddy-party"}
			if mode == "wrong_world" {
				h.module = "buddy-world"
			}
			h.hook = func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				switch mode {
				case "transfer":
					s.observeEncounterVisitLocked(opcode.SMSGTransferPending, entry[:4])
				case "closed":
					s.closed = true
				case "body":
					s.selfGUID++
				case "session":
					s.sessionID = "different-session"
				case "wrong_world":
					f.world.Self.Guid++
				}
			}
			s.session = h
			out, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
			if h.finished != nil {
				<-h.finished
			}
			if err != nil || out.Reason == "" || out.MapId != nil || len(out.Players) != 0 || len(out.Spells) != 0 || out.GetHostOwned() {
				t.Fatalf("changed custody exposed encounter facts: %v err=%v", out, err)
			}
		})
	}
}

func TestEncounterObservationRemoteReadAllowsCurrentCastReceipt(t *testing.T) {
	s, f, _ := genericCastRig(t)
	f.party.SessionId = s.sessionID
	s.applySpellbookLocked(map[uint32]bool{3018: true})
	if _, err := s.CastSpell(context.Background(), genericRequest(s, 3018)); err != nil {
		t.Fatal(err)
	}
	written := len(f.packets)
	revision := s.supportOp.view.OperationRevision
	h := &encounterReadHook{fakeSession: f, module: "buddy-world"}
	h.hook = func() {
		s.OnPacket(context.Background(), healingRejectionPacket(opcode.SMSGCastFailed, s.selfGUID, s.castCount, 3018))
	}
	s.session = h
	out, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if h.finished != nil {
		<-h.finished
	}
	if err != nil || !h.completed || out.Reason != "" || s.state.CastPending || s.state.LastCastFailure != 47 ||
		s.supportOp.view.OperationRevision != revision || s.supportOp.view.Phase != "server_rejected_no_replay" || len(f.packets) != written {
		t.Fatalf("read blocked or changed current rejection custody: observation=%v state=%v err=%v", out, &s.state, err)
	}
}

func TestEncounterObservationLargeLearnedCatalogRemainsComplete(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = f.party.SessionId
	f.world.VisitRevision = 1
	s.encounterVisit = encounterVisit{bound: true, revision: 1, mapID: f.world.Self.MapId}
	// Synthetic mechanics, not an actor dump: exercise a full learned catalog
	// and a large untruncated protobuf/JSON view through the ordinary RPC path.
	var rows strings.Builder
	known := map[uint32]bool{}
	for id := uint32(500000); id < 500381; id++ {
		fmt.Fprintf(&rows, "{\"id\":%d,\"Name\":%q,\"MaxHostile\":30}\n", id, strings.Repeat("synthetic mechanic ", 24))
		known[id] = true
	}
	catalog, err := spellcatalog.New([]byte(rows.String()))
	if err != nil {
		t.Fatal(err)
	}
	s.spellData = catalog
	s.applySpellbookLocked(known)
	before, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	h := &encounterReadHook{fakeSession: f, module: "buddy-world", hook: func() { s.mu.Lock(); s.mu.Unlock() }}
	s.session = h
	after, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if h.finished != nil {
		<-h.finished
	}
	if err != nil || !h.completed || len(after.Spells) != 381 || !proto.Equal(before, after) {
		t.Fatalf("large factual projection changed or lost rows: %d err=%v", len(after.Spells), err)
	}
	encoded, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("complete synthetic learnedSpells=%d observationBytes=%d", len(after.Spells), len(encoded))
	if len(encoded) < 300000 {
		t.Fatal("fixture no longer represents a large encounter observation", len(encoded))
	}
}

func TestEncounterObservationCannotBorrowWorldForNewOperation(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown_transport_%t", unknown), func(t *testing.T) {
			s, f, _ := genericCastRig(t)
			f.party.SessionId = s.sessionID
			s.applySpellbookLocked(map[uint32]bool{3018: true})
			f.failSend = unknown
			h := &encounterReadHook{fakeSession: f, module: "buddy-party"}
			var revision uint64
			h.hook = func() {
				s.CastSpell(context.Background(), genericRequest(s, 3018))
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.supportOp != nil {
					revision = s.supportOp.view.OperationRevision
				}
			}
			s.session = h
			out, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
			if h.finished != nil {
				<-h.finished
			}
			if err != nil || !h.completed || out.Reason != "world_observation_superseded" || out.MapId != nil || len(out.Players) != 0 || len(out.Spells) != 0 ||
				s.supportOp == nil || revision == 0 || s.supportOp.view.OperationRevision != revision || s.supportOp.view.EffectObserved || len(f.packets) != 1 {
				t.Fatalf("pre-operation World escaped or changed newly entered cast custody: out=%v operation=%v packets=%d err=%v", out, s.supportOp, len(f.packets), err)
			}
			if unknown && s.supportOp.view.Phase != "transport_unconfirmed_no_retry" {
				t.Fatal("unknown transport changed", s.supportOp.view.Phase)
			}
		})
	}
}
