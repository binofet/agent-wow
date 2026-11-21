package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	buddyworld "github.com/agent-wow/agent-wow/modules/buddy-world"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A CREATE carries nonzero fields only; VALUES carries its explicit delta.
// In particular these fixtures do not send the three idle channel words.
func sparseChannelUpdate(kind byte, fields map[int]uint32) []byte {
	p := binary.LittleEndian.AppendUint32(nil, 1)
	p = append(p, kind)
	p = append(p, packedGUID(1)...)
	if kind == 3 {
		p = append(p, 4, 0x20, 0) // PLAYER, UPDATEFLAG_LIVING.
		p = append(p, make([]byte, 4+2+4+4*4+4)...)
		for _, speed := range []float32{2.5, 7, 4.5, 4.7, 2.5, 7, 4.5, 3.14, 3.14} {
			p = binary.LittleEndian.AppendUint32(p, math.Float32bits(speed))
		}
	}
	keys := make([]int, 0, len(fields))
	for field := range fields {
		keys = append(keys, field)
	}
	sort.Ints(keys)
	blocks := keys[len(keys)-1]/32 + 1
	p = append(p, byte(blocks))
	for block := 0; block < blocks; block++ {
		var mask uint32
		for _, field := range keys {
			if field/32 == block {
				mask |= 1 << uint(field%32)
			}
		}
		p = binary.LittleEndian.AppendUint32(p, mask)
	}
	for _, field := range keys {
		p = binary.LittleEndian.AppendUint32(p, fields[field])
	}
	return p
}

func sparseChannelFields() map[int]uint32 {
	return map[int]uint32{0x17: 1 | 4<<8 | 3<<24, 0x18: 100, 0x20: 100,
		0x1c: 100, 0x24: 100, 0x3b: 8, 0x7a: 30 << 24}
}

func rawChannelSnapshot(t *testing.T, world *buddyworld.Service, payload []byte) *worldv1.SnapshotResponse {
	t.Helper()
	if payload != nil {
		if _, err := world.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGUpdateObject, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := world.Snapshot(context.Background(), nil)
	if err != nil || out.MalformedPackets != 0 {
		t.Fatal("invalid raw channel fixture", out, err)
	}
	return out
}

func sapWithRawChannel(t *testing.T, world *buddyworld.Service, payload []byte) (*Service, *fakeSession, *actionsv1.SapRequest) {
	t.Helper()
	s, f, req := policySapFixture(t)
	weapon := f.world.Self.Mainhand // Orthogonal item-query proof from the Sap fixture.
	f.world.Self = rawChannelSnapshot(t, world, payload).Self
	f.world.Self.Mainhand = weapon
	return s, f, req
}

func TestChannelSparseCreateAllowsExactSapWithoutExplicitZeroWords(t *testing.T) {
	world := buddyworld.NewService(1)
	world.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLoginVerifyWorld, Payload: make([]byte, 20)})
	s, f, req := sapWithRawChannel(t, world, sparseChannelUpdate(3, sparseChannelFields()))
	self := f.world.Self
	if !self.ChannelSpellKnown || !self.ChannelObjectKnown || self.ChannelSpellId != 0 || self.ChannelObjectGuid != 0 {
		t.Fatal("complete own CREATE did not baseline omitted channel words", self)
	}
	obs := &actionsv1.EncounterObservationResponse{Spells: map[string]*actionsv1.EncounterSpell{}}
	s.encounterSpells(f.world, nil, obs)
	if !obs.Spells["6770"].GetReady() {
		t.Fatal("sparse CREATE made exact Sap unavailable to Lua", obs.Spells["6770"])
	}
	st, err := s.Sap(policySapContext(strings.Repeat("a", 64)), req)
	if err != nil || !st.GetLastSpellOperation().GetTransportWritten() || lastCast(f) != 6770 {
		t.Fatal("sparse CREATE blocked selected Sap", st, err, f.packets)
	}
}

func TestChannelPartialOrActiveObservationsCannotAdmitExactSap(t *testing.T) {
	for _, scenario := range []string{"login_only", "partial_values", "partial_object", "spell", "object", "pending", "control_lost", "motion_unconfirmed"} {
		t.Run(scenario, func(t *testing.T) {
			world := buddyworld.NewService(1)
			world.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLoginVerifyWorld, Payload: make([]byte, 20)})
			payload := sparseChannelUpdate(3, sparseChannelFields())
			if scenario == "login_only" {
				payload = nil
			} else if scenario == "partial_values" || scenario == "partial_object" {
				fields := sparseChannelFields()
				fields[0x14], fields[0x16], fields[0x96] = 0, 0, 0 // High object word remains unobserved.
				payload = sparseChannelUpdate(0, fields)
			}
			s, f, req := sapWithRawChannel(t, world, payload)
			switch scenario {
			case "partial_values":
				// No CREATE/movement baseline; VALUES cannot prove standing.
			case "partial_object":
				// A real stationary heartbeat can supply position/movement; it
				// still cannot manufacture the absent channel object's high word.
				movement := append(packedGUID(1), make([]byte, 4+2+4+4*4+4)...)
				world.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.MSGMoveHeartbeat, Payload: movement})
				weapon := f.world.Self.Mainhand
				fresh, _ := world.Snapshot(context.Background(), nil)
				f.world.Self = fresh.Self
				f.world.Self.Mainhand = weapon
				if !f.world.Self.ChannelSpellKnown || f.world.Self.ChannelObjectKnown || safeSelfCombat(f.world, 1) != "" {
					t.Fatal("fixture did not isolate partial channel observation", f.world.Self)
				}
			case "spell", "object":
				field := 0x16
				if scenario == "object" {
					field = 0x14
				}
				weapon := f.world.Self.Mainhand
				f.world.Self = rawChannelSnapshot(t, world, sparseChannelUpdate(0, map[int]uint32{field: 7620})).Self
				f.world.Self.Mainhand = weapon
			case "pending":
				s.state.CastPending = true
			case "control_lost":
				s.clientControlLost = true
			case "motion_unconfirmed":
				s.state.MotionUnconfirmed = true
			}
			obs := &actionsv1.EncounterObservationResponse{Spells: map[string]*actionsv1.EncounterSpell{}}
			s.encounterSpells(f.world, nil, obs)
			if obs.Spells["6770"].GetReady() {
				t.Fatal("reflected Sap readiness differs from channel/cast admission", obs.Spells["6770"])
			}
			_, err := s.Sap(policySapContext(strings.Repeat("b", 64)), req)
			if status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
				t.Fatal("unavailable, partial, active or unresolved caster wrote Sap", err, f.packets)
			}
		})
	}
}
