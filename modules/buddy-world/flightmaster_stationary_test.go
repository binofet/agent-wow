package buddyworld

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestFlightmasterStationarityRequiresAnActualCurrentWorldSpaceImage(t *testing.T) {
	s := NewIndependentService(1)
	now := time.Unix(1234, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	spawn := objectFixture(npcGUID, 3, npcFields(0x2000))
	packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, spawn))
	now = now.Add(105 * time.Second)
	atRest := creatureLookup(t, s, npcGUID)
	static := atRest.RequestedCreature
	if !atRest.RequestedCreatureStationaryPositionKnown || static.PositionAgeMs != 105000 || static.ObservationAgeMs != 105000 {
		t.Fatal("living CREATE did not retain its actual standing coordinates and ages", static)
	}
	check := func(want bool) {
		t.Helper()
		if c := creatureLookup(t, s, npcGUID); c.RequestedCreatureStationaryPositionKnown != want {
			t.Fatal(c)
		}
	}
	start, stop := splineLiteral(t, monsterLinearGolden), splineLiteral(t, monsterStopGolden)
	packet(t, s, opcode.SMSGMonsterMove, start)
	check(false)
	packet(t, s, opcode.SMSGMonsterMove, stop)
	check(true)
	packet(t, s, opcode.SMSGMonsterMove, append(packedFixture(npcGUID+1), 0))
	check(true)
	packet(t, s, opcode.SMSGMonsterMove, append(packedFixture(npcGUID), 0))
	check(false)
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{unitHealth: 99}))
	check(false)
	// UPDATEFLAG_STATIONARY_POSITION is also positive. UPDATEFLAG_POSITION
	// without motion flags carries coordinates only, even with no transport.
	for _, format := range []uint16{0x40, 0x100} {
		b := new(bytes.Buffer)
		u32(b, 1)
		b.WriteByte(1)
		b.Write(packedFixture(npcGUID))
		b.Write(binary.LittleEndian.AppendUint16(nil, format))
		if format == 0x100 {
			b.WriteByte(0)
		}
		for _, coord := range []float32{10, 20, 30} {
			f32(b, coord)
		}
		if format == 0x100 {
			b.Write(make([]byte, 12))
		}
		f32(b, 0)
		if format == 0x100 {
			f32(b, 0)
		}
		packet(t, s, opcode.SMSGUpdateObject, b.Bytes())
		check(format == 0x40)
	}
	packet(t, s, opcode.SMSGUpdateObject, spawn)
	check(true)
	packet(t, s, opcode.SMSGUpdateObject, []byte{1, 0})
	check(false)
	packet(t, s, opcode.SMSGUpdateObject, spawn)
	packet(t, s, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, npcGUID), 0))
	check(false)
}
