package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	"github.com/agent-wow/agent-wow/pkg/areatriggerreference"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func boxPortalEntryFixture(t *testing.T, inside bool) (*Service, *fakeSession, func(time.Duration), *followPolicySession, dungeonPortal) {
	t.Helper()
	s, f, advance := portalEntryRig(t)
	var portal dungeonPortal
	for _, reference := range areatriggerreference.All() {
		if reference.ID == 3133 {
			portal = portalReference(reference)
		}
	}
	if portal.trigger == 0 || portal.reference.Radius != 0 {
		t.Fatal("the pinned oriented-box fixture is missing")
	}
	start := portal.center
	if !inside {
		// The actual observed owner is inside the volume; the follower starts
		// outside its rotated short side. No teleport destination is used.
		offset := portal.reference.Width/2 + 5
		start.x -= float32(math.Sin(portal.reference.Orientation) * offset)
		start.y += float32(math.Cos(portal.reference.Orientation) * offset)
	}
	putAtPortal(f.world.Self, portal.source, start)
	putAtPortal(f.world.Owner, portal.source, portal.center)
	f.world.Owner.Health = f.world.Owner.MaxHealth
	s.encounterVisit.mapID = portal.source
	s.navDataDir = "/synthetic-qualified-route"
	host := newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	toggle(t, s, true)
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("fixture position was not bound")
	}
	s.lastWorld = f.world
	s.observeDungeonPortalLocked(f.world, s.now())
	if s.followPortal.portal.trigger != portal.trigger || s.followPortal.witness == 0 {
		t.Fatal("actual shared Lua did not capture the unique box witness", s.followPortal)
	}
	f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
	s.observeDungeonPortalLocked(f.world, s.now())
	advance(750 * time.Millisecond)
	return s, f, advance, host, portal
}

func TestNormalBoxPortalWalkUsesCurrentLuaAndScopedStopThenContinues(t *testing.T) {
	s, f, advance, host, portal := boxPortalEntryFixture(t, false)
	if portal.contains(portal.source, s.pos, 0) {
		t.Fatal("fixture did not start outside the rotated box")
	}
	witness, sid := s.followPortal.witness, s.sessionID
	waitSource := strings.Replace(behaviors.FollowFormationSource,
		"local portal_wait_ms, portal_seen_ms, portal_catchup_ms = 500,20000,75000",
		"local portal_wait_ms, portal_seen_ms, portal_catchup_ms = 2000,20000,75000", 1)
	waitRevision := host.reload(t, waitSource)
	s.Tick(context.Background())
	if len(f.packets) != 0 || s.followPortal.witness != witness || s.formation.sourceRevision != waitRevision {
		t.Fatal("the current Lua wait was bypassed", s.stateLocked(), f.packets)
	}
	revision := host.reload(t, behaviors.FollowFormationSource)
	for range 24 {
		advance(250 * time.Millisecond)
		s.Tick(context.Background())
		if s.followPortal.notified {
			break
		}
	}
	if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) == 0 ||
		countPackets(f, opcode.CMSGAreatrigger) != 1 || !s.followPortal.notified || s.sessionID != sid ||
		s.followPortal.witness != witness || s.formation.sourceRevision != revision || !s.state.Following {
		t.Fatal("box entry did not use the verified route and same-session edited Lua", s.pos, s.stateLocked(), f.packets)
	}
	var previous *modv1.SendPacketRequest
	for _, packet := range f.packets {
		if packet.Opcode == opcode.CMSGAreatrigger {
			if previous == nil || previous.Opcode != opcode.MSGMoveStop || packet.TransportScope == nil ||
				!bytes.Equal(packet.Payload, binary.LittleEndian.AppendUint32(nil, portal.trigger)) ||
				!portalStopReportMatches(&modv1.WorldPacket{Opcode: previous.Opcode, Payload: previous.Payload, TransportScope: packet.TransportScope}, s.selfGUID, portal.source, s.pos) {
				t.Fatal("ID-only entry was not bound to the immediately preceding actual STOP", previous, packet)
			}
		}
		previous = packet
	}
	// A destination map in one queue is insufficient. Both actual entry
	// observations must agree on the new visit and own body before cleanup.
	f.world.Self.MapId = portal.destination
	f.world.VisitRevision++
	s.Tick(context.Background())
	if !s.followPortal.notified {
		t.Fatal("World-only map change cleared crossing custody")
	}
	f.world.VisitRevision--
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = 0, 0, 0
	portalFixtureArrive(t, s, f, portal.destination)
	s.Tick(context.Background())
	if s.followPortal.portal.trigger != 0 || s.state.Reason != "follow_instance_transfer_observed" {
		t.Fatal("matching actual visit did not settle the crossing", s.followPortal, s.stateLocked())
	}
	starts := countPackets(f, opcode.MSGMoveStartForward)
	f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = true, true, true
	f.world.Owner.MapId, f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = portal.destination, 10, 0, 0
	f.world.Owner.PositionRevision++
	for range 8 {
		advance(250 * time.Millisecond)
		s.Tick(context.Background())
	}
	if countPackets(f, opcode.MSGMoveStartForward) <= starts || countPackets(f, opcode.CMSGAreatrigger) != 1 || !s.state.Following {
		t.Fatal("ordinary following did not continue after actual entry", s.stateLocked(), f.packets)
	}
}

