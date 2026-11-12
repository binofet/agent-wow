package buddyworld

import (
	"bytes"
	"context"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func factionLookupCreature(t *testing.T, s *Service, entry uint32, low uint64, faction uint32) uint64 {
	t.Helper()
	guid := uint64(0xf130000000000000) | uint64(entry)<<24 | low
	fields := npcFields(0)
	fields[objectEntry], fields[0x37] = entry, faction
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(guid, 3, fields))
	return guid
}

func TestFactionLookupIncludesObservedCandidatesBeyondNearbyTruncation(t *testing.T) {
	s := NewIndependentService(1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for i := uint64(1); i <= 40; i++ {
		factionLookupCreature(t, s, 100, i, 32)
	}
	wanted := []uint64{factionLookupCreature(t, s, 1718, 1, 59), factionLookupCreature(t, s, 1719, 1, 59)}
	for _, guid := range wanted {
		stop := new(bytes.Buffer)
		stop.Write(packedFixture(guid))
		stop.WriteByte(0)
		for _, v := range []float32{100, 200, 3} {
			f32(stop, v)
		}
		u32(stop, 1)
		stop.WriteByte(1)
		packet(t, s, opcode.SMSGMonsterMove, stop.Bytes())
	}
	now = now.Add(2 * time.Second)
	before, _ := s.Snapshot(context.Background(), nil)
	if len(before.NearbyCreatures) != 32 {
		t.Fatal("nearby fixture did not saturate", len(before.NearbyCreatures))
	}
	for _, c := range before.NearbyCreatures {
		if c.FactionTemplate == 59 {
			t.Fatal("faction candidate was not outside nearby truncation", c)
		}
	}
	r, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureFactionTemplate: 59})
	if err != nil || len(r.RequestedCreatures) != len(wanted) {
		t.Fatal(r, err)
	}
	for i, c := range r.RequestedCreatures {
		if c.Guid != wanted[i] || c.FactionTemplate != 59 || !c.Present || !c.MapKnown || c.MapId != 0 || c.X != 100 || c.Y != 200 || c.PositionAgeMs != 2000 {
			t.Fatal("lookup lost actual faction/pose/age", c)
		}
	}
	r.RequestedCreatures[0].Health = 999
	again, _ := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureFactionTemplate: 59})
	if again.RequestedCreatures[0].Health == 999 {
		t.Fatal("faction projection aliases cached observations")
	}
	after, _ := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{})
	if !proto.Equal(before, after) || len(s.state.creatures) != 42 {
		t.Fatal("read-only faction lookup changed default ranking/cache", before, after)
	}
}

func TestFactionLookupFiltersConjunctivelyAndRetainsUncertainty(t *testing.T) {
	s := NewIndependentService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	match := factionLookupCreature(t, s, 1718, 1, 59)
	otherEntry := factionLookupCreature(t, s, 1719, 2, 59)
	otherFaction := factionLookupCreature(t, s, 1718, 3, 32)
	unknownFaction := factionLookupCreature(t, s, 1718, 4, 59)
	s.state.creatures[unknownFaction].known[0x37] = false
	foreign := factionLookupCreature(t, s, 1718, 5, 59)
	s.state.creatures[foreign].mapID = 1
	unknownMap := factionLookupCreature(t, s, 1718, 6, 59)
	s.state.creatures[unknownMap].mapKnown = false
	absent := factionLookupCreature(t, s, 1718, 7, 59)
	s.state.creatures[absent].present = false
	unknownEntry := factionLookupCreature(t, s, 1720, 8, 59)
	s.state.creatures[unknownEntry].known[objectEntry] = false
	s.state.creatures[unknownEntry].fields[objectEntry] = 0

	r, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureFactionTemplate: 59})
	if err != nil || len(r.RequestedCreatures) != 4 {
		t.Fatal(r, err)
	}
	got := map[uint64]*worldv1.CreatureState{}
	for _, c := range r.RequestedCreatures {
		got[c.Guid] = c
	}
	if got[match] == nil || got[otherEntry] == nil || got[unknownMap] == nil || got[unknownMap].MapKnown || got[unknownEntry] == nil || got[unknownEntry].Entry != 0 {
		t.Fatal("known same-map and uncertain faction candidates were hidden", r.RequestedCreatures)
	}
	for _, guid := range []uint64{otherFaction, unknownFaction, foreign, absent} {
		if got[guid] != nil {
			t.Fatal("nonmatching/unobserved/foreign/absent candidate admitted", got[guid])
		}
	}
	combined, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureEntry: 1718, CreatureFactionTemplate: 59})
	if err != nil || len(combined.RequestedCreatures) != 2 {
		t.Fatal("entry and faction became a union", combined, err)
	}
	for _, c := range combined.RequestedCreatures {
		if c.Guid != match && c.Guid != unknownMap {
			t.Fatal("combined filter admitted another entry/faction", c)
		}
	}
	entryOnly, _ := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureEntry: 1718})
	if len(entryOnly.RequestedCreatures) != 5 {
		t.Fatal("faction addition changed legacy entry-only filtering", entryOnly)
	}
	for _, request := range []*worldv1.SnapshotRequest{nil, {}, {CreatureFactionTemplate: 0}} {
		zero, err := s.Snapshot(context.Background(), request)
		if err != nil || len(zero.RequestedCreatures) != 0 {
			t.Fatal("default request gained a faction lookup", zero, err)
		}
	}
}

func TestFactionLookupRequiresOwnKnownMapAndPreservesBound(t *testing.T) {
	s := NewIndependentService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for i := uint64(1); i <= 40; i++ {
		factionLookupCreature(t, s, 1718, i, 59)
	}
	query := &worldv1.SnapshotRequest{CreatureFactionTemplate: 59}
	r, err := s.Snapshot(context.Background(), query)
	if err != nil || len(r.RequestedCreatures) != 32 || len(s.state.creatures) != 40 {
		t.Fatal("faction lookup lost bounded cache projection", r, err)
	}
	for i, c := range r.RequestedCreatures {
		if c.Guid&0xffffff != uint64(i+1) {
			t.Fatal("faction results lost deterministic existing ordering", c)
		}
	}
	s.state.players[0].mapKnown = false
	r, _ = s.Snapshot(context.Background(), query)
	if len(r.RequestedCreatures) != 0 {
		t.Fatal("unknown own map granted faction projection", r)
	}
	s.state.players[0].mapKnown = true
	s.state.players[0].present = false
	r, _ = s.Snapshot(context.Background(), query)
	if len(r.RequestedCreatures) != 0 {
		t.Fatal("missing own body granted faction projection", r)
	}
}
