// Package buddyworld retains read-only, bounded observations of two players.
package buddyworld

import (
	"context"
	"fmt"
	"sync"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/types/known/emptypb"
)

const OwnerGUID uint64 = 3

type observed struct {
	guid                                                         uint64
	present, mapKnown, positionKnown, movementKnown, speedsKnown bool
	mapID, movementFlags, extraFlags, movementTime               uint32
	x, y, z, orientation, walkSpeed, runSpeed                    float32
	positionRevision                                             uint64
	positionAt, vitalsAt, observedAt                             time.Time
	// Only fields up to PLAYER_FLAGS are needed, regardless of nearby population.
	fields            [playerFlags + 1]uint32
	known             [playerFlags + 1]bool
	isSelf            bool
	questLogKnown     bool
	questLog          [25]uint32
	questRevision     uint64
	backpackKnown     bool
	backpack          [16]uint64
	backpackRevision  uint64
	attackVictimKnown bool
	attackVictim      uint64
	attackAt          time.Time
	trajectory        observedTrajectory
}

type Service struct {
	worldv1.UnimplementedBuddyWorldServer
	mu                           sync.Mutex
	state                        observations
	now                          func() time.Time
	ignored, malformed, observed uint64
	lastOpcode                   uint32
	lastError                    string
}

func NewService(selfGUID uint64) *Service {
	return &Service{state: observations{players: [2]observed{{guid: selfGUID, isSelf: true}, {guid: OwnerGUID}}, creatures: make(map[uint64]*observed), items: make(map[uint64]*observed)}, now: time.Now}
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
		PositionKnown: p.positionKnown, X: p.x, Y: p.y, Z: p.z, Orientation: p.orientation,
		PositionAgeMs: age(now, p.positionAt), PositionRevision: p.positionRevision,
		MovementFlagsKnown: p.movementKnown, MovementFlags: p.movementFlags,
		ExtraMovementFlags: p.extraFlags, MovementTime: p.movementTime,
		Health: p.fields[unitHealth], MaxHealth: p.fields[unitMaxHealth],
		UnitFlagsKnown: p.known[unitFlags], UnitFlags: p.fields[unitFlags],
		PlayerFlags: p.fields[playerFlags], DynamicFlags: p.fields[unitDynamicFlags],
		TargetKnown: p.known[0x12] && p.known[0x13], TargetGuid: uint64(p.fields[0x12]) | uint64(p.fields[0x13])<<32, FactionTemplate: p.fields[0x37], UnitFlags_2: p.fields[0x3c],
		VitalsAgeMs: age(now, p.vitalsAt), ObservationAgeMs: age(now, p.observedAt),
		SpeedsKnown: p.speedsKnown, WalkSpeed: p.walkSpeed, RunSpeed: p.runSpeed,
	}
	r.HealthKnown = p.known[unitHealth] && p.known[unitMaxHealth] && r.MaxHealth > 0 && r.Health <= r.MaxHealth
	r.PowerType = p.fields[unitBytes0] >> 24
	if p.known[unitBytes0] && r.PowerType < 7 {
		i := int(r.PowerType)
		r.Power, r.MaxPower = p.fields[unitPower1+i], p.fields[unitMaxPower1+i]
		r.PowerKnown = p.known[unitPower1+i] && p.known[unitMaxPower1+i] && r.Power <= r.MaxPower
	}
	// Ghosts can have positive health. Public player flags must also be known.
	r.AliveKnown = r.HealthKnown && p.known[playerFlags]
	r.Alive = r.AliveKnown && r.Health > 0 && r.PlayerFlags&0x10 == 0 && r.DynamicFlags&0x20 == 0
	return r
}

func (s *Service) Snapshot(context.Context, *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	r := &worldv1.SnapshotResponse{Self: snapshot(s.state.players[0], now), Owner: snapshot(s.state.players[1], now), IgnoredPackets: s.ignored, MalformedPackets: s.malformed, ObservedPackets: s.observed, LastOpcode: s.lastOpcode, LastParseError: s.lastError}
	s.state.appendSnapshot(r, now)
	return r, nil
}

// Callback failures are deliberately local: a malformed gameplay observation
// must not terminate the authenticated session. Changes commit per whole packet.
func (s *Service) OnPacket(_ context.Context, packet *modv1.WorldPacket) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if packet == nil {
		s.malformed++
		s.lastError = "missing packet"
		return &emptypb.Empty{}, nil
	}
	s.lastOpcode = packet.Opcode
	next := s.state.clone()
	handled, err := parsePacket(packet.Opcode, packet.Payload, &next, s.now())
	if err != nil {
		s.malformed++
		s.lastError = fmt.Sprintf("%s (opcode 0x%03x)", err.Error(), packet.Opcode)
	} else if handled {
		next.edited = nil
		s.state = next
		s.observed++
	} else {
		s.ignored++
	}
	return &emptypb.Empty{}, nil
}
