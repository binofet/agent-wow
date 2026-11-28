package buddynav

import (
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
)

func TestIndependentStrictOrdinaryTransportGUID(t *testing.T) {
	for _, guid := range []uint64{0x1fc0000000000123, 0x1fc0000000000000, 0x1fc0123400000123} {
		wire, err := movementinfo.MoverFrame(99, movementinfo.Info{Flags: 0x201, TransportPresent: true, Transport: movementinfo.Transport{GUID: guid, X: 1, Y: 2, Z: 3, Time: 124, Seat: -1}})
		if err != nil {
			t.Fatal(err)
		}
		allowed := RouteBodyFrame(opcode.MSGMoveStartForward, wire, 99)
		if want := guid == 0x1fc0000000000123; allowed != want {
			t.Errorf("hull %016x route-admitted=%v, require genuine nonzero transport counter and exact high32", guid, allowed)
		}
	}
}
