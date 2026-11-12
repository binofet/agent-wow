package buddyworld

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func trainerCreate(guid uint64, entry, flags uint32, x float32) []byte {
	fields := npcFields(flags)
	fields[objectEntry] = entry
	b := objectFixture(guid, 3, fields)
	offset := 4 + 1 + len(packedFixture(guid)) + 1 + 2 + 10
	binary.LittleEndian.PutUint32(b[offset:], mathFloatBits(x))
	return b
}
func TestTrainerProjectionSurvivesDefaultNearbyTruncationAndPreservesPublicProof(t *testing.T) {
	s := NewIndependentService(1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for i := uint64(1); i <= 40; i++ {
		guid := uint64(0xf130000000000000) | uint64(23686)<<24 | i
		packet(t, s, opcode.SMSGUpdateObject, trainerCreate(guid, 23686, 0, float32(i)))
	}
	guid := uint64(0xf130000000000000) | uint64(1430)<<24 | 1
	r := packet(t, s, opcode.SMSGUpdateObject, trainerCreate(guid, 1430, 0x51, 50))
	if len(r.NearbyCreatures) != 32 || len(r.TrainerCreatures) != 1 || len(r.QuestGivers) != 0 {
		t.Fatal(r)
	}
	for _, c := range r.NearbyCreatures {
		if c.Guid == guid {
			t.Fatal("trainer did not exceed default slice")
		}
	}
	now = now.Add(3 * time.Second)
	r = creatureLookup(t, s, guid)
	if !proto.Equal(r.TrainerCreatures[0], r.RequestedCreature) || r.TrainerCreatures[0].ObservationAgeMs != 3000 || r.TrainerCreatures[0].PositionAgeMs != 3000 || !r.TrainerCreatures[0].Alive {
		t.Fatal(r)
	}
	r.TrainerCreatures[0].X = 999
	if creatureLookup(t, s, guid).TrainerCreatures[0].X != 50 {
		t.Fatal("trainer projection aliases cache")
	}
}
func TestTrainerProjectionBoundSortAndUnknownFlagsDoNotInventTrainer(t *testing.T) {
	s := NewIndependentService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for i := uint64(1); i <= 80; i++ {
		guid := uint64(0xf130000000000000) | uint64(1430)<<24 | i
		packet(t, s, opcode.SMSGUpdateObject, trainerCreate(guid, 1430, 0x51, float32(i)))
	}
	r := creatureLookup(t, s, 0)
	if len(r.TrainerCreatures) != 64 || len(s.state.creatures) != 80 {
		t.Fatal(r)
	}
	for i, c := range r.TrainerCreatures {
		if c.Guid&0xffffff != uint64(i+1) {
			t.Fatal("not ordered by observed self", i, c)
		}
	}
	noFlags := uint64(0xf130000000000000) | uint64(2329)<<24 | 1
	fields := npcFields(0)
	delete(fields, unitNPCFlags)
	fields[objectEntry] = 2329
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(noFlags, 3, fields))
	for _, c := range creatureLookup(t, s, 0).TrainerCreatures {
		if c.Guid == noFlags {
			t.Fatal("missing flags inferred trainer")
		}
	}
}
func TestTrainerProjectionFlagRemovalMalformedRollbackAndWorldLifecycle(t *testing.T) {
	for _, mode := range []string{"flags", "destroy", "out_of_range", "transfer", "new_world"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			guid := uint64(0xf130000000000000) | uint64(2329)<<24 | 1
			packet(t, s, opcode.SMSGUpdateObject, trainerCreate(guid, 2329, 0x51, 1))
			before := creatureLookup(t, s, 0).TrainerCreatures
			bad := deltaFixture(guid, map[int]uint32{unitNPCFlags: 0})
			packet(t, s, opcode.SMSGUpdateObject, bad[:len(bad)-1])
			if !proto.Equal(before[0], creatureLookup(t, s, 0).TrainerCreatures[0]) {
				t.Fatal("partial packet changed trainer")
			}
			switch mode {
			case "flags":
				packet(t, s, opcode.SMSGUpdateObject, deltaFixture(guid, map[int]uint32{unitNPCFlags: 0x40}))
			case "destroy":
				packet(t, s, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, guid), 0))
			case "out_of_range":
				p := binary.LittleEndian.AppendUint32(nil, 1)
				p = append(p, 4)
				p = binary.LittleEndian.AppendUint32(p, 1)
				packet(t, s, opcode.SMSGUpdateObject, append(p, packedFixture(guid)...))
			case "transfer":
				packet(t, s, opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, 1))
			case "new_world":
				packet(t, s, opcode.SMSGNewWorld, login())
			}
			r, _ := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{})
			if len(r.TrainerCreatures) != 0 {
				t.Fatal("trainer survived removal", r)
			}
		})
	}
}
