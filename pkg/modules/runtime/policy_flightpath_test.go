package modrt

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestFlightmasterTaggedTravelRequiresCurrentContractAndOldTravelStillWorks(t *testing.T) {
	travel := &actionsv1.TravelToRequest{}
	old := protodesc.ToDescriptorProto(travel.ProtoReflect().Descriptor())
	old.Field = old.Field[:6]
	source := &descriptorpb.FileDescriptorProto{Name: proto.String("previous_travel.proto"), Package: proto.String("warcraftbuddy.actions.v1"), Syntax: proto.String("proto3"), MessageType: []*descriptorpb.DescriptorProto{old}}
	file, err := protodesc.NewFile(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := file.Messages().Get(0)
	if !observedRequestMatches(previous, travel) {
		t.Fatal("ordinary previous TravelTo contract rejected")
	}
	travel.Flightmaster = &actionsv1.FlightmasterTarget{Guid: 123}
	if observedRequestMatches(previous, travel) {
		t.Fatal("guarded leg downgraded to ordinary TravelTo")
	}
	travel.Flightmaster = nil
	old.Field[4].Type = descriptorpb.FieldDescriptorProto_TYPE_UINT32.Enum()
	file, err = protodesc.NewFile(source, nil)
	if err != nil || observedRequestMatches(file.Messages().Get(0), travel) {
		t.Fatal("different legacy coordinate type was admitted", err)
	}
}

func TestFreePolicyFlightmasterWriterConsumesOnlyTheSelectedPacket(t *testing.T) {
	const npc = uint64(0xf130000625000045)
	for _, mode := range []string{"status", "discover", "known_unwritten", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			const sid = "flight-custody"
			token := strings.Repeat("a", 64)
			want := uint32(opcode.CMSGTaxinodeStatusQuery)
			qmode := "status"
			if mode == "discover" {
				qmode = "discover"
				want = opcode.CMSGTaxiqueryavailablenodes
			}
			q := &actionsv1.FlightpathQueryRequest{SessionId: sid, SelfGuid: 99, VisitId: 3, Mode: qmode, NpcGuid: npc}
			origin := modsession.WriteOrigin{Writer: "lua", ActionID: "102", Module: actionsModule, SessionID: sid}
			op := &policySpell{flightpath: q, admitted: true, origin: origin}
			m := &Manager{policySpells: policySpellRegistry{active: map[string]*policySpell{token: op}}}
			calls := 0
			m.session.SendPacket = func(ctx context.Context, code uint32, data []byte) error {
				calls++
				if modsession.WriteOriginFromContext(ctx) != origin || code != want || binary.LittleEndian.Uint64(data) != npc {
					t.Fatal("wrong packet origin or shape", code)
				}
				switch mode {
				case "known_unwritten":
					return modcb.PacketNotDispatchedError("synthetic stopped before socket")
				case "unknown":
					return errors.New("socket completion unavailable")
				}
				return nil
			}
			md, _ := metadata.FromOutgoingContext(modcb.WithPolicySpell(context.Background(), token))
			ctx := metadata.NewIncomingContext(context.Background(), md)
			body := binary.LittleEndian.AppendUint64(nil, npc)
			write := m.modulePacketWriter(sid, actionsModule)
			for _, err := range []error{write(ctx, opcode.CMSGActivatetaxi, body), write(ctx, want, binary.LittleEndian.AppendUint64(nil, npc+1)), write(ctx, want, body[:7]), m.modulePacketWriter("other", actionsModule)(ctx, want, body)} {
				if !modcb.PacketWasNotDispatched(err) {
					t.Fatal("unselected packet reached socket", err)
				}
			}
			err := write(ctx, want, body)
			if (err == nil) != (mode == "status" || mode == "discover") || op.written != (err == nil) || op.attempted != (mode != "known_unwritten") {
				t.Fatal(mode, err, op.attempted, op.written)
			}
			if err := write(ctx, want, body); !modcb.PacketWasNotDispatched(err) || calls != 1 {
				t.Fatal("consumed free query replayed", err, calls)
			}
		})
	}
}
