// Package buddyworld retains bounded observations of a character and its
// optional companion owner, and provides scoped spline completion reports.
package buddyworld

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddyidentity"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const OwnerGUID = buddyidentity.CompanionOwnerGUID

type observed struct {
	guid                                                         uint64
	present, mapKnown, positionKnown, movementKnown, speedsKnown bool
	mapID, movementFlags, extraFlags, movementTime               uint32
	x, y, z, orientation, walkSpeed, runSpeed                    float32
	positionRevision                                             uint64
	positionInferred, stationaryPosition                         bool
	positionAt, vitalsAt, observedAt                             time.Time
	groundEffectFirstSeen                                        time.Time
	passenger                                                    *receivedPassenger
	transport                                                    *receivedHull
	transportFraction                                            *receivedPathFraction
	// Only fields up to PLAYER_FLAGS are needed, regardless of nearby population.
	fields                      [playerFlags + 1]uint32
	known                       [playerFlags + 1]bool
	isSelf                      bool
	publicPlayer                bool
	questLogKnown               bool
	questLog                    [25][5]uint32
	questRevision               uint64
	backpackKnown               bool
	backpack                    [16]uint64
	backpackRevision            uint64
	bagSlotsKnown               bool
	bags                        [4]uint64
	equipmentKnown              bool
	equipment                   [19]uint64
	inventoryEpoch              uint64
	isContainer, containerKnown bool
	inventoryItemKnown          bool
	attackVictimKnown           bool
	attackVictim                uint64
	attackAt                    time.Time
	trajectory                  observedTrajectory
	receivedMovement            bool
	receivedMovementInfo        movementinfo.Info
	receivedMovementWire        [movementinfo.MaxEncodedSize]byte
	receivedMovementWireLen     uint8
	receivedSpline              bool
	receivedSplineHeader        observedTrajectory
	receivedUnitFlags           bool
	receivedTaxiBarrier         bool
	receivedOwnCreate           bool
	receivedMotionInvalid       bool
	receivedPosition            bool
	cast                        observedCreatureCast
	auras                       *observedCreatureAuras
	mainhandKnown               bool
	mainhand                    uint64
	offhandKnown                bool
	offhand                     uint64
	ammoKnown                   bool
	ammo                        uint32
	copperKnown                 bool
	copper                      uint32
	xpKnown, nextLevelXPKnown   bool
	xp, nextLevelXP             uint32
	skillsKnown                 bool
	skills                      [128][3]uint32
	freePrimaryKnown            bool
	freePrimary                 uint32
}

type Service struct {
	worldv1.UnimplementedBuddyWorldServer
	mu                           sync.Mutex
	state                        observations
	now                          func() time.Time
	ignored, malformed, observed uint64
	lastOpcode                   uint32
	lastError                    string
	combat                       combatJournal
	session                      modv1.SessionClient
	motion                       ownMotionAuthority
	completion                   splineCompletionState
	worldTransfer                worldTransferObservation
	knockback                    *modv1.WorldPacket
}

func NewService(selfGUID uint64) *Service {
	return newService(selfGUID, OwnerGUID)
}

// NewIndependentService observes only the session character, with no owner.
func NewIndependentService(selfGUID uint64) *Service {
	return newService(selfGUID, 0)
}

func newService(selfGUID, ownerGUID uint64) *Service {
	return &Service{state: observations{players: [2]observed{{guid: selfGUID, isSelf: true}, {guid: ownerGUID}}, nearbyPlayers: make(map[uint64]*observed), creatures: make(map[uint64]*observed), items: make(map[uint64]*observed), gameObjects: make(map[uint64]*observed), corpses: make(map[uint64]*observed)}, now: time.Now}
}

func age(now, observed time.Time) uint64 {
	if observed.IsZero() || now.Before(observed) {
		return 0
	}
	return uint64(now.Sub(observed) / time.Millisecond)
}

