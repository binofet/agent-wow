package buddyworld

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestOwnXPCreateZeroPartialDeltaAndLevelProgress(t *testing.T) {
	s := NewService(1)
	r := packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	if r.SelfXpKnown || r.SelfNextLevelXpKnown || r.SelfXpRevision != 0 {
		t.Fatal("login invented XP", r)
	}
	fields := vitalFields()
	fields[0x36] = 1
	fields[playerNextLevelXP] = 400
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
	if !r.SelfXpKnown || r.SelfXp != 0 || !r.SelfNextLevelXpKnown || r.SelfNextLevelXp != 400 || r.SelfXpRevision != 1 {
		t.Fatal("own CREATE zero baseline missing", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
	if r.SelfXpRevision != 1 {
		t.Fatal("same CREATE invented progress")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{playerXP: 50}))
	if r.SelfXp != 50 || r.SelfNextLevelXp != 400 || r.SelfXpRevision != 2 {
		t.Fatal("partial XP destroyed threshold", r)
	}
	r = packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, deltaFixture(1, map[int]uint32{playerXP: 50, unitHealth: 50})))
	if r.SelfXpRevision != 2 {
		t.Fatal("unrelated/repeated update invented XP progress")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x36: 2, playerXP: 0, playerNextLevelXP: 900}))
	if !r.Self.LevelKnown || r.Self.Level != 2 || !r.SelfXpKnown || r.SelfXp != 0 || !r.SelfNextLevelXpKnown || r.SelfNextLevelXp != 900 || r.SelfXpRevision != 3 {
		t.Fatal("actual level-up fields not retained", r)
	}
	raw, e := protojson.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	var wire map[string]any
	json.Unmarshal(raw, &wire)
	if wire["selfXpKnown"] != true {
		t.Fatal("known zero disappeared in protobuf JSON", wire)
	}
	if _, present := wire["selfXp"]; present {
		t.Fatal("fixture should exercise omitted proto3 zero")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x36: 80, playerXP: 0, playerNextLevelXP: 0}))
	if !r.SelfXpKnown || !r.SelfNextLevelXpKnown || r.SelfNextLevelXp != 0 {
		t.Fatal("observed zero threshold was treated as unknown")
	}
}
func TestOwnXPUnobservedPartialAndLevelOnlyInvalidation(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x27a: 0}))
	if !r.SelfXpKnown || r.SelfXp != 0 || r.SelfNextLevelXpKnown {
		t.Fatal("one observed scalar implied another", r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x27b: 400}))
	if !r.SelfXpKnown || !r.SelfNextLevelXpKnown {
		t.Fatal("partial scalar was forgotten")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x36: 1}))
	if r.SelfXpKnown || r.SelfNextLevelXpKnown {
		t.Fatal("first level observation reused unbound XP")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{playerXP: 25, playerNextLevelXP: 400}))
	before := r.SelfXpRevision
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x36: 1, unitHealth: 60}))
	if !r.SelfXpKnown || !r.SelfNextLevelXpKnown || r.SelfXpRevision != before {
		t.Fatal("same level invalidated XP")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x36: 2}))
	if r.SelfXpKnown || r.SelfNextLevelXpKnown {
		t.Fatal("old-level XP presented as current")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{playerXP: 5}))
	if !r.SelfXpKnown || r.SelfXp != 5 || r.SelfNextLevelXpKnown {
		t.Fatal("new partial XP invented threshold")
	}
}
func TestOwnXPVisitLossForeignUpdatesAndMalformedRollback(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	fields := vitalFields()
	fields[0x36] = 1
	fields[playerXP] = 125
	fields[playerNextLevelXP] = 400
	before := packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, fields))
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{playerXP: 999, playerNextLevelXP: 999}))
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(2, map[int]uint32{playerXP: 999}))
	bad := deltaFixture(1, map[int]uint32{playerXP: 200, playerNextLevelXP: 500})
	for n := 0; n < len(bad); n++ {
		packet(t, s, opcode.SMSGUpdateObject, bad[:n])
	}
	packet(t, s, opcode.SMSGUpdateObject, append(bad, 255))
	r, _ := s.Snapshot(context.Background(), nil)
	if !r.SelfXpKnown || !r.SelfNextLevelXpKnown || r.SelfXp != 125 || r.SelfNextLevelXp != 400 || r.SelfXpRevision != before.SelfXpRevision {
		t.Fatal("foreign/malformed packet changed XP", r)
	}
	r = packet(t, s, opcode.SMSGNewWorld, login())
	if r.SelfXpKnown || r.SelfNextLevelXpKnown {
		t.Fatal("new visit reused old XP")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
	if !r.SelfXpKnown || r.SelfXp != 125 {
		t.Fatal("new own CREATE did not restore facts")
	}
	r = packet(t, s, opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, 1))
	if r.SelfXpKnown || r.SelfNextLevelXpKnown {
		t.Fatal("transfer reused old XP")
	}
}
func TestOwnXPReflectedAPIIsAdditiveAndOwnOnly(t *testing.T) {
	r, _ := NewService(1).Snapshot(context.Background(), nil)
	fields := r.ProtoReflect().Descriptor().Fields()
	for _, item := range []struct {
		name   protoreflect.Name
		number protoreflect.FieldNumber
		kind   protoreflect.Kind
	}{
		{"self_xp_known", 56, protoreflect.BoolKind}, {"self_xp", 57, protoreflect.Uint32Kind}, {"self_next_level_xp_known", 58, protoreflect.BoolKind}, {"self_next_level_xp", 59, protoreflect.Uint32Kind}, {"self_xp_revision", 60, protoreflect.Uint64Kind},
	} {
		f := fields.ByName(item.name)
		if f == nil || f.Number() != item.number || f.Kind() != item.kind {
			t.Fatal("wrong additive XP ABI", item)
		}
	}
	if f := r.Self.ProtoReflect().Descriptor().Fields().ByName("xp"); f != nil {
		t.Fatal("private XP leaked into shared nearby/owner PlayerState")
	}
}
