package buddyworld

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func gatheringFixture(t *testing.T) (*Service, uint64) {
	t.Helper()
	s := NewIndependentService(1)
	position := login()
	for n, v := range []float32{1, 2, 3} {
		binary.LittleEndian.PutUint32(position[4+n*4:], math.Float32bits(v))
	}
	packet(t, s, opcode.SMSGLoginVerifyWorld, position)
	g := uint64(0xf11000065200002a)
	packet(t, s, opcode.SMSGUpdateObject, gameObjectFixture(g, map[int]uint32{objectEntry: 1618, gameObjectBytes: 3<<8 | 1}))
	return s, g
}

func gatheringSnapshot(t *testing.T, s *Service, include bool) *worldv1.SnapshotResponse {
	t.Helper()
	r, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{IncludeGatheringObjects: include})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGatheringObjectsOptInActualCandidateAndSnapshotIsolation(t *testing.T) {
	s, guid := gatheringFixture(t)
	if len(gatheringSnapshot(t, s, false).NearbyGatheringObjects) != 0 {
		t.Fatal("discovery must be explicitly requested")
	}
	r := gatheringSnapshot(t, s, true)
	if len(r.NearbyGatheringObjects) != 1 || r.NearbyGatheringObjects[0].Guid != guid || !r.NearbyGatheringObjects[0].TypeKnown || r.NearbyGatheringObjects[0].GameObjectType != 3 || len(r.RequestedGameObjects) != 0 {
		t.Fatal(r)
	}
	r.NearbyGatheringObjects[0].X = 999
	if gatheringSnapshot(t, s, true).NearbyGatheringObjects[0].X == 999 {
		t.Fatal("discovery exposed mutable cache")
	}
}

func TestGatheringObjectsRequireKnownSameMapPoseAndType(t *testing.T) {
	for _, mode := range []string{"absent", "entry_unknown", "entry_mismatch", "type_unknown", "type", "map_unknown", "other_map", "pose_unknown", "nan", "self_unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, guid := gatheringFixture(t)
			p := s.state.gameObjects[guid]
			switch mode {
			case "absent":
				p.present = false
			case "entry_unknown":
				p.known[objectEntry] = false
			case "entry_mismatch":
				p.fields[objectEntry]++
			case "type_unknown":
				p.known[gameObjectBytes] = false
			case "type":
				p.fields[gameObjectBytes] = 19<<8 | 1
			case "map_unknown":
				p.mapKnown = false
			case "other_map":
				p.mapID++
			case "pose_unknown":
				p.positionKnown = false
			case "nan":
				p.x = float32(math.NaN())
			case "self_unknown":
				s.state.players[0].positionKnown = false
			}
			if len(gatheringSnapshot(t, s, true).NearbyGatheringObjects) != 0 {
				t.Fatal("unknown/ineligible observation became candidate")
			}
		})
	}
}

func TestGatheringObjectsRetainActualCandidateBeyondLastServerPosition(t *testing.T) {
	s := NewIndependentService(1)
	position := login()
	for n := 0; n < 3; n++ {
		binary.LittleEndian.PutUint32(position[4+n*4:], 0)
	}
	packet(t, s, opcode.SMSGLoginVerifyWorld, position)
	g := uint64(0xf11000065200002a)
	data := gameObjectFixture(g, map[int]uint32{objectEntry: 1618, gameObjectBytes: 3<<8 | 1})
	// The fixture's public CREATE position follows count/type/GUID/object-type,
	// update flags and movement flags. Set both actual world/fallback positions.
	at := 4 + 1 + len(packedFixture(g)) + 1 + 2 + 1
	for n, value := range []float32{30, 0, 0, 30, 0, 0} {
		binary.LittleEndian.PutUint32(data[at+n*4:], math.Float32bits(value))
	}
	packet(t, s, opcode.SMSGUpdateObject, data)
	r := gatheringSnapshot(t, s, true)
	if r.Self.X != 0 || len(r.NearbyGatheringObjects) != 1 || r.NearbyGatheringObjects[0].X != 30 {
		t.Fatal("raw server position incorrectly filtered actual cache", r)
	}
}

func TestGatheringObjectsBoundDistanceGuidOrderWithoutChangingCache(t *testing.T) {
	s, guid := gatheringFixture(t)
	base := s.state.gameObjects[guid]
	for n := 0; n < 40; n++ {
		p := *base
		p.guid = 0xf110000652000100 + uint64(n)
		p.x, p.y, p.z = s.state.players[0].x+float32(n/2), s.state.players[0].y, s.state.players[0].z
		s.state.gameObjects[p.guid] = &p
	}
	delete(s.state.gameObjects, guid)
	r := gatheringSnapshot(t, s, true)
	if len(r.NearbyGatheringObjects) != 32 || len(s.state.gameObjects) != 40 {
		t.Fatal(len(r.NearbyGatheringObjects), len(s.state.gameObjects))
	}
	for n, g := range r.NearbyGatheringObjects {
		if g.Guid != 0xf110000652000100+uint64(n) {
			t.Fatal("unstable distance/GUID order", n, g)
		}
	}
}
