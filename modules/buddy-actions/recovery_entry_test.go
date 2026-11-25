package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/areatriggerreference"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
	"strings"
	"testing"
	"time"
)

func corpseEntranceRig(t *testing.T) (*Service, *fakeSession, areatriggerreference.Trigger) {
	t.Helper()
	s, f, _ := portalEntryRig(t)
	var entrance areatriggerreference.Trigger
	for _, v := range areatriggerreference.All() {
		if v.SourceMap != v.DestinationMap && v.Radius > 1 {
			entrance = v
			break
		}
	}
	if entrance.ID == 0 {
		t.Fatal("public entrance fixture missing")
	}
	ghostAt(f, float32(entrance.X))
	f.world.Self.MapId = entrance.SourceMap
	f.world.Self.Y = float32(entrance.Y)
	f.world.Self.Z = float32(entrance.Z)
	s.encounterVisit.mapID = entrance.SourceMap
	seedSyntheticPortalWorldScope(f, 9)
	s.recovery.lifeKnown = true
	s.recovery.lastObservedAlive = false
	s.recovery.graveyardPoseObserved = true
	s.recovery.state = actionsv1.RecoveryState{DeathEpoch: 1, GhostObserved: true, GhostMovementSupported: true, CorpseQueryKnown: true, CorpseFound: true, CorpseQueryRevision: 1, CorpseMapId: entrance.DestinationMap,
		CorpseLocation: &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: entrance.SourceMap, X: float32(entrance.X), Y: float32(entrance.Y), Z: float32(entrance.Z)}}
	return s, f, entrance
}
func corpseEntranceRequest(s *Service, f *fakeSession, v areatriggerreference.Trigger) *actionsv1.CorpseApproachRequest {
	return &actionsv1.CorpseApproachRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, DeathEpoch: 1, ExpectedPositionRevision: f.world.Self.PositionRevision, CorpseQueryRevision: 1, RangeYards: .25, TriggerId: v.ID, EnterInstance: true}
}
func TestRecoveryDungeonEntranceScopedNormalPacketNoReplayAndLifeEvidence(t *testing.T) {
	s, f, v := corpseEntranceRig(t)
	q := corpseEntranceRequest(s, f, v)
	got, err := s.ApproachCorpse(context.Background(), q)
	if err != nil || !got.Recovery.CorpseEntryWritten || countOpcode(f, opcode.CMSGAreatrigger) != 1 || countOpcode(f, opcode.CMSGReclaimCorpse) != 0 {
		t.Fatal(got, err, f.packets)
	}
	// A repeated exact request cannot repeat the irreversible trigger, even with a fresh pose.
	q.ExpectedPositionRevision = f.world.Self.PositionRevision
	if _, err = s.ApproachCorpse(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if countOpcode(f, opcode.CMSGAreatrigger) != 1 || got.Recovery.AliveObserved {
		t.Fatal("write became life or was replayed")
	}
	f.world.Self.Alive = true
	f.world.Self.Health = 50
	f.world.Self.PlayerFlags = 0
	state, err := s.Status(context.Background(), &emptypb.Empty{})
	if err != nil || !state.Recovery.AliveObserved || countOpcode(f, opcode.CMSGReclaimCorpse) != 0 {
		t.Fatal(state, err)
	}
}
func TestRecoveryDungeonEntranceRejectsStaleForeignAndLivingNoPackets(t *testing.T) {
	for _, mode := range []string{"stale_query", "stale_death", "stale_pose", "foreign_body", "wrong_map", "outside", "living", "unknown_motion", "other_writer", "pending_trigger", "unknown_trigger", "accepted_offer", "reclaim_pending"} {
		t.Run(mode, func(t *testing.T) {
			s, f, v := corpseEntranceRig(t)
			q := corpseEntranceRequest(s, f, v)
			switch mode {
			case "stale_query":
				q.CorpseQueryRevision++
			case "stale_death":
				q.DeathEpoch++
			case "stale_pose":
				q.ExpectedPositionRevision++
			case "foreign_body":
				q.SelfGuid++
			case "wrong_map":
				s.recovery.state.CorpseMapId = v.SourceMap
			case "outside":
				f.world.Self.X += 100
			case "living":
				f.world.Self.Alive = true
				f.world.Self.Health = 50
				f.world.Self.PlayerFlags = 0
			case "unknown_motion":
				s.state.MotionUnconfirmed = true
			case "other_writer":
				s.state.InteractionOwner = "trade"
			case "pending_trigger":
				s.areaTrigger.operation = &actionsv1.AreaTriggerOperation{Pending: true}
			case "unknown_trigger":
				s.areaTrigger.operation = &actionsv1.AreaTriggerOperation{TransportUnknown: true}
			case "accepted_offer":
				s.resurrectionOffer = &resurrectionOffer{consumed: true}
			case "reclaim_pending":
				s.recovery.state.ReclaimAttempted = true
			}
			_, err := s.ApproachCorpse(context.Background(), q)
			if err == nil || len(f.packets) != 0 {
				t.Fatal(mode, err, f.packets)
			}
		})
	}
}
func TestRecoveryExteriorMarkerIsNotReclaimLocation(t *testing.T) {
	s, f, _ := corpseEntranceRig(t)
	state, err := s.Status(context.Background(), &emptypb.Empty{})
	if err != nil || len(state.Recovery.CorpseEntrances) == 0 {
		t.Fatal(state, err)
	}
	_, err = s.ReclaimCorpse(context.Background(), &actionsv1.ReclaimCorpseRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, DeathEpoch: 1, ExpectedPositionRevision: f.world.Self.PositionRevision, CorpseGuid: ownCorpseGUID, ExpectedCorpseRevision: 1})
	if err == nil || len(f.packets) != 0 {
		t.Fatal("exterior marker authorized reclaim", err, f.packets)
	}
}

