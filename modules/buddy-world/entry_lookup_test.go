package buddyworld

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestEntryFilteredCacheLookupsReturnBoundedRealBarrelsAndPracticeFires(t *testing.T) {
	s := NewIndependentService(1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for i := uint64(1); i <= 40; i++ {
		fire := uint64(0xf130000000000000) | uint64(23537)<<24 | i
		fields := npcFields(0)
		fields[3] = 23537
		packet(t, s, opcode.SMSGUpdateObject, objectFixture(fire, 3, fields))
		barrel := uint64(0xf110000000000000) | uint64(186234)<<24 | i
		packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(barrel, map[int]uint32{3: 186234, 0x11: 10<<8 | 1, 9: 4}))
	}
	// Decorative bucket props are actual objects, but not barrel entry186234.
	packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(0xf11002d8f600002a, map[int]uint32{3: 186614, 0x11: 5<<8 | 1}))
	before, _ := s.Snapshot(context.Background(), nil)
	now = now.Add(2 * time.Second)
	r, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureEntry: 23537, GameObjectEntry: 186234})
	if err != nil || len(r.RequestedCreatures) != 32 || len(r.RequestedGameObjects) != 32 || len(s.state.creatures) != 40 || len(s.state.gameObjects) != 41 {
		t.Fatal(r, err)
	}
	for i, c := range r.RequestedCreatures {
		if c.Entry != 23537 || !c.Present || !c.MapKnown || !c.PositionKnown || c.PositionAgeMs != 2000 || c.Guid&0xffffff != uint64(i+1) {
			t.Fatal(c)
		}
	}
	for i, g := range r.RequestedGameObjects {
		if g.Entry != 186234 || !g.Present || !g.TypeKnown || g.GameObjectType != 10 || !g.StateKnown || g.State != 1 || !g.FlagsKnown || g.Flags != 4 || g.PositionAgeMs != 2000 || g.Guid&0xffffff != uint64(i+1) {
			t.Fatal(g)
		}
	}
	if len(r.QuestObjects) != 0 || r.Owner != nil {
		t.Fatal("lookup invented crate or owner authority")
	}
	r.RequestedGameObjects[0].X = 900
	r.RequestedCreatures[0].Health = 999
	again, _ := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureEntry: 23537, GameObjectEntry: 186234})
	if again.RequestedGameObjects[0].X == 900 || again.RequestedCreatures[0].Health == 999 {
		t.Fatal("entry lookup aliases cached objects")
	}
	zero, _ := s.Snapshot(context.Background(), nil)
	if len(zero.RequestedCreatures) != 0 || len(zero.RequestedGameObjects) != 0 || zero.InventoryRevision != before.InventoryRevision || len(s.state.creatures) != 40 || len(s.state.gameObjects) != 41 {
		t.Fatal("lookup mutated observation state")
	}
}

func TestEntryLookupIdentityValidationAndDefaultCompatibility(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	before, _ := s.Snapshot(context.Background(), nil)
	zero, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{})
	if err != nil || !proto.Equal(before, zero) {
		t.Fatal(before, zero, err)
	}
	for _, request := range []*worldv1.SnapshotRequest{{CreatureEntry: 0x1000000}, {GameObjectEntry: 0x1000000}, {CreatureGuid: 3, CreatureEntry: 23537}} {
		if r, e := s.Snapshot(context.Background(), request); r != nil || status.Code(e) != codes.InvalidArgument {
			t.Fatal(request, r, e)
		}
	}
	r, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureEntry: 23537, GameObjectEntry: 186234})
	if err != nil || len(r.RequestedCreatures) != 0 || len(r.RequestedGameObjects) != 0 {
		t.Fatal("cache misses fabricated presence", r, err)
	}
}

func TestEntryLookupRemovalAndMalformedWholePacketPreserveObservedFacts(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	guid := uint64(0xf110000000000000) | uint64(186234)<<24 | 1
	packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(guid, map[int]uint32{3: 186234, 0x11: 10<<8 | 1}))
	query := &worldv1.SnapshotRequest{GameObjectEntry: 186234}
	before, _ := s.Snapshot(context.Background(), query)
	bad := deltaFixture(guid, map[int]uint32{3: 186614, 0x11: 5<<8 | 1})
	binary.LittleEndian.PutUint32(bad, 2)
	bad = append(bad, 255)
	packet(t, s, opcode.SMSGUpdateObject, bad)
	r, _ := s.Snapshot(context.Background(), query)
	if len(r.RequestedGameObjects) != 1 || !proto.Equal(r.RequestedGameObjects[0], before.RequestedGameObjects[0]) {
		t.Fatal("malformed entry update committed", r)
	}
	packet(t, s, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, guid), 0))
	r, _ = s.Snapshot(context.Background(), query)
	if len(r.RequestedGameObjects) != 0 {
		t.Fatal("removed barrel remained present")
	}
	packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(guid, map[int]uint32{3: 186234, 0x11: 10<<8 | 1}))
	packet(t, s, opcode.SMSGTransferPending, []byte{1, 0, 0, 0})
	r, _ = s.Snapshot(context.Background(), query)
	if len(r.RequestedGameObjects) != 0 {
		t.Fatal("old-map barrel remained present")
	}
}
