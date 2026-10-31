package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const ownCorpseGUID uint64 = 0xf10100000000002a

// Object::BuildMovementUpdate: POSITION0x100 + STATIONARY0x40 + LOWGUID0x10.
// No transport, actualXYZ and repeatedXYZ, orientation and corpse orientation,
// lowGUID, then ordinary update-mask fields. This is not living movement.
func corpseFixture(guid, owner uint64, flags uint32) []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	b.WriteByte(3)
	b.Write(packedFixture(guid))
	b.WriteByte(7)
	b.Write([]byte{0x50, 1, 0})
	for _, v := range []float32{1, 2, 3, 1, 2, 3, .25, .25} {
		f32(b, v)
	}
	u32(b, uint32(guid))
	fields := map[int]uint32{}
	if uint32(owner) != 0 {
		fields[6] = uint32(owner)
	}
	if uint32(owner>>32) != 0 {
		fields[7] = uint32(owner >> 32)
	}
	if flags != 0 {
		fields[0x21] = flags
	}
	valuesFixture(b, fields)
	return b.Bytes()
}

func TestOwnCorpseLiteralStationaryCreateAndCompressed(t *testing.T) {
	golden, err := hex.DecodeString("0100000003c12a01f1075001000000803f00000040000040400000803f00000040000040400000803e0000803e2a0000000240000000020000000100000008000000")
	if err != nil {
		t.Fatal(err)
	}
	// Independently literal-framed golden and reusable fixture must agree.
	if !bytes.Equal(golden, corpseFixture(ownCorpseGUID, 1, 8)) {
		t.Fatalf("corpse framing changed: %x", corpseFixture(ownCorpseGUID, 1, 8))
	}
	for _, zip := range []bool{false, true} {
		s := NewService(1)
		now := time.Unix(1000, 0)
		s.now = func() time.Time { return now }
		packet(t, s, opcode.SMSGLoginVerifyWorld, login())
		code, data := uint32(opcode.SMSGUpdateObject), golden
		if zip {
			code, data = opcode.SMSGCompressedUpdateObject, compressed(t, golden)
		}
		r := packet(t, s, code, data)
		c := r.OwnCorpse
		if c == nil || c.Guid != ownCorpseGUID || c.OwnerGuid != 1 || !c.Present || !c.MapKnown || c.MapId != 0 || !c.PositionKnown || c.X != 1 || c.Y != 2 || c.Z != 3 || c.Orientation != .25 || c.PositionRevision != 1 || !c.FlagsKnown || c.Flags != 8 || !c.DynamicFlagsKnown || c.DynamicFlags != 0 {
			t.Fatal(c, r.LastParseError)
		}
		now = now.Add(3 * time.Second)
		r, _ = s.Snapshot(context.Background(), nil)
		if r.OwnCorpse.PositionAgeMs != 3000 || r.OwnCorpse.ObservationAgeMs != 3000 {
			t.Fatal(r.OwnCorpse)
		}
		r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(ownCorpseGUID, map[int]uint32{0x22: 1}))
		if r.OwnCorpse.DynamicFlags != 1 || r.OwnCorpse.PositionRevision != 1 || r.OwnCorpse.ObservationAgeMs != 0 || r.OwnCorpse.PositionAgeMs != 3000 {
			t.Fatal(r.OwnCorpse)
		}
	}
}