func TestRecoveryDungeonSharedLuaChoosesEntranceAndNeverReclaimsAfterEntryResurrection(t *testing.T) {
	s, f, _ := corpseEntranceRig(t)
	tickDeathLua(t, s)
	if countOpcode(f, opcode.CMSGAreatrigger) != 1 || countOpcode(f, opcode.CMSGReclaimCorpse) != 0 {
		t.Fatal("actual Lua did not select normal dungeon entry", s.stateLocked(), f.packets)
	}
	tickDeathLua(t, s)
	if countOpcode(f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("Lua repeated entry")
	}
	f.world.Self.Alive = true
	f.world.Self.Health = 50
	f.world.Self.PlayerFlags = 0
	tickDeathLua(t, s)
	if countOpcode(f, opcode.CMSGReclaimCorpse) != 0 {
		t.Fatal("early actual resurrection still caused reclaim")
	}
}

func TestRecoveryDungeonEntranceUnknownTransportNeverReplays(t *testing.T) {
	s, f, v := corpseEntranceRig(t)
	f.failSend = true
	f.failSendOpcode = opcode.CMSGAreatrigger
	q := corpseEntranceRequest(s, f, v)
	got, err := s.ApproachCorpse(context.Background(), q)
	if err == nil || !got.Recovery.CorpseEntryAttempted || !got.Recovery.CorpseEntryUnknown || got.Recovery.CorpseEntryWritten {
		t.Fatal(got, err)
	}
	f.failSend = false
	q.ExpectedPositionRevision = f.world.Self.PositionRevision
	s.ApproachCorpse(context.Background(), q)
	if countOpcode(f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("unknown entrance replayed", f.packets)
	}
}

func TestRecoveryDungeonApproachUsesCurrentMapVerifiedRouteToSelectedEntrance(t *testing.T) {
	s, f, v := corpseEntranceRig(t)
	f.world.Self.X -= 20
	f.world.Self.PositionRevision++
	s.navDataDir = "synthetic-verified-ghost-route"
	planned := false
	s.planRecoveryPath = func(ctx context.Context, root string, mapID uint32, from, to position, arrival float64) ([]position, error) {
		planned = true
		if root != s.navDataDir || mapID != v.SourceMap || to != (position{x: float32(v.X), y: float32(v.Y), z: float32(v.Z)}) || arrival != .25 {
			t.Fatal("route used corpse-map coordinates", mapID, to)
		}
		return []position{from, to}, nil
	}
	q := corpseEntranceRequest(s, f, v)
	q.EnterInstance = false
	_, err := s.ApproachCorpse(context.Background(), q)
	if err != nil || !planned || countOpcode(f, opcode.MSGMoveStartForward) != 1 || countOpcode(f, opcode.CMSGAreatrigger) != 0 || countOpcode(f, opcode.CMSGReclaimCorpse) != 0 {
		t.Fatal(planned, err, f.packets)
	}
}

func TestRecoveryDungeonSharedLuaWalksEntranceBeforeOneTrigger(t *testing.T) {
	s, f, _ := corpseEntranceRig(t)
	now := s.now()
	s.now = func() time.Time { return now }
	f.world.Self.X -= 20
	f.world.Self.PositionRevision++
	s.navDataDir = "synthetic-verified-ghost-route"
	s.planRecoveryPath = func(ctx context.Context, root string, mapID uint32, from, to position, arrival float64) ([]position, error) {
		return []position{from, to}, nil
	}
	for i := 0; i < 100 && countOpcode(f, opcode.CMSGAreatrigger) == 0; i++ {
		tickDeathLua(t, s)
		now = now.Add(250 * time.Millisecond)
	}
	if countOpcode(f, opcode.MSGMoveHeartbeat) == 0 || countOpcode(f, opcode.CMSGAreatrigger) != 1 || countOpcode(f, opcode.CMSGReclaimCorpse) != 0 {
		t.Fatal("shared Lua failed normal ghost walk/entry", s.pos, s.stateLocked(), f.packets)
	}
}

