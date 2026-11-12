package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func skinnableFields() map[int]uint32 {
	return map[int]uint32{objectEntry: 291, unitHealth: 0, unitMaxHealth: 100, unitFlags: 0x04000000, 0x36: 5}
}

func TestSkinnableCorpseActualCreateZeroFlagsAndExactLookup(t *testing.T) {
	for _, compressedPacket := range []bool{false, true} {
		s := NewIndependentService(1)
		packet(t, s, opcode.SMSGLoginVerifyWorld, login())
		data, op := objectFixture(npcGUID, 3, skinnableFields()), uint32(opcode.SMSGUpdateObject)
		if compressedPacket {
			data, op = compressed(t, data), opcode.SMSGCompressedUpdateObject
		}
		r := packet(t, s, op, data)
		if r.MalformedPackets != 0 || len(r.SkinnableCorpses) != 1 || len(r.LootableCorpses) != 0 {
			t.Fatal(r)
		}
		c := r.SkinnableCorpses[0]
		if !c.UnitFlagsKnown || !c.DynamicFlagsKnown || c.UnitFlags != 0x04000000 || c.DynamicFlags != 0 || !c.HealthKnown || c.Health != 0 || !c.AliveKnown || c.Alive || !c.LevelKnown || c.Level != 5 {
			t.Fatal(c)
		}
		exact := creatureLookup(t, s, npcGUID)
		if !proto.Equal(c, exact.RequestedCreature) || !proto.Equal(c, r.NearbyCreatures[0]) {
			t.Fatal("lookup/collection knownness disagree", exact)
		}
		// An actual CREATE supplies the omitted public zero baseline; a zero value
		// alone in a partial/unknown observation is not interchangeable with it.
		c.UnitFlags = 0
		if creatureLookup(t, s, npcGUID).RequestedCreature.UnitFlags != 0x04000000 {
			t.Fatal("caller mutated cached observation")
		}
	}
}

func TestSkinnableCorpseRequiresKnownFlagsAndActualDeadUnlootableState(t *testing.T) {
	for _, name := range []string{"unit_unknown", "dynamic_unknown", "health_unknown", "max_health_unknown", "zero_max_health", "alive", "lootable", "not_skinnable", "absent", "unknown_entry"} {
		t.Run(name, func(t *testing.T) {
			s := NewIndependentService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, skinnableFields()))
			s.mu.Lock()
			p := s.state.creatures[npcGUID]
			switch name {
			case "unit_unknown":
				p.known[unitFlags] = false
			case "dynamic_unknown":
				p.known[unitDynamicFlags] = false
			case "health_unknown":
				p.known[unitHealth] = false
			case "max_health_unknown":
				p.known[unitMaxHealth] = false
			case "zero_max_health":
				p.fields[unitMaxHealth] = 0
			case "alive":
				p.fields[unitHealth] = 1
			case "lootable":
				p.fields[unitDynamicFlags] = 1
			case "not_skinnable":
				p.fields[unitFlags] = 0
			case "absent":
				p.present = false
			case "unknown_entry":
				p.known[objectEntry] = false
			}
			s.mu.Unlock()
			r := creatureLookup(t, s, npcGUID)
			if len(r.SkinnableCorpses) != 0 {
				t.Fatal("unknown/ineligible corpse became skinning candidate", r)
			}
			if name == "unit_unknown" && r.RequestedCreature.UnitFlagsKnown || name == "dynamic_unknown" && r.RequestedCreature.DynamicFlagsKnown {
				t.Fatal("unknown flag advertised known", r.RequestedCreature)
			}
		})
	}
}

func TestSkinnableCorpseLootFlagTransitionAndMalformedRollback(t *testing.T) {
	s := NewIndependentService(1)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	fields := skinnableFields()
	fields[unitDynamicFlags] = 1
	r := packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, fields))
	if len(r.SkinnableCorpses) != 0 || len(r.LootableCorpses) != 1 {
		t.Fatal(r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{unitDynamicFlags: 0}))
	if len(r.SkinnableCorpses) != 1 || len(r.LootableCorpses) != 0 {
		t.Fatal("actual cleared loot flag not observed", r)
	}
	before := proto.Clone(r.SkinnableCorpses[0]).(*worldv1.CreatureState)
	data := deltaFixture(npcGUID, map[int]uint32{unitFlags: 0})
	for n := range len(data) {
		r = packet(t, s, opcode.SMSGUpdateObject, data[:n])
		if len(r.SkinnableCorpses) != 1 || !proto.Equal(before, r.SkinnableCorpses[0]) {
			t.Fatal("partial packet changed skinning evidence", n, r)
		}
	}
	r = packet(t, s, opcode.SMSGUpdateObject, data)
	if len(r.SkinnableCorpses) != 0 || !creatureLookup(t, s, npcGUID).RequestedCreature.UnitFlagsKnown {
		t.Fatal("actual skinnable clear ignored", r)
	}
}

