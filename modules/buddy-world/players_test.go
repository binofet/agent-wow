package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Literal build12340 PLAYER4 CREATE: packedGUID4, Living MovementInfo, speeds,
// public sparse fields (mage5, health40/100, mana60/120, flags8). Zero-valued
// public player/ghost flags are omitted, as in normal server CREATE packets.
const nearbyPlayerGolden = "010000000301040420000000000000007b0000000000803f00000040000040400000000000000000000020400000e0400000904066669640000020400000e04000009040c3f54840c3f5484002000080030300400801080000280000003c00000064000000780000000500000008000000"

func nearbyByGUID(t *testing.T, r *worldv1.SnapshotResponse, guid uint64) *worldv1.PlayerState {
	t.Helper()
	for _, p := range r.NearbyPlayers {
		if p.Guid == guid {
			return p
		}
	}
	t.Fatalf("observed player%d absent: %v", guid, r.NearbyPlayers)
	return nil
}

func TestNearbyPlayerLiteralGoldenAndCompressedPublicVitals(t *testing.T) {
	golden, err := hex.DecodeString(nearbyPlayerGolden)
	if err != nil {
		t.Fatal(err)
	}
	for _, zipped := range []bool{false, true} {
		s := NewService(1)
		packet(t, s, opcode.SMSGLoginVerifyWorld, login())
		packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
		packet(t, s, opcode.SMSGUpdateObject, createFixture(3, vitalFields()))
		op, data := uint32(opcode.SMSGUpdateObject), golden
		if zipped {
			op, data = opcode.SMSGCompressedUpdateObject, compressed(t, golden)
		}
		r := packet(t, s, op, data)
		p := nearbyByGUID(t, r, 4)
		if r.MalformedPackets != 0 || len(r.NearbyPlayers) != 1 || !p.Present || !p.MapKnown || p.MapId != 0 || !p.PositionKnown || p.X != 1 || p.Y != 2 || p.Z != 3 || p.PositionRevision != 1 || !p.MovementFlagsKnown || !p.HealthKnown || p.Health != 40 || p.MaxHealth != 100 || !p.PowerKnown || p.Power != 60 || p.MaxPower != 120 || !p.AliveKnown || !p.Alive || !p.UnitFlagsKnown || p.UnitFlags != 8 || !p.ClassKnown || p.ClassId != 8 || !p.LevelKnown || p.Level != 5 {
			t.Fatal("public player golden mismatch", p, r.LastParseError)
		}
		if r.Self.Guid != 1 || r.Owner.Guid != 3 || r.Self.Health != 50 || r.Owner.Health != 50 || s.state.players[1].isSelf {
			t.Fatal("fixed players changed", r)
		}
	}
}

func TestNearbyPlayersMovementPowerFlagsAndStationaryAges(t *testing.T) {
	s := NewService(1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	fields := vitalFields()
	fields[unitBytes0] = 1 | (4 << 8) | (3 << 24)
	fields[unitPower1+3] = 80
	fields[unitMaxPower1+3] = 100
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(5, 4, fields))
	now = now.Add(time.Second)
	power := append(packedFixture(5), 3)
	power = append(power, 30, 0, 0, 0)
	r := packet(t, s, opcode.SMSGPowerUpdate, power)
	p := nearbyByGUID(t, r, 5)
	if !p.PowerKnown || p.PowerType != 3 || p.Power != 30 || p.ClassId != 4 {
		t.Fatal(p)
	}
	move, _ := hex.DecodeString(heartbeatGolden)
	move = append(packedFixture(5), move[2:]...)
	r = packet(t, s, opcode.MSGMoveHeartbeat, move)
	p = nearbyByGUID(t, r, 5)
	if p.PositionRevision != 2 || p.MovementFlags != 1 || p.MovementTime != 1234 || p.Orientation != .5 {
		t.Fatal(p)
	}
	rev := p.PositionRevision
	now = now.Add(30 * time.Second)
	r, _ = s.Snapshot(context.Background(), nil)
	p = nearbyByGUID(t, r, 5)
	if !p.Present || !p.PositionKnown || p.PositionAgeMs != 30000 || p.PositionRevision != rev || p.VitalsAgeMs != 30000 {
		t.Fatal("stationary evidence expired or refreshed", p)
	}
	for _, fields := range []map[int]uint32{{unitHealth: 0}, {unitHealth: 40, playerFlags: 0x10}, {playerFlags: 0, unitDynamicFlags: 0x20}} {
		r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(5, fields))
		p = nearbyByGUID(t, r, 5)
		if !p.AliveKnown || p.Alive {
			t.Fatal("dead/ghost player became healable", p)
		}
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(5, map[int]uint32{unitDynamicFlags: 0, unitHealth: 101}))
	if p = nearbyByGUID(t, r, 5); p.HealthKnown || p.AliveKnown {
		t.Fatal("invalid health accepted", p)
	}
}