func TestNormalPortalRefusesAmbiguousReferenceWitness(t *testing.T) {
	var reference areatriggerreference.Trigger
	for _, row := range areatriggerreference.All() {
		if row.ID == 2410 {
			reference = row
		}
	}
	point := position{x: float32(reference.X), y: float32(reference.Y), z: float32(reference.Z)}
	matches := 0
	for _, row := range areatriggerreference.All() {
		if row.Contains(reference.SourceMap, float64(point.x), float64(point.y), float64(point.z), 0) {
			matches++
		}
	}
	if matches < 2 {
		t.Fatal("the held ambiguity fixture changed", matches)
	}
	if p, ok := witnessedPortal(reference.SourceMap, point); ok || p.trigger != 0 {
		t.Fatal("native row order selected an ambiguous entrance", p)
	}
}

func TestNormalPortalStopReportCannotAuthorizeDifferentSource(t *testing.T) {
	for _, kind := range []string{"missing scope", "wrong map", "foreign body", "different pose", "trailing byte", "owner reappeared", "controller replaced"} {
		t.Run(kind, func(t *testing.T) {
			s, f, _, _, _ := boxPortalEntryFixture(t, true)
			f.onOwnMovementReport = func(packet *modv1.WorldPacket) {
				switch kind {
				case "missing scope":
					packet.TransportScope = nil
				case "wrong map":
					packet.TransportScope.MapId++
				case "foreign body":
					packet.Payload = append(packedGUID(8), packet.Payload[len(packedGUID(s.selfGUID)):]...)
				case "different pose":
					at := len(packedGUID(s.selfGUID)) + 10
					binary.LittleEndian.PutUint32(packet.Payload[at:], math.Float32bits(s.pos.x+1))
				case "trailing byte":
					packet.Payload = append(packet.Payload, 0)
				case "owner reappeared":
					f.world.Owner.Present = true
				case "controller replaced":
					s.posRevision++
				}
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGAreatrigger) != 0 || s.followPortal.notified || s.state.MotionUnconfirmed {
				t.Fatal("a report read crossed its immutable source fence", kind, s.followPortal, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestNormalPortalClockCannotRebindTheStopProposal(t *testing.T) {
	for _, kind := range []string{"pose", "body", "follow", "interaction", "channel"} {
		t.Run(kind, func(t *testing.T) {
			s, f, _, _, _ := boxPortalEntryFixture(t, true)
			f.onClock = func() {
				switch kind {
				case "pose":
					s.pos.x++
				case "body":
					s.selfGUID = 8
				case "follow":
					s.followRevision++
				case "interaction":
					s.state.InteractionOwner = "trade"
				case "channel":
					f.world.Self.ChannelSpellId = 5143
				}
			}
			s.Tick(context.Background())
			if len(f.packets) != 0 || s.followPortal.notified || s.state.MotionUnconfirmed {
				t.Fatal("a late callback freshened the STOP proposal", kind, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestNormalPortalIdleEstimateNeedsItsCurrentPriorWrittenSource(t *testing.T) {
	s, f, _, _, _ := boxPortalEntryFixture(t, true)
	s.posEstimated = true
	s.Tick(context.Background())
	if len(f.packets) != 0 || s.followPortal.notified || s.state.MotionUnconfirmed {
		t.Fatal("an unsupported idle estimate was refreshed by a new STOP clock", s.stateLocked(), f.packets)
	}
}

func TestNormalPortalUnknownSurvivesFollowHostSourceAndNewStop(t *testing.T) {
	s, f, advance, host, _ := boxPortalEntryFixture(t, true)
	f.failSend, f.failSendOpcode = true, opcode.CMSGAreatrigger
	s.Tick(context.Background())
	if !s.followPortal.notified || countPackets(f, opcode.CMSGAreatrigger) != 1 || s.state.MotionUnconfirmed ||
		s.state.LastTransportFailure == nil || !s.state.LastTransportFailure.OutcomeUnknown {
		t.Fatal("actual attempted trigger did not retain UNKNOWN separately from movement", s.stateLocked(), f.packets)
	}
	f.failSend = false
	toggle(t, s, false)
	toggle(t, s, true)
	s.hostEncounterEpoch++
	s.portalGoalRevision++
	s.portalGoal = portalCatchupGoal{}
	host.reload(t, strings.Replace(behaviors.FollowFormationSource, "= 500,20000,75000", "= 0,20000,75000", 1))
	// Even a later known normal STOP and different Core motion source cannot
	// turn the uncertain operation into permission to send the same trigger.
	if !s.movementLocked(context.Background(), opcode.MSGMoveStop, s.pos, false) {
		t.Fatal("synthetic later STOP did not write")
	}
	advance(2 * encounterLeaseDuration)
	for range 4 {
		s.Tick(context.Background())
		advance(250 * time.Millisecond)
	}
	if !s.followPortal.notified || countPackets(f, opcode.CMSGAreatrigger) != 1 || countPackets(f, opcode.MSGMoveStop) != 2 {
		t.Fatal("new intent, source, clock or STOP replayed an unresolved crossing", s.followPortal, f.packets)
	}
}

func TestNormalPortalKnownZeroWriteRefusalCanUseFreshReportAndCurrentLua(t *testing.T) {
	s, f, advance, _, _ := boxPortalEntryFixture(t, true)
	f.onOwnMovementReport = func(packet *modv1.WorldPacket) {
		// The snapshot callback returned a source superseded at Core's owner
		// loop before dispatch. The synthetic Core refuses without recording
		// a game packet; production Core has its own final-owner-loop tests.
		packet.TransportScope.MotionRevision++
		f.onOwnMovementReport = nil
	}
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGAreatrigger) != 0 || s.followPortal.notified || s.state.LastTransportFailure == nil ||
		s.state.LastTransportFailure.OutcomeUnknown || s.state.Reason != "follow_instance_trigger_not_dispatched" {
		t.Fatal("known zero-write refusal became an uncertain physical crossing", s.followPortal, s.stateLocked(), f.packets)
	}
	advance(250 * time.Millisecond)
	s.Tick(context.Background())
	if !s.followPortal.notified || countPackets(f, opcode.CMSGAreatrigger) != 1 || countPackets(f, opcode.MSGMoveStop) != 2 {
		t.Fatal("fresh qualified source could not recover the known zero-write refusal", s.followPortal, s.stateLocked(), f.packets)
	}
}
