package buddyactions

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
)

func flightpathStatusPayload(npc uint64, known byte) []byte {
	return append(binary.LittleEndian.AppendUint64(nil, npc), known)
}

func flightpathMenuPayload(npc uint64, source uint32, nodes ...uint32) []byte {
	payload := make([]byte, 72)
	binary.LittleEndian.PutUint32(payload, 1)
	binary.LittleEndian.PutUint64(payload[4:], npc)
	binary.LittleEndian.PutUint32(payload[12:], source)
	for _, node := range nodes {
		word, bit := (node-1)/32, (node-1)%32
		offset := 16 + word*4
		mask := binary.LittleEndian.Uint32(payload[offset:])
		binary.LittleEndian.PutUint32(payload[offset:], mask|uint32(1)<<bit)
	}
	return payload
}

func TestFlightpathStatusDecodesExactGUIDAndKnownFact(t *testing.T) {
	for _, npc := range []uint64{1, 0xf130000202000001, ^uint64(0)} {
		for _, known := range []byte{0, 1} {
			t.Run(fmt.Sprintf("%x/%d", npc, known), func(t *testing.T) {
				gotNPC, gotKnown, err := decodeFlightpathStatus(flightpathStatusPayload(npc, known))
				if err != nil || gotNPC != npc || gotKnown != (known == 1) {
					t.Fatal("taxi status changed the actual GUID or known fact", gotNPC, gotKnown, err)
				}
			})
		}
	}
}

func TestFlightpathStatusRejectsMalformedPayloadWithoutPartialFacts(t *testing.T) {
	valid := flightpathStatusPayload(0xf130000202000001, 1)
	for length := 0; length < len(valid); length++ {
		t.Run(fmt.Sprintf("truncated/%d", length), func(t *testing.T) {
			npc, known, err := decodeFlightpathStatus(valid[:length])
			if err == nil || npc != 0 || known {
				t.Fatal("truncated taxi status exposed partial facts", npc, known, err)
			}
		})
	}
	for name, payload := range map[string][]byte{
		"trailing":       append(append([]byte(nil), valid...), 0),
		"zero GUID":      flightpathStatusPayload(0, 1),
		"known byte 2":   flightpathStatusPayload(1, 2),
		"known byte 255": flightpathStatusPayload(1, 255),
	} {
		t.Run(name, func(t *testing.T) {
			npc, known, err := decodeFlightpathStatus(payload)
			if err == nil || npc != 0 || known {
				t.Fatal("malformed taxi status exposed partial facts", npc, known, err)
			}
		})
	}
}

func TestFlightpathMenuDecodesAllMaskWordBoundariesInAscendingOrder(t *testing.T) {
	const npc = uint64(0xf130000202000001)
	var nodes []uint32
	for word := uint32(0); word < 14; word++ {
		nodes = append(nodes, word*32+1, word*32+32)
	}
	// Set bits out of order to prove the returned order is the wire mask's
	// node numbering rather than the helper's insertion order.
	var reverse []uint32
	for i := len(nodes) - 1; i >= 0; i-- {
		reverse = append(reverse, nodes[i])
	}
	payload := flightpathMenuPayload(npc, 448, reverse...)
	gotNPC, source, gotNodes, err := decodeFlightpathMenu(payload)
	if err != nil || gotNPC != npc || source != 448 || !reflect.DeepEqual(gotNodes, nodes) {
		t.Fatal("taxi menu shifted mask words, endianness, node IDs or ordering", gotNPC, source, gotNodes, err)
	}
}

func TestFlightpathMenuPreservesEmptyAndSourceAbsentMasks(t *testing.T) {
	for _, source := range []uint32{1, 448} {
		for _, nodes := range [][]uint32{nil, {2, 33, 447}} {
			t.Run(fmt.Sprintf("source=%d/nodes=%v", source, nodes), func(t *testing.T) {
				npc, gotSource, gotNodes, err := decodeFlightpathMenu(flightpathMenuPayload(^uint64(0), source, nodes...))
				if err != nil || npc != ^uint64(0) || gotSource != source || len(gotNodes) != len(nodes) {
					t.Fatal("taxi decoder invented a source-node bit or rejected the observed mask", npc, gotSource, gotNodes, err)
				}
				for i := range nodes {
					if gotNodes[i] != nodes[i] {
						t.Fatal("taxi menu changed a reported node", gotNodes, nodes)
					}
				}
			})
		}
	}
}

func TestFlightpathMenuDecodesFullMaskAndDetachesReturnedFacts(t *testing.T) {
	payload := flightpathMenuPayload(1, 1)
	for offset := 16; offset < len(payload); offset++ {
		payload[offset] = 0xff
	}
	_, _, nodes, err := decodeFlightpathMenu(payload)
	if err != nil || len(nodes) != 448 {
		t.Fatal("full taxi mask did not retain exactly 448 nodes", len(nodes), err)
	}
	for i, node := range nodes {
		if node != uint32(i+1) {
			t.Fatal("full taxi mask omitted or duplicated a node", i, node)
		}
	}
	payload[16] = 0
	if nodes[0] != 1 {
		t.Fatal("decoded taxi facts alias the packet bytes", nodes)
	}
	nodes[0] = 400
	_, _, fresh, err := decodeFlightpathMenu(flightpathMenuPayload(1, 1, 1))
	if err != nil || len(fresh) != 1 || fresh[0] != 1 {
		t.Fatal("caller mutation changed a later decode", fresh, err)
	}
}

func TestFlightpathMenuRejectsMalformedPayloadWithoutPartialFacts(t *testing.T) {
	valid := flightpathMenuPayload(0xf130000202000001, 1, 1, 32, 33, 448)
	assertRejected := func(t *testing.T, payload []byte) {
		t.Helper()
		npc, source, nodes, err := decodeFlightpathMenu(payload)
		if err == nil || npc != 0 || source != 0 || nodes != nil {
			t.Fatal("malformed taxi menu exposed partial facts", npc, source, nodes, err)
		}
	}
	for length := 0; length < len(valid); length++ {
		t.Run(fmt.Sprintf("truncated/%d", length), func(t *testing.T) { assertRejected(t, valid[:length]) })
	}
	t.Run("trailing", func(t *testing.T) { assertRejected(t, append(append([]byte(nil), valid...), 0)) })
	for _, tag := range []uint32{0, 2, 0x100, ^uint32(0)} {
		t.Run(fmt.Sprintf("tag/%d", tag), func(t *testing.T) {
			payload := append([]byte(nil), valid...)
			binary.LittleEndian.PutUint32(payload, tag)
			assertRejected(t, payload)
		})
	}
	t.Run("zero GUID", func(t *testing.T) { assertRejected(t, flightpathMenuPayload(0, 1)) })
	for _, source := range []uint32{0, 449, ^uint32(0)} {
		t.Run(fmt.Sprintf("source/%d", source), func(t *testing.T) { assertRejected(t, flightpathMenuPayload(1, source)) })
	}
}