func TestNearbyPlayersNeverAcquirePrivateOwnAuthorityOrExposeOtherFields(t *testing.T) {
	s := NewService(1)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	before := packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	fields := vitalFields()
	for key, value := range map[int]uint32{questLogFirst: 999, playerCoinage: 50000, playerSkillsFirst: 333, playerSkillsFirst + 1: 75, playerFreePrimary: 0, backpackFirst: 42, backpackFirst + 1: 0x40000000, playerOffhand: 43, playerOffhand + 1: 0x40000000, 0x162: 44, 0x163: 0x40000000, 0x55: 99, 0x78: 500, 0x12: 123, 0x13: 1, unitChannelSpell: 42340, unitChannelObject: 99} {
		fields[key] = value
	}
	r := packet(t, s, opcode.SMSGUpdateObject, objectFixture(4, 4, fields))
	p := nearbyByGUID(t, r, 4)
	if !proto.Equal(before.Self, r.Self) || r.SelfCopper != before.SelfCopper || r.SelfCopperRevision != before.SelfCopperRevision || r.SelfSkillsRevision != before.SelfSkillsRevision || r.SelfQuestLogRevision != before.SelfQuestLogRevision || r.InventoryRevision != before.InventoryRevision || r.SelfQuestLog[0].QuestId != 0 || r.Backpack[0].ItemGuid != 0 {
		t.Fatal("foreign private data mutated self", r)
	}
	cached := s.state.nearbyPlayers[4]
	if cached.isSelf || cached.questLogKnown || cached.backpackKnown || cached.copperKnown || cached.skillsKnown || cached.freePrimaryKnown || cached.mainhandKnown || cached.offhandKnown || cached.known[0x55] || cached.known[0x78] || cached.known[0x12] || cached.known[unitChannelSpell] {
		t.Fatal("foreign private/non-whitelisted evidence retained", cached)
	}
	allowed := map[string]bool{}
	for _, name := range []string{"guid", "present", "map_known", "map_id", "position_known", "x", "y", "z", "orientation", "position_age_ms", "position_revision", "movement_flags_known", "movement_flags", "extra_movement_flags", "movement_time", "health_known", "health", "max_health", "power_known", "power", "max_power", "power_type", "unit_flags_known", "unit_flags", "unit_flags_2", "player_flags", "dynamic_flags", "alive_known", "alive", "vitals_age_ms", "class_known", "class_id", "level_known", "level", "observation_age_ms"} {
		allowed[name] = true
	}
	p.ProtoReflect().Range(func(field protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if !allowed[string(field.Name())] {
			t.Errorf("unapproved nearby field%s", field.Name())
		}
		return true
	})
	if p.Mainhand != nil || p.Offhand != nil || p.TargetKnown || p.ChannelSpellKnown || p.BaseManaKnown || p.SpeedsKnown {
		t.Fatal(p)
	}
	// Even item metadata owned by the other player cannot enter own inventory.
	r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(0x400000000000002a, 1, map[int]uint32{objectEntry: 159, itemOwner: 4, itemStack: 2}))
	if len(s.state.items) != 0 || r.Backpack[0].ItemGuid != 0 {
		t.Fatal("foreign item granted inventory authority", r)
	}
}

func TestNearbyPlayerLifecycleNeverRecreatesFromUnobservedMovement(t *testing.T) {
	for _, mode := range []string{"destroy", "out_of_range", "new_world", "transfer"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			packet(t, s, opcode.SMSGUpdateObject, objectFixture(4, 4, vitalFields()))
			var r *worldv1.SnapshotResponse
			switch mode {
			case "destroy":
				b := new(bytes.Buffer)
				_ = binary.Write(b, binary.LittleEndian, uint64(4))
				b.WriteByte(0)
				r = packet(t, s, opcode.SMSGDestroyObject, b.Bytes())
			case "out_of_range":
				b := new(bytes.Buffer)
				u32(b, 1)
				b.WriteByte(4)
				u32(b, 1)
				b.Write(packedFixture(4))
				r = packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
			case "new_world":
				r = packet(t, s, opcode.SMSGNewWorld, login())
			case "transfer":
				r = packet(t, s, opcode.SMSGTransferPending, []byte{1, 0, 0, 0})
			}
			if len(r.NearbyPlayers) != 0 || len(s.state.nearbyPlayers) != 0 {
				t.Fatal("old player retained", r)
			}
			move, _ := hex.DecodeString(heartbeatGolden)
			move = append(packedFixture(4), move[2:]...)
			packet(t, s, opcode.MSGMoveHeartbeat, move)
			r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(4, map[int]uint32{unitHealth: 1}))
			if len(r.NearbyPlayers) != 0 || r.MalformedPackets != 0 {
				t.Fatal("unobserved packet invented player", r)
			}
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(4, 4, vitalFields()))
			if p := nearbyByGUID(t, r, 4); !p.MapKnown || !p.Present || !p.HealthKnown {
				t.Fatal(p)
			}
		})
	}
}

