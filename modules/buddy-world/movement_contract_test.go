package buddyworld

import (
	"os"
	"path/filepath"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestOwnMovementFactsPublishedWithEveryWorldImport(t *testing.T) {
	api := worldv1.File_modules_buddy_world_world_proto
	want := protodesc.ToFileDescriptorProto(api)
	for _, module := range []string{
		"buddy-world", "buddy-actions", "buddy-inventory", "buddy-loot",
		"buddy-quests", "buddy-training", "buddy-supplies", "buddy-professions",
		"buddy-quest-items",
	} {
		t.Run(module, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", module, "module.pb"))
			if err != nil {
				t.Fatal(err)
			}
			set := &descriptorpb.FileDescriptorSet{}
			if err := proto.Unmarshal(body, set); err != nil {
				t.Fatal(err)
			}
			files, err := protodesc.NewFiles(set)
			if err != nil {
				t.Fatalf("published import closure is incomplete: %v", err)
			}
			file, err := files.FindFileByPath(api.Path())
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(protodesc.ToFileDescriptorProto(file), want) {
				t.Fatal("published World import differs from compiled API")
			}
		})
	}
	for _, field := range []struct {
		message protoreflect.Name
		name    protoreflect.Name
		number  protoreflect.FieldNumber
		kind    protoreflect.Kind
	}{
		{"PlayerState", "position_inferred", 61, protoreflect.BoolKind},
		{"SnapshotResponse", "self_movement_facts", 48, protoreflect.MessageKind},
		{"SnapshotResponse", "self_spline_facts", 49, protoreflect.MessageKind},
		{"OwnMovementFacts", "wire_image", 10, protoreflect.BytesKind},
		{"OwnSplineFacts", "id_known", 7, protoreflect.BoolKind},
		{"OwnSplineFacts", "spline_id", 8, protoreflect.Uint32Kind},
		{"MovementTransportFacts", "seat", 7, protoreflect.Sint32Kind},
	} {
		got := api.Messages().ByName(field.message).Fields().ByName(field.name)
		if got == nil || got.Number() != field.number || got.Kind() != field.kind {
			t.Fatalf("%s.%s wire contract changed: %v", field.message, field.name, got)
		}
	}
}
