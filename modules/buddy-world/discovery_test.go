package buddyworld

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func discoveryRig(t *testing.T) (*Service, *worldv1.ClientMovementRequest) {
	t.Helper()
	s := NewService(1)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	w := packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	s.state.edited = make(map[uint64]bool)
	return s, &worldv1.ClientMovementRequest{SelfGuid: 1, MapId: w.Self.MapId, ServerPositionRevision: w.Self.PositionRevision, Sequence: 1, Valid: true, X: 201, Y: 2, Z: 3}
}

func discoveryNPC(s *Service, n int, x float32) *observed {
	p := &observed{guid: 0xf130000123000000 | uint64(n), present: true, mapKnown: true, positionKnown: true, x: x, y: 2, z: 3, positionRevision: 1, observedAt: s.now(), positionAt: s.now()}
	p.fields[objectEntry], p.known[objectEntry] = 291, true
	p.fields[unitHealth], p.fields[unitMaxHealth] = 100, 100
	p.known[unitHealth], p.known[unitMaxHealth] = true, true
	p.fields[unitNPCFlags], p.known[unitNPCFlags] = 2, true
	s.state.putCreature(p)
	return p
}

func publishDiscovery(t *testing.T, s *Service, r *worldv1.ClientMovementRequest) {
	t.Helper()
	if _, err := s.ObserveClientMovement(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryUsesClientWriteWithoutChangingServerObservations(t *testing.T) {
	s, req := discoveryRig(t)
	for i := 1; i <= 40; i++ {
		discoveryNPC(s, i, float32(i+1))
	}
	near := discoveryNPC(s, 99, 202)
	before, _ := s.Snapshot(context.Background(), nil)
	publishDiscovery(t, s, req)
	after, _ := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureGuid: near.guid, CreatureEntry: 291})
	if !proto.Equal(before.Self, after.Self) || after.DiscoveryAnchor == nil || !after.DiscoveryAnchor.Estimated || after.DiscoveryAnchor.ServerPositionRevision != before.Self.PositionRevision || after.DiscoveryAnchor.Sequence != 1 {
		t.Fatal("client estimate changed raw server pose or lost provenance", after)
	}
	if after.NearbyCreatures[0].Guid != near.guid || after.QuestGivers[0].Guid != near.guid || after.RequestedCreatures[0].Guid != near.guid || after.RequestedCreature.DistanceToSelfObserved != 201 || after.RequestedCreature.X != 202 || after.RequestedCreature.PositionRevision != 1 {
		t.Fatal("discovery used stale raw ranking or relabeled observed distance", after)
	}
	s.now = func() time.Time { return time.Unix(1005, 0) }
	after, _ = s.Snapshot(context.Background(), nil)
	if after.DiscoveryAnchor.AgeMs != 5000 || after.Self.PositionAgeMs != 5000 {
		t.Fatal("ages fabricated", after)
	}
}

func TestDiscoveryCacheAdmissionAndAttackersCannotBeHidden(t *testing.T) {
	s, req := discoveryRig(t)
	for i := 1; i <= maxCreatures; i++ {
		discoveryNPC(s, i, float32(i))
	}
	publishDiscovery(t, s, req)
	near := discoveryNPC(s, 900, 202)
	if s.state.creatures[near.guid] == nil || len(s.state.creatures) != maxCreatures {
		t.Fatal("cache admitted using old server pose")
	}
	attacker := discoveryNPC(s, 901, 1000)
	// An actual incoming damage/ATTACKSTART event establishes the relation.
	attacker.attackVictimKnown, attacker.attackVictim = true, 1
	s.state.putCreature(attacker)
	w, _ := s.Snapshot(context.Background(), nil)
	if w.NearbyCreatures[0].Guid != attacker.guid || len(w.NearbyCreatures) != 32 {
		t.Fatal("actual attacker hidden", w.NearbyCreatures)
	}
	for i := 1000; i < 1200; i++ {
		discoveryNPC(s, i, float32(202+i%3))
	}
	if s.state.creatures[attacker.guid] == nil {
		t.Fatal("attacker evicted by nearby unengaged population")
	}
	stop := append(packedFixture(attacker.guid), packedFixture(1)...)
	stop = binary.LittleEndian.AppendUint32(stop, 0)
	w = packet(t, s, opcode.SMSGAttackstop, stop)
	if w.NearbyCreatures[0].Guid == attacker.guid {
		t.Fatal("actual STOP did not remove attacker priority")
	}
}