func TestNearbyPlayerMalformedUpdatesRollbackTogetherAndEveryCreatePrefix(t *testing.T) {
	s := NewService(1)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	golden, _ := hex.DecodeString(nearbyPlayerGolden)
	packet(t, s, opcode.SMSGUpdateObject, golden)
	before, _ := s.Snapshot(context.Background(), nil)
	first := deltaFixture(4, map[int]uint32{unitHealth: 1})
	second := deltaFixture(1, map[int]uint32{unitHealth: 2})
	third := objectFixture(5, 4, vitalFields())
	payload := append(append(append([]byte{}, first...), second[4:]...), third[4:]...)
	binary.LittleEndian.PutUint32(payload, 4)
	payload = append(payload, 255)
	r := packet(t, s, opcode.SMSGUpdateObject, payload)
	if r.MalformedPackets != before.MalformedPackets+1 || !proto.Equal(r.Self, before.Self) || len(r.NearbyPlayers) != 1 || !proto.Equal(r.NearbyPlayers[0], before.NearbyPlayers[0]) || s.state.nearbyPlayers[5] != nil {
		t.Fatal("packet rollback leaked changed/new player", r)
	}
	for n := 0; n < len(golden); n++ {
		packet(t, s, opcode.SMSGUpdateObject, golden[:n])
	}
	r, _ = s.Snapshot(context.Background(), nil)
	if len(r.NearbyPlayers) != 1 || !proto.Equal(r.NearbyPlayers[0], before.NearbyPlayers[0]) {
		t.Fatal("truncated CREATE changed public baseline", r)
	}
}

func TestNearbyPlayerPopulationBoundRetainsNearestAndEvictionIsAtomic(t *testing.T) {
	s := NewService(1)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for i := 180; i >= 1; i-- {
		guid := uint64(1000 + i)
		b := objectFixture(guid, 4, vitalFields())
		offset := 4 + 1 + len(packedFixture(guid)) + 1 + 2 + 10
		binary.LittleEndian.PutUint32(b[offset:], mathFloatBits(float32(i+1)))
		packet(t, s, opcode.SMSGUpdateObject, b)
	}
	r, _ := s.Snapshot(context.Background(), nil)
	if len(r.NearbyPlayers) != 64 || len(s.state.nearbyPlayers) != 64 {
		t.Fatal("unbounded player cache", len(r.NearbyPlayers), len(s.state.nearbyPlayers))
	}
	for i, p := range r.NearbyPlayers {
		if p.Guid != uint64(1001+i) {
			t.Fatal("nearest-player ranking/retention changed", i, p)
		}
	}
	before := proto.Clone(r).(*worldv1.SnapshotResponse)
	bad := objectFixture(99, 4, vitalFields())
	binary.LittleEndian.PutUint32(bad, 2)
	bad = append(bad, 255)
	r = packet(t, s, opcode.SMSGUpdateObject, bad)
	if !proto.Equal(&worldv1.SnapshotResponse{NearbyPlayers: r.NearbyPlayers}, &worldv1.SnapshotResponse{NearbyPlayers: before.NearbyPlayers}) || s.state.nearbyPlayers[99] != nil {
		t.Fatal("malformed admission evicted committed player", r)
	}
}

func TestNearbyPlayerRequiresActualPlayerCreateAndIndependentHasNoOwnerAuthority(t *testing.T) {
	s := NewIndependentService(1)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	move, _ := hex.DecodeString(heartbeatGolden)
	move = append(packedFixture(42), move[2:]...)
	packet(t, s, opcode.MSGMoveHeartbeat, move)
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(42, vitalFields()))
	power := append(packedFixture(42), 0, 1, 0, 0, 0)
	packet(t, s, opcode.SMSGPowerUpdate, power)
	for _, guid := range []uint64{0, 0x400000000000002a, 0xf13000012300002a, 0x1000000000000, 0x100000000} {
		packet(t, s, opcode.SMSGUpdateObject, objectFixture(guid, 4, vitalFields()))
	}
	r, _ := s.Snapshot(context.Background(), nil)
	if len(r.NearbyPlayers) != 0 || r.Owner != nil {
		t.Fatal("unknown/non-player packets invented player", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(3, vitalFields()))
	if len(r.NearbyPlayers) != 1 || r.NearbyPlayers[0].Guid != 3 || r.Owner != nil || r.OwnerTarget != nil || s.state.players[1] != (observed{}) {
		t.Fatal("ambient GUID3 gained owner authority", r)
	}
	before := proto.Clone(r.NearbyPlayers[0])
	r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(3, 3, npcFields(2)))
	if r.MalformedPackets != 1 || !proto.Equal(before, r.NearbyPlayers[0]) {
		t.Fatal("known player accepted another object type", r)
	}
}

func TestNearbyPlayerSnapshotAndPacketUpdatesRemainRaceSafe(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(4, 4, vitalFields()))
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for n := 0; n < 100; n++ {
				if worker%2 == 0 {
					_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGUpdateObject, Payload: deltaFixture(4, map[int]uint32{unitHealth: uint32(n % 100)})})
				} else {
					r, e := s.Snapshot(context.Background(), nil)
					if e != nil || len(r.NearbyPlayers) != 1 {
						t.Error(r, e)
					}
				}
			}
		}(i)
	}
	group.Wait()
}
