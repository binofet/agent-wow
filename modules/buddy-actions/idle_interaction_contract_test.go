package buddyactions

import (
	"os"
	"path/filepath"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestIdleInteractionContractsShareCurrentActionsAndCommonSession(t *testing.T) {
	want := map[string]*descriptorpb.FileDescriptorProto{}
	for _, file := range []*descriptorpb.FileDescriptorProto{
		protodesc.ToFileDescriptorProto(actionsv1.File_modules_buddy_actions_actions_proto),
		protodesc.ToFileDescriptorProto(modv1.File_api_module_v1_session_proto),
	} {
		want[file.GetName()] = file
	}
	for _, module := range []string{"buddy-actions", "buddy-inventory", "buddy-loot"} {
		t.Run(module, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", module, "module.pb"))
			if err != nil {
				t.Fatal(err)
			}
			set := &descriptorpb.FileDescriptorSet{}
			if err := proto.Unmarshal(raw, set); err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, file := range set.File {
				if expected := want[file.GetName()]; expected != nil {
					if seen[file.GetName()] || !proto.Equal(file, expected) {
						t.Fatalf("%s does not contain the current compiled contract", file.GetName())
					}
					seen[file.GetName()] = true
				}
			}
			if len(seen) != len(want) {
				t.Fatal("imported current Actions/CommonSession contract is absent", seen)
			}
		})
	}
}
