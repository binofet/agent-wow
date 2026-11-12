package buddyparty

import (
	"bytes"
	"context"
	"testing"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const moonDeltaHex = "0003000000000000000478563412000030f1"
const raidListHex = "01030a000000000030f10478563412000030f1060b000000000030f1070c000000000030f1"

func TestRaidTargetsServerListAndDeltas(t *testing.T) {
	recorder := &packetRecorder{}
	s := NewService(recorder, "body-one", 2, "Arlen")
	moon := unhex(t, moonDeltaHex)
	deliver(t, s, opcode.MSGRaidTargetUpdate, moon)
	if got := state(t, s); got.RaidTargets != nil || got.IgnoredPackets != 1 {
		t.Fatal("marker packet without observed group gained authority", got)
	}
	deliver(t, s, opcode.SMSGGroupList, unhex(t, duoHex))
	deliver(t, s, opcode.MSGRaidTargetUpdate, moon)
	partial := state(t, s).RaidTargets
	if partial == nil || partial.GroupGuid != groupGUID || partial.Complete || len(partial.Marks) != 1 ||
		partial.Marks[0].Icon != 5 || partial.Marks[0].TargetGuid != 0xf130000012345678 || partial.Marks[0].SetterGuid != 3 {
		t.Fatal("mode-0 moon delta mismatch", partial)
	}
	deliver(t, s, opcode.MSGRaidTargetUpdate, unhex(t, raidListHex))
	complete := state(t, s).RaidTargets
	if complete == nil || !complete.Complete || complete.Revision <= partial.Revision || complete.ListRevision != complete.Revision || len(complete.Marks) != 4 ||
		complete.Marks[0].Icon != 4 || complete.Marks[1].Icon != 5 || complete.Marks[2].Icon != 7 || complete.Marks[3].Icon != 8 ||
		complete.Marks[1].SetterGuid != 0 {
		t.Fatal("mode-1 triangle/moon/cross/skull list mismatch", complete)
	}
	clear := unhex(t, "000000000000000000040000000000000000")
	deliver(t, s, opcode.MSGRaidTargetUpdate, clear)
	cleared := state(t, s).RaidTargets
	if !cleared.Complete || cleared.ListRevision != complete.ListRevision || cleared.Revision <= complete.Revision || len(cleared.Marks) != 3 || cleared.Marks[1].Icon != 7 {
		t.Fatal("zero target did not clear just its own slot", cleared)
	}
	// A group counter refresh with the same roster must not erase marks.
	same := unhex(t, duoHex)
	same[12]++
	deliver(t, s, opcode.SMSGGroupList, same)
	if !proto.Equal(cleared, state(t, s).RaidTargets) {
		t.Fatal("unchanged roster dropped its marker observations")
	}
	// Normal party additions may reset target icons even with the same GUID.
	copy(same[20:25], "Mira\x00")
	same[25], same[37] = 4, 4
	deliver(t, s, opcode.SMSGGroupList, same)
	if got := state(t, s); got.RaidTargets != nil || got.Group.Members[1].Guid != 4 {
		t.Fatal("old markers survived an observed roster change", got)
	}
	deliver(t, s, opcode.MSGRaidTargetUpdate, []byte{1})
	if empty := state(t, s).RaidTargets; !empty.GetComplete() || len(empty.GetMarks()) != 0 {
		t.Fatal("empty server list was not known empty", empty)
	}
	deliver(t, s, opcode.SMSGGroupDestroyed, nil)
	if state(t, s).RaidTargets != nil || len(recorder.snapshot()) != 0 {
		t.Fatal("disband retained marks or passive receive wrote transport")
	}
}

func TestRaidTargetsMalformedPacketDoesNotChangeBindings(t *testing.T) {
	s := NewService(&packetRecorder{}, "body-one", 2, "Arlen")
	deliver(t, s, opcode.SMSGGroupList, unhex(t, duoHex))
	deliver(t, s, opcode.MSGRaidTargetUpdate, unhex(t, raidListHex))
	before := state(t, s)
	delta := unhex(t, moonDeltaHex)
	bad := [][]byte{nil, append(bytes.Clone(delta), 0),
		unhex(t, "0003000000000000000878563412000030f1"),
		unhex(t, "010a0b"), unhex(t, "010878563412000030f1"), unhex(t, "01040000000000000000"),
		unhex(t, "010478563412000030f1040a000000000030f1"),
		unhex(t, "010478563412000030f10778563412000030f1")}
	for n := 1; n < len(delta); n++ {
		bad = append(bad, delta[:n])
	}
	for _, payload := range bad {
		deliver(t, s, opcode.MSGRaidTargetUpdate, payload)
	}
	after := state(t, s)
	if !proto.Equal(before.RaidTargets, after.RaidTargets) || after.MalformedPackets-before.MalformedPackets != uint64(len(bad)) {
		t.Fatal("malformed marker packet changed observations or was misclassified", after)
	}
}

func TestRefreshRaidTargetsOnlyWritesBoundListQuery(t *testing.T) {
	recorder := &packetRecorder{}
	s := NewService(recorder, "body-one", 2, "Arlen")
	ctx := context.Background()
	request := &partyv1.RaidTargetListRequest{SessionId: "body-one", ExpectedGroupGuid: groupGUID}
	if _, err := s.RefreshRaidTargets(ctx, request); err == nil {
		t.Fatal("marker refresh accepted without an observed group")
	}
	deliver(t, s, opcode.SMSGGroupList, unhex(t, duoHex))
	for _, bad := range []*partyv1.RaidTargetListRequest{nil, {},
		{SessionId: "old-body", ExpectedGroupGuid: groupGUID},
		{SessionId: "body-one", ExpectedGroupGuid: groupGUID + 1}} {
		if _, err := s.RefreshRaidTargets(ctx, bad); err == nil {
			t.Fatal("unbound marker refresh accepted", bad)
		}
	}
	result, err := s.RefreshRaidTargets(ctx, request)
	packets := recorder.snapshot()
	if err != nil || !result.GetTransportWritten() || len(packets) != 1 ||
		packets[0].Opcode != uint32(opcode.MSGRaidTargetUpdate) || !bytes.Equal(packets[0].Payload, []byte{0xff}) {
		t.Fatalf("marker list request mismatch: %v, %v, %v", result, err, packets)
	}
	if state(t, s).RaidTargets != nil {
		t.Fatal("successful request transport fabricated a server marker list")
	}
}
