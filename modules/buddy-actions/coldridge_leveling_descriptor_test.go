package buddyactions

import (
	"os"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

// The core loads module.pb while Go uses the generated descriptor. A new
// request/handoff must have the same wire contract at both boundaries.
func TestColdridgeLevelingPublishedDescriptorMatchesAPI(t *testing.T) {
	body, err := os.ReadFile("module.pb")
	if err != nil {
		t.Fatal(err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(body, set); err != nil {
		t.Fatal(err)
	}
	api := actionsv1.File_modules_buddy_actions_actions_proto
	found := false
	for _, file := range set.File {
		if file.GetName() == api.Path() {
			found = true
			if !proto.Equal(file, protodesc.ToFileDescriptorProto(api)) {
				t.Fatal("installed descriptor and generated actions API differ")
			}
		}
	}
	if !found {
		t.Fatal("actions API absent from module descriptor")
	}
	field := api.Messages().ByName("State").Fields().ByName("coldridge_leveling")
	// Parent mission source already occupies69; preserve this independent70.
	if field == nil || field.Number() != 70 || field.Message().FullName() != "warcraftbuddy.actions.v1.ColdridgeLevelingState" {
		t.Fatal("leveling handoff wire field changed", field)
	}
	method := api.Services().ByName("BuddyActions").Methods().ByName("StartColdridgeLeveling")
	if method == nil || method.Input().FullName() != "warcraftbuddy.actions.v1.ColdridgeLevelingRequest" || method.Output().FullName() != "warcraftbuddy.actions.v1.State" {
		t.Fatal("explicit leveling RPC contract changed", method)
	}
	manifest, err := os.ReadFile("module.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(manifest), "  start_coldridge_leveling: /warcraftbuddy.actions.v1.BuddyActions/StartColdridgeLeveling\n") != 1 {
		t.Fatal("explicit leveling RPC is not published exactly once")
	}
}
