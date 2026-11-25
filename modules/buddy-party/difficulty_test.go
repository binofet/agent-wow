package buddyparty

import (
	"context"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestGroupDifficultyKnownZeroRawValuesAndAbsentTail(t *testing.T) {
	for _, values := range [][2]byte{{0, 0}, {1, 3}, {255, 254}} {
		payload := unhex(t, duoHex)
		payload[len(payload)-3], payload[len(payload)-2] = values[0], values[1]
		group, err := decodeGroup(payload, 2, "Arlen")
		if err != nil || !group.GetDungeonDifficultyKnown() || !group.GetRaidDifficultyKnown() || group.GetDungeonDifficulty() != uint32(values[0]) || group.GetRaidDifficulty() != uint32(values[1]) {
			t.Fatalf("difficulty lost: %v %v", group, err)
		}
	}
	group, err := decodeGroup(unhex(t, leftHex), 2, "Arlen")
	if err != nil || group.GetDungeonDifficultyKnown() || group.GetRaidDifficultyKnown() {
		t.Fatalf("leave invented difficulty: %v %v", group, err)
	}
	// A valid one-member roster has no tail, so zero must remain unknown.
	payload := unhex(t, duoHex)
	payload = append(append(append([]byte{}, payload[:16]...), 0, 0, 0, 0), 2, 0, 0, 0, 0, 0, 0, 0)
	group, err = decodeGroup(payload, 2, "Arlen")
	if err != nil || !group.GetInGroup() || group.GetDungeonDifficultyKnown() || group.GetRaidDifficultyKnown() {
		t.Fatalf("count-zero group: %v %v", group, err)
	}
}

func TestDifficultyPublicCloneReflectionMalformedPreservesGroup(t *testing.T) {
	s := NewService(nil, "test", 2, "Arlen")
	payload := unhex(t, duoHex)
	payload[len(payload)-3], payload[len(payload)-2] = 0, 3
	deliver(t, s, opcode.SMSGGroupList, payload)
	before := state(t, s)
	if !before.GetGroup().GetDungeonDifficultyKnown() || before.GetGroup().GetDungeonDifficulty() != 0 || before.GetGroup().GetRaidDifficulty() != 3 {
		t.Fatal("public state lost known zero/raw difficulty", before)
	}
	for name, number := range map[protoreflect.Name]protoreflect.FieldNumber{"dungeon_difficulty_known": 8, "dungeon_difficulty": 9, "raid_difficulty_known": 10, "raid_difficulty": 11} {
		field := before.Group.ProtoReflect().Descriptor().Fields().ByName(name)
		if field == nil || field.Number() != number {
			t.Fatal("missing backward-compatible reflection", name)
		}
	}
	saved := proto.Clone(before.Group)
	before.Group.RaidDifficulty = 99
	if state(t, s).Group.GetRaidDifficulty() != 3 {
		t.Fatal("State clone leaked")
	}
	for cut := 1; cut <= 13; cut++ {
		deliver(t, s, opcode.SMSGGroupList, payload[:len(payload)-cut])
		if !proto.Equal(saved, state(t, s).Group) {
			t.Fatal("malformed tail mutated valid state", cut)
		}
	}
	_, _ = s.State(context.Background(), nil)
	deliver(t, s, opcode.SMSGGroupDestroyed, nil)
	if group := state(t, s).Group; group.GetDungeonDifficultyKnown() || group.GetRaidDifficultyKnown() {
		t.Fatal("destroy retained difficulty", group)
	}
}
