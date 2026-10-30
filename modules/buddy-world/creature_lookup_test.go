package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func creatureLookup(t *testing.T, s *Service, guid uint64) *worldv1.SnapshotResponse {
	t.Helper()
	r, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureGuid: guid})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRequestedCreatureOutsideNearbyLimitDoesNotChangeDiscovery(t *testing.T) {
	s := NewIndependentService(1)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for i := range 40 {
		packet(t, s, opcode.SMSGUpdateObject, independentCreature(npcGUID+uint64(i), float32(i+1)))
	}
	before := creatureLookup(t, s, 0)
	guid := npcGUID + 39
	if len(before.NearbyCreatures) != 32 || len(s.state.creatures) != 40 {
		t.Fatal("fixture did not exceed discovery limit", before)
	}
	for _, c := range before.NearbyCreatures {
		if c.Guid == guid {
			t.Fatal("requested creature was already in nearest32", c)
		}
	}
	r := creatureLookup(t, s, guid)
	if c := r.RequestedCreature; c == nil || c.Guid != guid || !c.Present || c.X != 40 || !c.DistanceToSelfObservedKnown || c.DistanceToSelfObserved != 39 || c.DistanceToOwnerObservedKnown {
		t.Fatal("lookup failed to return retained creature with actual distances", r)
	}
	if r.Owner != nil || r.OwnerTarget != nil || len(s.state.creatures) != 40 {
		t.Fatal("lookup restored owner or changed cache", r)
	}
	copy := proto.Clone(r).(*worldv1.SnapshotResponse)
	copy.RequestedCreature = nil
	if !proto.Equal(before, copy) {
		t.Fatal("lookup changed ordinary snapshot", before, copy)
	}
	r.RequestedCreature.X = 999
	if again := creatureLookup(t, s, guid); again.RequestedCreature.X != 40 {
		t.Fatal("caller changed cached observation", again)
	}
}

func TestRequestedCreatureIdentityValidationAndDefaultSnapshot(t *testing.T) {
	s := NewService(1)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, vitalFields()))
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(0)))
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{0x12: uint32(npcGUID & 0xffffffff), 0x13: uint32(npcGUID >> 32)}))
	before := creatureLookup(t, s, 0)
	nilRequest, err := s.Snapshot(context.Background(), nil)
	if err != nil || !proto.Equal(before, nilRequest) || before.RequestedCreature != nil {
		t.Fatal("nil/zero request changed existing snapshot contract", nilRequest, err)
	}
	r := creatureLookup(t, s, npcGUID)
	if !proto.Equal(r.RequestedCreature, before.OwnerTarget) || !proto.Equal(r.Self, before.Self) || !proto.Equal(r.Owner, before.Owner) || !proto.Equal(r.OwnerTarget, before.OwnerTarget) {
		t.Fatal("lookup changed self or owner authority", r)
	}
	for _, guid := range []uint64{1, 3, 0x4000000000000042, 0xf11000012300002a, 0xf130000123000000} {
		result, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureGuid: guid})
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid creature GUID %x accepted: %v %v", guid, result, err)
		}
	}
	if missing := creatureLookup(t, s, npcGUID+1); missing.RequestedCreature != nil {
		t.Fatal("cache miss invented creature", missing)
	}
	if after := creatureLookup(t, s, 0); !proto.Equal(before, after) {
		t.Fatal("lookup changed default snapshot", before, after)
	}
	// A public CREATE without an entry remains nonactionable for lookup too.
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID+1, 3, map[int]uint32{unitHealth: 100, unitMaxHealth: 100}))
	if unknownEntry := creatureLookup(t, s, npcGUID+1); unknownEntry.RequestedCreature != nil {
		t.Fatal("zero entry was returned as a known creature", unknownEntry)
	}
}

func TestRequestedCreaturePreservesActualAgesAndPosition(t *testing.T) {
	s := NewIndependentService(1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(0)))
	packet(t, s, opcode.SMSGMonsterMove, splineLiteral(t, monsterLinearGolden))
	attack := binary.LittleEndian.AppendUint64(nil, npcGUID)
	attack = binary.LittleEndian.AppendUint64(attack, 1)
	packet(t, s, opcode.SMSGAttackstart, attack)
	before := creatureLookup(t, s, npcGUID).RequestedCreature
	for _, elapsed := range []time.Duration{1500 * time.Millisecond, 2 * time.Second} {
		now = now.Add(elapsed)
		r := creatureLookup(t, s, npcGUID)
		c := r.RequestedCreature
		wantAge := uint64(now.Sub(time.Unix(1000, 0)) / time.Millisecond)
		if c == nil || c.PositionAgeMs != wantAge || c.ObservationAgeMs != wantAge || c.AttackAgeMs != wantAge || c.Trajectory == nil || c.Trajectory.AgeMs != wantAge {
			t.Fatal("lookup refreshed actual observation ages", r)
		}
		if c.PositionRevision != before.PositionRevision || c.X != 10 || c.Y != 20 || c.Z != 30 || c.Trajectory.DestinationX != 14 || c.AttackVictimGuid != 1 || !proto.Equal(c, r.NearbyCreatures[0]) {
			t.Fatal("lookup predicted a position or changed actual creature state", r)
		}
		if r.Self.PositionAgeMs != wantAge {
			t.Fatal("self and requested creature used different snapshot time", r)
		}
	}
	// Every part of one snapshot uses a single clock read under the same lock.
	clockReads := 0
	s.now = func() time.Time {
		clockReads++
		return now.Add(time.Duration(clockReads) * time.Millisecond)
	}
	r := creatureLookup(t, s, npcGUID)
	if clockReads != 1 || r.Self.PositionAgeMs != r.RequestedCreature.PositionAgeMs {
		t.Fatal("lookup was not part of the atomic world snapshot", clockReads, r)
	}
}

