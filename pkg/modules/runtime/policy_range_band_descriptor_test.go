package modrt

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"testing"
)

func TestRangeBandAttestationRequiresExactInstalledInputAndEcho(t *testing.T) {
	base := protodesc.ToFileDescriptorProto(actionsv1.File_modules_buddy_actions_actions_proto)
	method := actionsv1.File_modules_buddy_actions_actions_proto.Services().ByName("BuddyActions").Methods().ByName("ApproachTarget")
	if !targetRangeBandDescriptor(method) {
		t.Fatal("current API not admitted")
	}
	for _, scenario := range []string{"old_input", "old_echo", "wrong_type", "wrong_tag", "scalar_guids"} {
		t.Run(scenario, func(t *testing.T) {
			fd := protodesc.ToFileDescriptorProto(actionsv1.File_modules_buddy_actions_actions_proto)
			for _, msg := range fd.MessageType {
				for i, f := range msg.Field {
					if scenario == "old_input" && msg.GetName() == "ApproachTargetRequest" && f.GetName() == "min_range_yards" {
						msg.Field = append(msg.Field[:i], msg.Field[i+1:]...)
						break
					}
					if scenario == "old_echo" && msg.GetName() == "TargetMovementState" && f.GetName() == "min_range_yards" {
						msg.Field = append(msg.Field[:i], msg.Field[i+1:]...)
						break
					}
					if msg.GetName() == "ApproachTargetRequest" && f.GetName() == "clearance_target_guids" {
						switch scenario {
						case "wrong_type":
							x := descriptorpb.FieldDescriptorProto_TYPE_STRING
							f.Type = &x
						case "wrong_tag":
							x := int32(12)
							f.Number = &x
						case "scalar_guids":
							x := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
							f.Label = &x
						}
					}
				}
			}
			file, err := protodesc.NewFile(fd, protoregistry.GlobalFiles)
			if err != nil {
				t.Fatal(err)
			}
			if targetRangeBandDescriptor(file.Services().ByName("BuddyActions").Methods().ByName("ApproachTarget")) {
				t.Fatal("unsupported API advertised", scenario)
			}
		})
	}
	if len(base.MessageType) == 0 || targetRangeBandDescriptor(nil) {
		t.Fatal("bad descriptor fixture")
	}
}