func TestRecoveryDungeonGhostInteriorFreshVisitRequeriesThenReclaimsWithoutEntryReplay(t *testing.T) {
	s, f, v := corpseEntranceRig(t)
	tickDeathLua(t, s)
	if s.recovery.entryOperation == nil {
		t.Fatal("missing entry scope")
	}
	setAreaTriggerDestinationFacts(t, s, f, s.recovery.entryOperation)
	tickDeathLua(t, s)
	if !s.recovery.state.CorpseEntryDestinationObserved || countOpcode(f, opcode.MSGCorpseQuery) != 1 || countOpcode(f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("interior ghost did not query actual corpse", s.stateLocked(), f.packets)
	}
	f.world.OwnCorpse = &worldv1.CorpseState{Guid: ownCorpseGUID, OwnerGuid: s.selfGUID, Present: true, MapKnown: true, MapId: v.DestinationMap, PositionKnown: true, X: 10, Y: 20, Z: 30, PositionRevision: 1, FlagsKnown: true}
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(v.DestinationMap, v.DestinationMap, position{x: 10, y: 20, z: 30}))
	s.recovery.state.ReclaimDelayKnown = true
	for i := 0; i < 4 && countOpcode(f, opcode.CMSGReclaimCorpse) == 0; i++ {
		tickDeathLua(t, s)
	}
	if countOpcode(f, opcode.CMSGReclaimCorpse) != 1 || countOpcode(f, opcode.CMSGAreatrigger) != 1 || countOpcode(f, opcode.MSGCorpseQuery) != 1 {
		t.Fatal("interior continuation/replay failed", s.stateLocked(), f.packets)
	}
}
func TestRecoveryDungeonMapOnlyDoesNotReopenQueryOrEntry(t *testing.T) {
	s, f, v := corpseEntranceRig(t)
	tickDeathLua(t, s)
	portalFixtureArrive(t, s, f, v.DestinationMap) // No scoped transfer/ACK evidence.
	tickDeathLua(t, s)
	if s.recovery.state.CorpseEntryDestinationObserved || countOpcode(f, opcode.MSGCorpseQuery) != 0 || countOpcode(f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("map-only state promoted to entry", s.stateLocked(), f.packets)
	}
}
func TestRecoveryInvalidEntranceLeavesPhysicalOwnershipUnchanged(t *testing.T) {
	for _, mode := range []string{"unknown_trigger", "no_trigger"} {
		t.Run(mode, func(t *testing.T) {
			s, f, v := corpseEntranceRig(t)
			q := corpseEntranceRequest(s, f, v)
			if mode == "unknown_trigger" {
				q.TriggerId = ^uint32(0)
			} else {
				q.TriggerId = 0
				q.EnterInstance = false
			}
			_, err := s.ApproachCorpse(context.Background(), q)
			if err == nil || s.recovery.token != "" || s.interactionToken != "" || s.state.InteractionOwner != "" || s.recovery.state.Active || len(f.packets) != 0 {
				t.Fatal("invalid request acquired physical ownership", err, s.stateLocked(), f.packets)
			}
		})
	}
}

type corpsePolicyTokenSession struct {
	*fakeSession
	token   string
	checked int
}

func (f *corpsePolicyTokenSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if r.Opcode == opcode.MSGMoveStop || r.Opcode == opcode.CMSGAreatrigger {
		md, _ := metadata.FromOutgoingContext(ctx)
		token, ok := modcb.IncomingPolicySpell(metadata.NewIncomingContext(context.Background(), md))
		if !ok || token != f.token {
			return nil, modcb.PacketNotDispatchedError("native scoped write lost exact incoming policy token")
		}
		f.checked++
	}
	return f.fakeSession.SendPacket(ctx, r, opts...)
}
func TestRecoveryDungeonNativeForwardsPolicyTokenOnScopedStopAndTrigger(t *testing.T) {
	s, f, v := corpseEntranceRig(t)
	guarded := &corpsePolicyTokenSession{fakeSession: f, token: strings.Repeat("a", 64)}
	s.session = guarded
	outgoing := modcb.WithPolicySpell(context.Background(), guarded.token)
	md, _ := metadata.FromOutgoingContext(outgoing)
	incoming := metadata.NewIncomingContext(context.Background(), md)
	got, err := s.ApproachCorpse(incoming, corpseEntranceRequest(s, f, v))
	if err != nil || guarded.checked != 2 || countOpcode(f, opcode.CMSGAreatrigger) != 1 || !got.Recovery.CorpseEntryWritten {
		t.Fatal("native incoming RPC did not preserve scoped policy custody", guarded.checked, got, err)
	}
}
