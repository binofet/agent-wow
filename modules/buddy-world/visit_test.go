package buddyworld

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestSnapshotVisitRevisionTracksValidWorldBoundaries(t *testing.T) {
	s := NewService(1)
	initial, err := s.Snapshot(context.Background(), nil)
	if err != nil || initial.GetVisitRevision() != 0 {
		t.Fatalf("initial snapshot: visit %d, error %v", initial.GetVisitRevision(), err)
	}

	map389 := login()
	binary.LittleEndian.PutUint32(map389, 389)
	badPosition := append([]byte(nil), map389...)
	binary.LittleEndian.PutUint32(badPosition[4:], math.Float32bits(float32(math.NaN())))
	transfer := binary.LittleEndian.AppendUint32(nil, 389)
	for _, step := range []struct {
		name      string
		op        uint32
		body      []byte
		visit     uint64
		malformed uint64
		owner     bool
	}{
		{"own CREATE before verification", opcode.SMSGUpdateObject, createFixture(1, vitalFields()), 0, 0, false},
		{"short verification", opcode.SMSGLoginVerifyWorld, map389[:19], 0, 1, false},
		{"login verification", opcode.SMSGLoginVerifyWorld, map389, 1, 1, false},
		{"ordinary CREATE", opcode.SMSGUpdateObject, createFixture(3, vitalFields()), 1, 1, true},
		{"new world invalid pose", opcode.SMSGNewWorld, badPosition, 1, 2, true},
		{"same map login verification", opcode.SMSGLoginVerifyWorld, map389, 2, 2, false},
		{"short transfer", opcode.SMSGTransferPending, transfer[:3], 2, 3, false},
		{"same map new world without pending", opcode.SMSGNewWorld, map389, 3, 3, false},
		{"transfer pending", opcode.SMSGTransferPending, transfer, 0, 3, false},
		{"own CREATE during transfer", opcode.SMSGUpdateObject, createFixture(1, vitalFields()), 0, 3, false},
		{"new world with trailing byte", opcode.SMSGNewWorld, append(append([]byte(nil), map389...), 0), 0, 4, false},
		{"same map transfer completes", opcode.SMSGNewWorld, map389, 4, 4, false},
		{"own object disappears", opcode.SMSGDestroyObject, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0}, 0, 4, false},
		{"new world map zero", opcode.SMSGNewWorld, login(), 5, 4, false},
	} {
		r := packet(t, s, step.op, step.body)
		if r.GetVisitRevision() != step.visit || r.GetMalformedPackets() != step.malformed || r.GetOwner().GetPresent() != step.owner {
			t.Fatalf("%s: visit=%d malformed=%d owner=%t, want %d/%d/%t (parse error %q)", step.name, r.GetVisitRevision(), r.GetMalformedPackets(), r.GetOwner().GetPresent(), step.visit, step.malformed, step.owner, r.GetLastParseError())
		}
	}
}