func snapshot(p observed, now time.Time) *worldv1.PlayerState {
	r := &worldv1.PlayerState{
		Guid: p.guid, Present: p.present, MapKnown: p.mapKnown, MapId: p.mapID,
		PositionKnown: p.positionKnown, PositionInferred: p.positionInferred, X: p.x, Y: p.y, Z: p.z, Orientation: p.orientation,
		PositionAgeMs: age(now, p.positionAt), PositionRevision: p.positionRevision,
		MovementFlagsKnown: p.movementKnown, MovementFlags: p.movementFlags,
		PassengerMovement:  p.passengerSnapshot(now),
		ExtraMovementFlags: p.extraFlags, MovementTime: p.movementTime,
		Health: p.fields[unitHealth], MaxHealth: p.fields[unitMaxHealth],
		UnitFlagsKnown: p.known[unitFlags], UnitFlags: p.fields[unitFlags],
		PlayerFlags: p.fields[playerFlags], DynamicFlags: p.fields[unitDynamicFlags],
		TargetKnown: p.known[0x12] && p.known[0x13], TargetGuid: uint64(p.fields[0x12]) | uint64(p.fields[0x13])<<32, FactionTemplate: p.fields[0x37], UnitFlags_2: p.fields[0x3c],
		VitalsAgeMs: age(now, p.vitalsAt), ObservationAgeMs: age(now, p.observedAt),
		SpeedsKnown: p.speedsKnown, WalkSpeed: p.walkSpeed, RunSpeed: p.runSpeed,
		AuraStateKnown: p.known[0x3d], AuraState: p.fields[0x3d],
	}
	r.HealthKnown = p.known[unitHealth] && p.known[unitMaxHealth] && r.MaxHealth > 0 && r.Health <= r.MaxHealth
	r.PowerType = p.fields[unitBytes0] >> 24
	r.ClassId = (p.fields[unitBytes0] >> 8) & 0xff
	r.ClassKnown = p.known[unitBytes0] && r.ClassId > 0 && r.ClassId <= 11 && r.ClassId != 10
	r.RaceId = p.fields[unitBytes0] & 0xff
	r.RaceKnown = p.present && p.known[unitBytes0] && r.RaceId > 0 && r.RaceId <= 11 && r.RaceId != 9
	r.Level = p.fields[0x36]
	r.LevelKnown = p.known[0x36] && r.Level > 0
	r.BaseMana = p.fields[0x78]
	r.BaseManaKnown = p.known[0x78] && r.BaseMana > 0
	r.CombatReach = math.Float32frombits(p.fields[0x42])
	r.CombatReachKnown = p.known[0x42] && validFloat(r.CombatReach) && r.CombatReach >= 0 && r.CombatReach <= 50
	r.BaseAttackTimeMs = p.fields[0x3e]
	r.BaseAttackTimeKnown = p.known[0x3e] && r.BaseAttackTimeMs > 0 && r.BaseAttackTimeMs <= 60000
	if p.known[unitBytes0] && r.PowerType < 7 {
		i := int(r.PowerType)
		r.Power, r.MaxPower = p.fields[unitPower1+i], p.fields[unitMaxPower1+i]
		r.PowerKnown = p.known[unitPower1+i] && p.known[unitMaxPower1+i] && r.Power <= r.MaxPower
	}
	// Ghosts can have positive health. Public player flags must also be known.
	r.AliveKnown = r.HealthKnown && p.known[playerFlags]
	r.Alive = r.AliveKnown && r.Health > 0 && r.PlayerFlags&0x10 == 0 && r.DynamicFlags&0x20 == 0
	if !p.present && p.mapKnown && !p.positionKnown {
		// The last local XYZ belongs to the previous map. A remote party
		// zone supplies no coordinates on this player's new map.
		r.X, r.Y, r.Z, r.Orientation = 0, 0, 0, 0
	}
	if p.isSelf && p.present {
		r.ChannelObjectKnown = p.known[unitChannelObject] && p.known[unitChannelObject+1]
		r.ChannelObjectGuid = uint64(p.fields[unitChannelObject]) | uint64(p.fields[unitChannelObject+1])<<32
		r.ChannelSpellKnown, r.ChannelSpellId = p.known[unitChannelSpell], p.fields[unitChannelSpell]
		r.ShapeshiftFormKnown, r.ShapeshiftForm = p.known[unitShapeshiftBytes], p.fields[unitShapeshiftBytes]>>24
	}
	return r
}