func TestSkinnableCorpseCollectionBoundDeterministicWithoutCacheChanges(t *testing.T) {
	s := NewIndependentService(1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	for i := 0; i < 50; i++ {
		guid := npcGUID + uint64(i)
		data := objectFixture(guid, 3, skinnableFields())
		offset := 4 + 1 + len(packedFixture(guid)) + 1 + 2 + 10
		binary.LittleEndian.PutUint32(data[offset:], mathFloatBits(float32(i)))
		packet(t, s, opcode.SMSGUpdateObject, data)
	}
	first := creatureLookup(t, s, 0)
	if len(first.SkinnableCorpses) != 32 || len(first.NearbyCreatures) != 32 || len(s.state.creatures) != 50 {
		t.Fatal("bounded snapshot changed cache", first)
	}
	for i := 1; i < len(first.SkinnableCorpses); i++ {
		a, b := first.SkinnableCorpses[i-1], first.SkinnableCorpses[i]
		if a.DistanceToSelfObserved > b.DistanceToSelfObserved || a.DistanceToSelfObserved == b.DistanceToSelfObserved && a.Guid > b.Guid {
			t.Fatal("nondeterministic nearest ordering", a, b)
		}
	}
	for range 10 {
		if !proto.Equal(first, creatureLookup(t, s, 0)) {
			t.Fatal("read changed discovery/cache/age")
		}
	}
	// Exact lookup remains independent of the new32-result truncation.
	exact := creatureLookup(t, s, npcGUID+49).RequestedCreature
	if exact == nil || !exact.UnitFlagsKnown || !exact.DynamicFlagsKnown || len(s.state.creatures) != 50 {
		t.Fatal(exact)
	}
	now = now.Add(time.Second)
	if got := creatureLookup(t, s, npcGUID+49).RequestedCreature; got.ObservationAgeMs != 1000 || got.PositionRevision != exact.PositionRevision {
		t.Fatal("snapshot invented fresh observation", got)
	}
}

func TestSkinnableCorpseRemovalAndMapTransferRemainAbsent(t *testing.T) {
	for _, name := range []string{"destroy", "out_of_range", "transfer_pending", "new_world"} {
		t.Run(name, func(t *testing.T) {
			s := NewIndependentService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, skinnableFields()))
			switch name {
			case "destroy":
				packet(t, s, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, npcGUID), 0))
			case "out_of_range":
				b := new(bytes.Buffer)
				u32(b, 1)
				b.WriteByte(4)
				u32(b, 1)
				b.Write(packedFixture(npcGUID))
				packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
			case "transfer_pending":
				packet(t, s, opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
			case "new_world":
				packet(t, s, opcode.SMSGNewWorld, login())
			}
			r := creatureLookup(t, s, npcGUID)
			if len(r.SkinnableCorpses) != 0 || r.RequestedCreature != nil {
				t.Fatal("removed observation resurrected", r)
			}
		})
	}
}

func TestSkinnableCorpseDescriptorFieldsGenerated(t *testing.T) {
	data, err := os.ReadFile("module.pb")
	if err != nil {
		t.Fatal(err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err = proto.Unmarshal(data, set); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, file := range set.File {
		for _, message := range file.MessageType {
			for _, field := range message.Field {
				key := message.GetName() + "." + field.GetName()
				want := map[string]int32{"CreatureState.unit_flags_known": 39, "CreatureState.dynamic_flags_known": 40, "SnapshotResponse.skinnable_corpses": 37}
				if n, ok := want[key]; ok {
					if field.GetNumber() != n {
						t.Fatal(key, field)
					}
					found[key] = true
				}
			}
		}
	}
	if len(found) != 3 {
		t.Fatal("stale module descriptor", found)
	}
	if _, err := NewService(1).Snapshot(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}