func TestOwnCorpseRequiresActualTypeOwnerAndUnambiguousNonBones(t *testing.T) {
	unknown := NewIndependentService(0)
	packet(t, unknown, opcode.SMSGLoginVerifyWorld, login())
	if r := packet(t, unknown, opcode.SMSGUpdateObject, corpseFixture(ownCorpseGUID, 0, 0)); r.OwnCorpse != nil {
		t.Fatal("zero owner was treated as an own identity", r.OwnCorpse)
	}
	s := NewIndependentService(6)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	for _, data := range [][]byte{
		deltaFixture(ownCorpseGUID, map[int]uint32{6: 6, 0x21: 0}),
		corpseFixture(ownCorpseGUID, 1, 0),
		corpseFixture(ownCorpseGUID+1, 0, 0),
		corpseFixture(ownCorpseGUID+2, 6, 1),
		corpseFixture(0xf13000000000002a, 6, 0),
	} {
		if r := packet(t, s, opcode.SMSGUpdateObject, data); r.OwnCorpse != nil {
			t.Fatal("foreign/bones/unadmitted corpse became own", r.OwnCorpse)
		}
	}
	r := packet(t, s, opcode.SMSGUpdateObject, corpseFixture(ownCorpseGUID+3, 6, 0))
	if r.OwnCorpse == nil || r.OwnCorpse.OwnerGuid != 6 || r.Owner != nil {
		t.Fatal(r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, corpseFixture(ownCorpseGUID+4, 6, 0))
	if r.OwnCorpse != nil {
		t.Fatal("two actual own bodies selected arbitraryGUID", r.OwnCorpse)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(ownCorpseGUID+4, map[int]uint32{0x21: 1}))
	if r.OwnCorpse == nil || r.OwnCorpse.Guid != ownCorpseGUID+3 {
		t.Fatal("actual bones flag did not disambiguate", r.OwnCorpse)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(ownCorpseGUID+3, map[int]uint32{6: 1}))
	if r.OwnCorpse != nil {
		t.Fatal("owner delta kept foreign authorization")
	}
}

func TestPlayerCorpseExportRetainsActualOwnerAndExcludesBones(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	r := packet(t, s, opcode.SMSGUpdateObject, corpseFixture(ownCorpseGUID, 4, 0))
	if len(r.PlayerCorpses) != 1 || r.PlayerCorpses[0].OwnerGuid != 4 || r.OwnCorpse != nil {
		t.Fatal("foreign party corpse was guessed or hidden", r.PlayerCorpses)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(ownCorpseGUID, map[int]uint32{0x21: 1}))
	if len(r.PlayerCorpses) != 0 {
		t.Fatal("bones exported as resurrectable corpse")
	}
}

func TestOwnCorpseMalformedWholePacketRollsBack(t *testing.T) {
	s := NewService(1)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	before := packet(t, s, opcode.SMSGUpdateObject, corpseFixture(ownCorpseGUID, 1, 0)).OwnCorpse
	change := deltaFixture(ownCorpseGUID, map[int]uint32{6: 3, 0x21: 1})
	for n := 0; n < len(change); n++ {
		packet(t, s, opcode.SMSGUpdateObject, change[:n])
	}
	packet(t, s, opcode.SMSGUpdateObject, append(append([]byte(nil), change...), 255))
	bad := append(append([]byte(nil), change...), 255)
	binary.LittleEndian.PutUint32(bad, 2)
	r := packet(t, s, opcode.SMSGUpdateObject, bad)
	if !proto.Equal(before, r.OwnCorpse) {
		t.Fatal("partial corpse field mutation escaped", r.OwnCorpse)
	}
	create := corpseFixture(ownCorpseGUID+1, 1, 0)
	for n := 0; n < len(create); n++ {
		packet(t, s, opcode.SMSGUpdateObject, create[:n])
	}
	if len(s.state.corpses) != 1 {
		t.Fatal("partial CREATE admitted corpse")
	}
	// Wrong type for a previously admitted corpse is locally rejected.
	r = packet(t, s, opcode.SMSGUpdateObject, objectFixture(ownCorpseGUID, 3, npcFields(2)))
	if !proto.Equal(before, r.OwnCorpse) || r.MalformedPackets == 0 {
		t.Fatal(r)
	}
}

func TestOwnCorpseLifecycleClearsUntilActualCreate(t *testing.T) {
	for _, mode := range []string{"destroy", "out_of_range", "transfer", "new_world", "self_destroy"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(1)
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			packet(t, s, opcode.SMSGUpdateObject, corpseFixture(ownCorpseGUID, 1, 0))
			switch mode {
			case "destroy", "self_destroy":
				guid := ownCorpseGUID
				if mode == "self_destroy" {
					guid = 1
				}
				b := make([]byte, 9)
				binary.LittleEndian.PutUint64(b, guid)
				packet(t, s, opcode.SMSGDestroyObject, b)
			case "out_of_range":
				packet(t, s, opcode.SMSGUpdateObject, append([]byte{1, 0, 0, 0, 4, 1, 0, 0, 0}, packedFixture(ownCorpseGUID)...))
			case "transfer":
				packet(t, s, opcode.SMSGTransferPending, []byte{1, 0, 0, 0})
			case "new_world":
				packet(t, s, opcode.SMSGNewWorld, login())
			}
			r, _ := s.Snapshot(context.Background(), nil)
			if r.OwnCorpse != nil || len(s.state.corpses) != 0 {
				t.Fatal("stalecorpse survived lifecycle", r.OwnCorpse)
			}
			packet(t, s, opcode.SMSGLoginVerifyWorld, login())
			r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(ownCorpseGUID, map[int]uint32{6: 1}))
			if r.OwnCorpse != nil {
				t.Fatal("delta afterabsence invented corpse")
			}
			r = packet(t, s, opcode.SMSGUpdateObject, corpseFixture(ownCorpseGUID, 1, 0))
			if r.OwnCorpse == nil {
				t.Fatal("freshCREATE didnot restore actualcorpse")
			}
		})
	}
}

func TestOwnCorpseBoundProtectsOwnAndCopiesSnapshots(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, corpseFixture(ownCorpseGUID, 1, 0))
	for i := uint64(100); i < 100+maxCorpses+10; i++ {
		packet(t, s, opcode.SMSGUpdateObject, corpseFixture(0xf101000000000000|i, 3, 0))
	}
	if len(s.state.corpses) != maxCorpses || s.state.corpses[ownCorpseGUID] == nil {
		t.Fatal("unboundedcorpsecache/own evicted")
	}
	r, _ := s.Snapshot(context.Background(), nil)
	r.OwnCorpse.Guid = 999
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				r, _ := s.Snapshot(context.Background(), nil)
				if r.OwnCorpse == nil || r.OwnCorpse.Guid != ownCorpseGUID {
					t.Error("snapshot aliases corpse", r.OwnCorpse)
					return
				}
				r.OwnCorpse.X = 999
			}
		}()
	}
	wg.Wait()
}
