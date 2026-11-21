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
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestCleanseObservationReusesFactualToolkitAuraContract(t *testing.T) {
	s, f, _ := cleanseRig(t, 5, 528)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: cleanseAuraPacket(ownerGUID, 3150, true)})
	w, err := s.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	view := s.encounterSupportLocked(context.Background(), w, nil)
	toolkit, err := s.SpellToolkit(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.PlayerAuras) != 1 || len(view.PlayerAuras[0].Auras) != 1 || len(toolkit.PlayerAuras) != 1 || !proto.Equal(view.PlayerAuras[0], toolkit.PlayerAuras[0]) {
		t.Fatal("toolkit and attached policy disagree on observed auras", view, toolkit)
	}
	aura := view.PlayerAuras[0].Auras[0]
	metadata, ok := s.spellData.Lookup(aura.SpellId)
	if !ok || !aura.Negative || aura.Positive || !aura.MetadataKnown || aura.DispelType != uint32(metadata.DispelType) || aura.Revision == 0 {
		t.Fatal("harmful/type/revision facts were not reflected", aura)
	}
	out := &actionsv1.EncounterObservationResponse{Spells: map[string]*actionsv1.EncounterSpell{}}
	s.encounterSpells(w, nil, out)
	spell := out.Spells["528"]
	if spell == nil || spell.Capability != "cleanse" || spell.FriendlyDispelMask != 8 || !spell.Available || !spell.Ready || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("cleanse projection chose a cast or hid a learned mechanical capability", out.Spells)
	}
	// Busy state affects mechanical readiness, not the observed aura facts.
	s.state.CastPending = true
	if busy := s.encounterSupportLocked(context.Background(), w, nil); len(busy.PlayerAuras) != 1 || !proto.Equal(busy.PlayerAuras[0], view.PlayerAuras[0]) {
		t.Fatal("busy caster erased factual harmful aura observation", busy)
	}
}

func TestCastCleansePublishedContractRoutesToCompiledPrimitive(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	actions, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("missing actions manifest")
	}
	method, ok := actions.RPC("cast_cleanse")
	if !ok || actions.Manifest().ScriptAPI["cast_cleanse"].Effect != moddisc.EffectMutation || method.Path() != "/warcraftbuddy.actions.v1.BuddyActions/CastCleanse" {
		t.Fatal("cleanse RPC/manifest disagree")
	}
	s, f, _ := cleanseRig(t, 5, 528)
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, s)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///cleanse", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	input, output := dynamicpb.NewMessage(method.Descriptor().Input()), dynamicpb.NewMessage(method.Descriptor().Output())
	raw, err := protojson.Marshal(&actionsv1.CastCleanseRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: ownerGUID, SpellId: 528, AuraSlot: 0, AuraRevision: 1, AuraSpellId: 3150})
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
		t.Fatal("matched descriptor did not reach normal no-aura primitive precondition", err)
	}
}
