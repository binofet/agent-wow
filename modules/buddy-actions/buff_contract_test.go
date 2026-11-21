package buddyactions

import (
	"context"
	"net"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestCastBuffPublishedContractRoutesToCompiledPrimitive(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	actions, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("missing actions manifest")
	}
	method, ok := actions.RPC("cast_buff")
	if !ok || actions.Manifest().ScriptAPI["cast_buff"].Effect != moddisc.EffectMutation || method.Path() != "/warcraftbuddy.actions.v1.BuddyActions/CastBuff" {
		t.Fatal("buff RPC/manifest disagree")
	}
	s, f, _ := buffRig(t, 5, 1243)
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, s)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///buff", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	input, output := dynamicpb.NewMessage(method.Descriptor().Input()), dynamicpb.NewMessage(method.Descriptor().Output())
	raw, err := protojson.Marshal(&actionsv1.CastBuffRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: ownerGUID, SpellId: 1244})
	if err != nil {
		t.Fatal(err)
	}
	if err := protojson.Unmarshal(raw, input); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = conn.Invoke(ctx, method.Path(), input, output)
	if status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("matched descriptor did not reach normal unlearned-rank primitive precondition", err)
	}
}

func TestBuffObservationExposesCatalogFactsWithoutNativeChoices(t *testing.T) {
	s, f, _ := buffRig(t, 9, 5697, 687)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(ownerGUID, 9, 2, 7302, 600000)})
	out := &actionsv1.EncounterObservationResponse{Spells: map[string]*actionsv1.EncounterSpell{}}
	s.encounterSpells(f.world, nil, out)
	view := out.Spells["5697"]
	rank, _ := spellcatalog.Default().Lookup(5697)
	if view == nil || view.Capability != "buff" || !view.Ready || !view.Available || view.TargetRule != "ally" || view.FamilySet != rank.FamilySet || len(view.AuraEffects) == 0 {
		t.Fatal("Lua lacks learned buff mechanics", view)
	}
	support := s.encounterSupportLocked(context.Background(), f.world, nil)
	if support.AuraSpells[7302] == nil || !support.AuraSpells[7302].MetadataKnown || support.AuraSpells[7302].Known || len(support.PlayerAuras) != 1 || len(f.packets) != 0 {
		t.Fatal("observed stronger aura metadata missing or invented learned/cast", support)
	}
	// Busy casters still expose aura metadata; only exact spell readiness changes.
	s.state.CastPending = true
	support = s.encounterSupportLocked(context.Background(), f.world, nil)
	s.encounterSpells(f.world, nil, out)
	if support.AuraSpells[7302] == nil || out.Spells["5697"].Ready {
		t.Fatal("busy state hid facts or claimed ready", support)
	}
}
