package buddyworld

import (
	"os"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestFactionLookupPublishedDescriptorMatchesAPI(t *testing.T) {
	api := worldv1.File_modules_buddy_world_world_proto
	field := api.Messages().ByName("SnapshotRequest").Fields().ByName("creature_faction_template")
	if field == nil || field.Number() != 5 || field.Kind() != protoreflect.Uint32Kind {
		t.Fatal("bounded faction lookup wire contract changed", field)
	}
	body, err := os.ReadFile("module.pb")
	if err != nil {
		t.Fatal(err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(body, set); err != nil {
		t.Fatal(err)
	}
	for _, file := range set.File {
		if file.GetName() == api.Path() {
			if !proto.Equal(file, protodesc.ToFileDescriptorProto(api)) {
				t.Fatal("installed world descriptor and generated faction lookup differ")
			}
			return
		}
	}
	t.Fatal("world API absent from published descriptor")
}