func TestDiscoveryRejectsStaleForeignInvalidAndOutOfOrderReports(t *testing.T) {
	s, req := discoveryRig(t)
	publishDiscovery(t, s, req)
	before, _ := s.Snapshot(context.Background(), nil)
	for name, change := range map[string]func(*worldv1.ClientMovementRequest){
		"foreign":  func(r *worldv1.ClientMovementRequest) { r.SelfGuid = 4 },
		"map":      func(r *worldv1.ClientMovementRequest) { r.MapId = 1 },
		"revision": func(r *worldv1.ClientMovementRequest) { r.ServerPositionRevision++ },
		"nan":      func(r *worldv1.ClientMovementRequest) { r.X = float32(math.NaN()) },
		"inf":      func(r *worldv1.ClientMovementRequest) { r.Z = float32(math.Inf(1)) },
		"sequence": func(r *worldv1.ClientMovementRequest) { r.Sequence = 1 },
		"zero":     func(r *worldv1.ClientMovementRequest) { r.Sequence = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			r := proto.Clone(req).(*worldv1.ClientMovementRequest)
			r.Sequence = 2
			change(r)
			if _, err := s.ObserveClientMovement(context.Background(), r); err == nil {
				t.Fatal("accepted invalid report")
			}
			after, _ := s.Snapshot(context.Background(), nil)
			if !proto.Equal(before, after) {
				t.Fatal("invalid report mutated observations")
			}
		})
	}
	invalid := &worldv1.ClientMovementRequest{SelfGuid: 1, Sequence: 3, Valid: false}
	publishDiscovery(t, s, invalid)
	after, _ := s.Snapshot(context.Background(), nil)
	if after.DiscoveryAnchor != nil || !proto.Equal(before.Self, after.Self) {
		t.Fatal("ambiguous write did not invalidate only anchor")
	}
	if _, err := s.ObserveClientMovement(context.Background(), req); err == nil {
		t.Fatal("old valid report revived invalidated anchor")
	}
	req.Sequence = 4
	publishDiscovery(t, s, req)
}

func TestDiscoveryAuthoritativeEpochInvalidatesButForeignOrMalformedDoesNot(t *testing.T) {
	for _, kind := range []string{"own_pose", "transfer", "new_world", "absence"} {
		t.Run(kind, func(t *testing.T) {
			s, req := discoveryRig(t)
			publishDiscovery(t, s, req)
			foreign, _ := hex.DecodeString(heartbeatGolden)
			packet(t, s, opcode.MSGMoveHeartbeat, foreign)
			packet(t, s, opcode.MSGMoveHeartbeat, []byte{1, 1})
			w, _ := s.Snapshot(context.Background(), nil)
			if w.DiscoveryAnchor == nil {
				t.Fatal("foreign/malformed packet invalidated own anchor")
			}
			switch kind {
			case "own_pose":
				foreign[1] = 1
				packet(t, s, opcode.MSGMoveHeartbeat, foreign)
			case "transfer":
				packet(t, s, opcode.SMSGTransferPending, make([]byte, 4))
			case "new_world":
				packet(t, s, opcode.SMSGNewWorld, login())
			case "absence":
				packet(t, s, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, 1), 0))
			}
			w, _ = s.Snapshot(context.Background(), nil)
			if w.DiscoveryAnchor != nil {
				t.Fatal("authoritative epoch retained estimate", kind)
			}
			if _, err := s.ObserveClientMovement(context.Background(), req); err == nil {
				t.Fatal("stale report revived prior epoch")
			}
		})
	}
}
