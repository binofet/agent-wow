package spellcast

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestCastSpellPrimaryWireVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/wrath-cast-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Vectors []struct {
			Name       string
			CastCount  byte
			SpellId    uint32
			TargetMask uint32
			Hex        string
			Bytes      int
		}
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, v := range doc.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			r := &actionsv1.CastSpellRequest{SelfGuid: 1, SpellId: v.SpellId, Targets: &actionsv1.SpellCastTargets{Mask: v.TargetMask}}
			target := r.Targets
			switch v.Name {
			case "flamestrike_ground_1_2_3", "blizzard_ground_1_2_3":
				target.Destination = &actionsv1.SpellCastLocation{X: 1, Y: 2, Z: 3}
			case "source_then_destination":
				target.Source = &actionsv1.SpellCastLocation{X: 1, Y: 2, Z: 3}
				target.Destination = &actionsv1.SpellCastLocation{X: 4, Y: 5, Z: 6}
			case "unit_guid_f130000063000001_wire_only":
				target.ObjectGuid = 0xf130000063000001
			case "gameobject_guid_f110000063000001_wire_only":
				target.ObjectGuid = 0xf110000063000001
			case "item_guid_4000000000000099_wire_only":
				target.ItemGuid = 0x4000000000000099
			case "trade_nontraded_slot_6_wire_only":
				target.ItemGuid = 6
			case "string_utf8_hi_wire_only":
				target.StringTarget = "hi"
			case "transport_relative_destination_guid1_wire_only":
				target.Destination = &actionsv1.SpellCastLocation{TransportGuid: 1, X: 1, Y: 2, Z: 3}
			case "arcane_explosion_implicit":
			default:
				r.Trajectory = &actionsv1.SpellCastTrajectory{Elevation: .5, Speed: 20}
			}
			got, err := Encode(v.CastCount, r)
			want, e := hex.DecodeString(strings.ReplaceAll(v.Hex, " ", ""))
			if err != nil || e != nil || !bytes.Equal(got, want) || len(got) != v.Bytes {
				t.Fatalf("packet %x want %x: %v/%v", got, want, err, e)
			}
		})
	}
}

func TestCastSpellComposableTargetsAndOwnMovement(t *testing.T) {
	r := &actionsv1.CastSpellRequest{SelfGuid: 1, SpellId: 4000000000, Targets: &actionsv1.SpellCastTargets{Mask: ObjectMask | 0x10 | 0x20 | 0x40 | 0x2000, ObjectGuid: 3, ItemGuid: 0x4000000000000042, Source: &actionsv1.SpellCastLocation{X: 1}, Destination: &actionsv1.SpellCastLocation{Z: 9}, StringTarget: "any learned spell"}}
	p, err := Encode(29, r)
	if err != nil {
		t.Fatal(err)
	}
	n, err := TargetPrefixLen(p[6:])
	if err != nil || n != len(p)-6 {
		t.Fatal(n, err, p)
	}
	// A composed object union still consumes ONE GUID (03), independently of item.
	if p[10] != 1 || p[11] != 3 {
		t.Fatal(p)
	}
	move := []byte{1, 1}
	move = binary.LittleEndian.AppendUint32(move, 0)
	move = append(move, 0, 0)
	move = binary.LittleEndian.AppendUint32(move, 50)
	for range 4 {
		move = binary.LittleEndian.AppendUint32(move, 0)
	}
	move = binary.LittleEndian.AppendUint32(move, 0)
	r.Trajectory = &actionsv1.SpellCastTrajectory{MovementOpcode: opcode.MSGMoveHeartbeat, Movement: move}
	if _, err = Encode(30, r); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*actionsv1.CastSpellRequest){
		func(q *actionsv1.CastSpellRequest) { q.Targets.Destination.X = float32(math.NaN()) },
		func(q *actionsv1.CastSpellRequest) { q.Targets.StringTarget = "bad\x00name" },
		func(q *actionsv1.CastSpellRequest) { q.Trajectory.Movement[1] = 3 },
		func(q *actionsv1.CastSpellRequest) { q.Trajectory.MovementOpcode = opcode.CMSGSetSelection },
		func(q *actionsv1.CastSpellRequest) { q.Trajectory.Movement = append(q.Trajectory.Movement, 0) },
		func(q *actionsv1.CastSpellRequest) { q.Targets.Mask = 0 },
	} {
		q := proto.Clone(r).(*actionsv1.CastSpellRequest)
		mutate(q)
		if _, err = Encode(30, q); err == nil {
			t.Fatal("malformed DTO accepted", q)
		}
	}
}
