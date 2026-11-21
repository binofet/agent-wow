package modrt

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"

	buddyactions "github.com/agent-wow/agent-wow/modules/buddy-actions"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const friendlyEncounterSession = "friendly-encounter"
const friendlyEncounterSelf = uint64(22)
const friendlyEncounterBuff = uint32(1243)

type friendlyEncounterCallback struct {
	manager *Manager
	world   *worldv1.SnapshotResponse
	writes  []*modv1.SendPacketRequest
	visits  []uint64
}

func (f *friendlyEncounterCallback) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		ctx = metadata.NewIncomingContext(ctx, md)
	}
	err := f.manager.modulePacketWriter(friendlyEncounterSession, actionsModule)(ctx, r.Opcode, r.Payload)
	return &emptypb.Empty{}, err
}
func (f *friendlyEncounterCallback) GetClock(context.Context, *emptypb.Empty, ...grpc.CallOption) (*modv1.ClockResponse, error) {
	return &modv1.ClockResponse{}, nil
}
func (f *friendlyEncounterCallback) InvokeModule(_ context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	var result proto.Message
	switch r.Module + "." + r.Method {
	case "buddy-world.snapshot":
		result = proto.Clone(f.world)
	case "buddy-party.state":
		result = &partyv1.StateResponse{SessionId: friendlyEncounterSession, Group: &partyv1.Group{Known: true}}
	default:
		return nil, fmt.Errorf("unexpected friendly-cast dependency %s.%s", r.Module, r.Method)
	}
	body, err := anypb.New(result)
	return &modv1.InvokeModuleResponse{Result: body}, err
}
func friendlyEncounterManager(t *testing.T) (*Manager, *buddyactions.Service, *friendlyEncounterCallback) {
	t.Helper()
	m := New(packagedPolicySpellRegistry(t), nil, nil)
	m.started, m.dir = true, "/private/"+friendlyEncounterSession
	m.session.Identity = modsession.Identity{CharacterGUID: friendlyEncounterSelf}
	for _, inst := range m.instances {
		inst.healthy = true
	}
	cb := &friendlyEncounterCallback{manager: m, world: &worldv1.SnapshotResponse{VisitRevision: 1, Self: &worldv1.PlayerState{
		Guid: friendlyEncounterSelf, Present: true, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100,
		MapKnown: true, PositionKnown: true, PositionRevision: 1, MovementFlagsKnown: true, UnitFlagsKnown: true,
		ClassKnown: true, ClassId: 5, RaceKnown: true, RaceId: 1, LevelKnown: true, Level: 30,
		PowerKnown: true, PowerType: 0, Power: 1000, MaxPower: 1000, BaseManaKnown: true, BaseMana: 100,
		ShapeshiftFormKnown: true, AuraStateKnown: true, ChannelSpellKnown: true, ChannelObjectKnown: true}}}
	m.session.SendPacket = func(_ context.Context, code uint32, p []byte) error {
		cb.writes = append(cb.writes, &modv1.SendPacketRequest{Opcode: code, Payload: bytes.Clone(p)})
		return nil
	}
	native := buddyactions.NewService(cb, friendlyEncounterSelf, friendlyEncounterSession)
	key := strings.Repeat("a", 64)
	if err := native.UseHostEncounterWriter(key); err != nil {
		t.Fatal(err)
	}
	book := binary.LittleEndian.AppendUint32([]byte{0, 1, 0}, friendlyEncounterBuff)
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: append(book, 0, 0, 0, 0)})
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLoginVerifyWorld, Payload: make([]byte, 20)})
	// Observe the actual incoming metadata without substituting a fake native
	// implementation: the registered production Service decides admission.
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == actionsv1.BuddyActions_CastBuff_FullMethodName {
			cb.visits = append(cb.visits, modcb.IncomingPolicyEncounterVisit(ctx))
		}
		return handler(ctx, req)
	}))
	actionsv1.RegisterBuddyActionsServer(server, native)
	modcb.RegisterEncounterLeaseServer(server, native)
	h := health.NewServer()
	h.SetServingStatus(modcb.PolicySupportSpellService, grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, h)
	listener := bufconn.Listen(1024 * 1024)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///friendly-native", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	inst := m.instances[actionsModule]
	inst.conn = conn
	inst.launch.Environment = map[string]string{modcb.PolicyEncounterKeyEnv: key}
	t.Cleanup(func() { m.cancel(); conn.Close(); server.Stop(); listener.Close() })
	return m, native, cb
}
func friendlyEncounterPolicy(claim string) string {
	return fmt.Sprintf(`local obs
return {on_event=function(e)if e.kind=="encounter_observation" then obs=e.value end return true end,
 decide=function(frame)return {encounterOwnership={visitId=%s,observationSequence=frame.input.observationSequence},
 action={capability="buff",sessionId=frame.session_id,selfGuid="22",targetGuid="22",spellId=1243,visitId=obs.visitId}}end,
 export_state=function()return {version=1,data={obs=obs}}end,import_state=function(c)obs=c.data.obs;return true end}`, claim)
}
func observeFriendlyEncounter(t *testing.T, m *Manager, p *SessionPolicy) (context.Context, string) {
	t.Helper()
	view, err := m.readPolicyObservation(context.Background(), friendlyEncounterSession, encounterObservationMethod)
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := p.Enqueue(behavior.Event{Kind: "encounter_observation", Value: view})
	if err != nil {
		t.Fatal(err)
	}
	seq := strconv.FormatUint(sequence, 10)
	return context.WithValue(context.Background(), policyEncounterVisitKey{}, policyEncounterObservation{visit: 1, sequence: seq}), seq
}
func TestPolicyFriendlyEncounterBuffUsesRealOwnedNativeVisit(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	policy, err := m.StartPolicy(context.Background(), friendlyEncounterPolicy("obs.visitId"))
	if err != nil {
		t.Fatal(err)
	}
	defer policy.close()
	ctx, seq := observeFriendlyEncounter(t, m, policy)
	out, receipt, err := policy.Step(ctx, map[string]any{"observationSequence": seq}, nil)
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionTransportAccepted || len(cb.writes) != 1 {
		t.Fatal("owned friendly buff was silently rejected", out, receipt, err, cb.writes)
	}
	if len(cb.visits) != 1 || cb.visits[0] != 1 {
		t.Fatal("friendly RPC lost admitted visit", cb.visits)
	}
	want := binary.LittleEndian.AppendUint32([]byte{1}, friendlyEncounterBuff)
	want = append(want, 0)
	want = binary.LittleEndian.AppendUint32(want, 2)
	want = append(want, spellFixturePackedGUID(friendlyEncounterSelf)...)
	if cb.writes[0].Opcode != opcode.CMSGCastSpell || !bytes.Equal(cb.writes[0].Payload, want) {
		t.Fatal("different native spell/target packet", cb.writes)
	}
	observed, err := native.EncounterObservation(context.Background(), &emptypb.Empty{})
	if err != nil || !observed.GetHostOwned() || observed.GetVisitId() != 1 {
		t.Fatal("test did not exercise actual owned encounter", observed, err)
	}
	if receipt.Payload["effect_observed"] != false {
		t.Fatal("cast transport claimed aura success", receipt)
	}
}
func TestPolicyFriendlyEncounterRejectsStaleOrForeignAuthority(t *testing.T) {
	for _, mode := range []string{"foreign_claim", "foreign_key", "stale_world_visit"} {
		t.Run(mode, func(t *testing.T) {
			m, native, cb := friendlyEncounterManager(t)
			claim := "obs.visitId"
			if mode == "foreign_claim" {
				claim = `"2"`
			}
			policy, err := m.StartPolicy(context.Background(), friendlyEncounterPolicy(claim))
			if err != nil {
				t.Fatal(err)
			}
			defer policy.close()
			ctx, seq := observeFriendlyEncounter(t, m, policy)
			if mode == "foreign_key" {
				m.instances[actionsModule].launch.Environment[modcb.PolicyEncounterKeyEnv] = strings.Repeat("b", 64)
			}
			if mode == "stale_world_visit" {
				cb.world.VisitRevision = 2
			}
			_, receipt, err := policy.Step(ctx, map[string]any{"observationSequence": seq}, nil)
			if err == nil && (receipt == nil || receipt.Outcome != behavior.ActionNotDispatched) {
				t.Fatal("invalid authority admitted friendly cast", receipt, err)
			}
			if len(cb.writes) != 0 {
				t.Fatal("stale/foreign visit wrote packet", cb.writes)
			}
			observed, _ := native.EncounterObservation(context.Background(), &emptypb.Empty{})
			if observed.GetHostOwned() {
				t.Fatal("invalid claim/key/world granted ownership", observed)
			}
		})
	}
}