func (s *Service) Snapshot(ctx context.Context, request *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	guid := request.GetCreatureGuid()
	if request.GetCreatureEntry() > 0xffffff || request.GetGameObjectEntry() > 0xffffff {
		return nil, status.Error(codes.InvalidArgument, "entry filters must fit actual creature/gameobject GUID entries")
	}
	if guid != 0 && !creatureGUID(guid) {
		return nil, status.Error(codes.InvalidArgument, "creature_guid must identify a creature or be zero")
	}
	s.settleKnockback(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	settleSelfSpline(&s.state.players[0], now)
	r := &worldv1.SnapshotResponse{Self: snapshot(s.state.players[0], now), IgnoredPackets: s.ignored, MalformedPackets: s.malformed, ObservedPackets: s.observed, LastOpcode: s.lastOpcode, LastParseError: s.lastError}
	if r.Self.Guid != 0 && r.Self.Present && r.Self.MapKnown {
		r.VisitRevision = s.state.visitRevision
	}
	r.SelfAmmoKnown, r.SelfAmmoEntry = s.state.ammoView()
	r.SelfAmmoRevision = s.state.ammoRevision
	r.SelfMovementFacts = s.state.ownMovement.snapshot(now)
	r.SelfSplineFacts = s.state.ownSpline.snapshot(now)
	r.SelfMotionAuthority = s.motionSnapshotLocked(now)
	r.SplineCompletion = s.completionSnapshotLocked()
	r.WorldTransfer = s.worldTransferSnapshotLocked(now)
	if owner := s.state.players[1]; owner.guid != 0 {
		r.Owner = snapshot(owner, now)
	}
	s.state.appendSnapshot(r, now)
	r.DiscoveryAnchor = s.state.discoverySnapshot(now)
	s.state.appendPlayerCorpses(r, now)
	s.state.appendRequested(r, now, request.GetCreatureEntry(), request.GetGameObjectEntry(), request.GetCreatureFactionTemplate())
	if request.GetIncludeGatheringObjects() {
		s.state.appendGatheringObjects(r, now)
	}
	if p := s.state.creatures[guid]; guid != 0 && p != nil && p.present && p.known[objectEntry] && p.fields[objectEntry] != 0 {
		r.RequestedCreature = creatureSnapshot(p, &s.state, now)
		r.RequestedCreatureStationaryPositionKnown = p.positionKnown && !p.positionInferred && p.stationaryPosition
	}
	return r, nil
}

// Callback failures are deliberately local: a malformed gameplay observation
// must not terminate the authenticated session. Changes commit per whole packet.
func (s *Service) OnPacket(ctx context.Context, packet *modv1.WorldPacket) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if packet == nil {
		s.malformed++
		s.lastError = "missing packet"
		return &emptypb.Empty{}, nil
	}
	objectUpdate := packet.Opcode == opcode.SMSGUpdateObject || packet.Opcode == opcode.SMSGCompressedUpdateObject
	s.lastOpcode = packet.Opcode
	if isPartyLocationOpcode(packet.Opcode) {
		scope, current := packet.TransportScope, s.motion.scope
		// Only a location-relevant group packet advances the physical source.
		// Ignore stale or unchanged envelopes before cloning World.
		if !sameMotionVisit(scope, current) || scope.MotionRevision <= current.MotionRevision {
			s.ignored++
			return &emptypb.Empty{}, nil
		}
	}
	s.observeCombatPacketLocked(packet.Opcode, packet.Payload, s.now())
	next := s.state.clone()
	observedAt := s.now()
	transferBefore := s.captureWorldTransferBeforeLocked(packet, observedAt)
	var handled bool
	var err error
	if isPartyLocationOpcode(packet.Opcode) {
		handled, err = true, next.observePartyLocation(packet)
	} else {
		handled, err = parsePacket(packet.Opcode, packet.Payload, &next, observedAt)
	}
	if err != nil {
		if isPartyLocationOpcode(packet.Opcode) {
			// A malformed newer owner record can withdraw the remote map proof.
			// None of its other fields are committed.
			s.state.partyLocation = next.partyLocation
			s.state.players[1].mapKnown = next.players[1].mapKnown
		}
		s.state.invalidateCreatureStationarity(packet.Opcode, packet.Payload)
		s.state.invalidateTransportPacket(packet.Opcode, packet.Payload)
		s.state.invalidateCreatureAuras(packet.Opcode, packet.Payload)
		if objectUpdate {
			_ = grpc.SetTrailer(ctx, metadata.Pairs(modcb.BodyInputBarrierTrailer, modcb.BodyUpdateMalformed))
			// The rejected batch could contain a new or moved unit.
			s.state.creatureCoverageLost = true
		}
		s.invalidateWorldTransferPacketLocked(packet)
		s.invalidateMalformedMotionLocked(packet)
		if packet.Opcode == opcode.SMSGMoveKnockBack || packet.Opcode == opcode.MSGMoveKnockBack {
			c := cursor{b: packet.Payload}
			guid := c.guid()
			p := &s.state.players[0]
			if c.err != nil || guid == 0 || guid == p.guid {
				s.knockback = nil
				p.positionKnown, p.positionInferred, p.movementKnown = false, false, false
				p.observedAt = observedAt
			}
		}
		s.malformed++
		s.lastError = fmt.Sprintf("%s (opcode 0x%03x)", err.Error(), packet.Opcode)
	} else if handled {
		old, current := s.state.players[0], next.players[0]
		next.observeOwnerDeparture(&s.state, packet)
		s.observeKnockbackLocked(ctx, packet, current, &next)
		next.commitOwnMovementFacts(old, packet.Opcode, observedAt, packet.TransportScope)
		s.observeMotionLocked(packet, old, current, &next, observedAt)
		if !current.present || !current.mapKnown || !current.positionKnown || old.positionRevision != current.positionRevision || old.mapKnown != current.mapKnown || old.mapID != current.mapID {
			next.discovery = clientDiscoveryAnchor{}
		}
		oldAmmoKnown, oldAmmo := s.state.ammoView()
		newAmmoKnown, newAmmo := next.ammoView()
		if oldAmmoKnown != newAmmoKnown || oldAmmo != newAmmo {
			next.ammoRevision++
		}
		if old.xpKnown != current.xpKnown || old.nextLevelXPKnown != current.nextLevelXPKnown || old.xp != current.xp || old.nextLevelXP != current.nextLevelXP {
			next.xpRevision++
		}
		if old.copperKnown != current.copperKnown || old.copper != current.copper {
			next.copperRevision++
		}
		if old.skillsKnown != current.skillsKnown || old.skills != current.skills {
			next.skillsRevision++
		}
		if old.freePrimaryKnown != current.freePrimaryKnown || old.freePrimary != current.freePrimary {
			next.freePrimaryRevision++
		}
		if s.state.inventoryView() != next.inventoryView() {
			next.inventoryRevision++
		}
		if s.state.equipmentView() != next.equipmentView() {
			next.equipmentRevision++
		}
		if !proto.Equal(s.state.offhandSnapshot(), next.offhandSnapshot()) {
			next.offhandRevision++
		}
		next.edited = nil
		s.state = next
		s.observeWorldTransferLocked(packet, current, &next, transferBefore, observedAt)
		s.observed++
		if objectUpdate {
			reason := next.bodyInputBarrier
			if !scopeMatchesSelf(packet.TransportScope, current, next.visitRevision) || !current.positionKnown && s.knockback == nil {
				reason = modcb.BodyUpdateUnbound
			}
			if reason == "" {
				_ = grpc.SetTrailer(ctx, metadata.Pairs(modcb.BodyInputUpdateTrailer, "1"))
			} else {
				trailer := metadata.Pairs(modcb.BodyInputBarrierTrailer, reason)
				if reason == modcb.BodyUpdateSelfField {
					trailer.Append(modcb.BodyInputFieldTrailer, next.bodyInputFields...)
				}
				if reason == modcb.BodyUpdateOwnerTravel {
					for _, p := range next.bodyOwnerPositions {
						trailer.Append(modcb.OwnerPositionTrailer, p.Wire())
					}
				}
				_ = grpc.SetTrailer(ctx, trailer)
			}
		}
	} else {
		s.invalidateWorldTransferPacketLocked(packet)
		s.observeUnsupportedMotionLocked(packet)
		s.ignored++
	}
	return &emptypb.Empty{}, nil
}