func TestRequestedCreatureRetainsObservedDeath(t *testing.T) {
	s := NewIndependentService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(0)))
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{unitHealth: 0, unitDynamicFlags: 1}))
	r := creatureLookup(t, s, npcGUID)
	c := r.RequestedCreature
	if c == nil || !c.Present || !c.HealthKnown || c.Health != 0 || !c.AliveKnown || c.Alive || len(r.LootableCorpses) != 1 || !proto.Equal(c, r.LootableCorpses[0]) {
		t.Fatal("observed death became lookup absence or stale alive state", r)
	}
}

func TestRequestedCreatureAbsentAfterServerRemovalOrTransfer(t *testing.T) {
	for _, name := range []string{"destroy", "out of range", "transfer pending", "new world"} {
		t.Run(name, func(t *testing.T) {
			s := NewIndependentService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(0)))
			if creatureLookup(t, s, npcGUID).RequestedCreature == nil {
				t.Fatal("fixture creature missing before removal")
			}
			switch name {
			case "destroy":
				payload := append(binary.LittleEndian.AppendUint64(nil, npcGUID), 0)
				packet(t, s, opcode.SMSGDestroyObject, payload)
			case "out of range":
				b := new(bytes.Buffer)
				u32(b, 1)
				b.WriteByte(4)
				u32(b, 1)
				b.Write(packedFixture(npcGUID))
				packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
			case "transfer pending":
				packet(t, s, opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
			case "new world":
				packet(t, s, opcode.SMSGNewWorld, login())
			}
			if r := creatureLookup(t, s, npcGUID); r.RequestedCreature != nil || len(s.state.creatures) != 0 || r.MalformedPackets != 0 {
				t.Fatal("lookup resurrected removed creature", r)
			}
		})
	}
}

func TestRequestedCreatureLookupDoesNotPinAgainstCacheEviction(t *testing.T) {
	s := NewIndependentService(1)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	packet(t, s, opcode.SMSGUpdateObject, independentCreature(npcGUID, 300))
	for i := 1; i < maxCreatures; i++ {
		packet(t, s, opcode.SMSGUpdateObject, independentCreature(npcGUID+uint64(i), float32(i)))
	}
	for range 3 {
		if r := creatureLookup(t, s, npcGUID); r.RequestedCreature == nil || len(s.state.creatures) != maxCreatures {
			t.Fatal("retained creature missing before eviction", r)
		}
	}
	packet(t, s, opcode.SMSGUpdateObject, independentCreature(npcGUID+maxCreatures, 0))
	if r := creatureLookup(t, s, npcGUID); r.RequestedCreature != nil || len(s.state.creatures) != maxCreatures || s.state.creatures[npcGUID] != nil {
		t.Fatal("lookup pinned or resurrected evicted creature", r)
	}
}

func TestRequestedCreatureDescriptorUsesExistingSnapshotAlias(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "buddy-world")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"module.yaml", "module.pb", "compose.yaml"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	module, ok := registry.Lookup("buddy-world")
	if !ok {
		t.Fatal("missing buddy-world descriptor")
	}
	method, ok := module.RPC("snapshot")
	if !ok || method.Path() != "/warcraftbuddy.world.v1.BuddyWorld/Snapshot" || len(module.Manifest().RPC) != 1 {
		t.Fatal("lookup changed snapshot alias or added RPC authority")
	}
	guid := method.Descriptor().Input().Fields().ByName("creature_guid")
	creature := method.Descriptor().Output().Fields().ByName("requested_creature")
	if guid == nil || guid.Number() != 1 || guid.Kind() != protoreflect.Uint64Kind || creature == nil || creature.Number() != 23 || creature.Kind() != protoreflect.MessageKind || creature.Message().FullName() != "warcraftbuddy.world.v1.CreatureState" {
		t.Fatal("manifest snapshot descriptor has stale lookup schema", guid, creature)
	}
}
