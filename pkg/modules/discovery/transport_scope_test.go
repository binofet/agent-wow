package moddisc

import (
	"os"
	"path/filepath"
	"testing"

	modtest "github.com/agent-wow/agent-wow/pkg/modules/internal/testutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestTransportScopePacketDescriptorAcceptsLegacyAndRejectsShadowedContracts(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(packet, scope *descriptorpb.DescriptorProto)
		ok   bool
	}{
		{"current", func(_, _ *descriptorpb.DescriptorProto) {}, true},
		{"legacy", func(p, _ *descriptorpb.DescriptorProto) { p.Field = p.Field[:2] }, true},
		{"extra field", func(p, _ *descriptorpb.DescriptorProto) {
			p.Field = append(p.Field, &descriptorpb.FieldDescriptorProto{Name: proto.String("extra"), Number: proto.Int32(4), Type: descriptorpb.FieldDescriptorProto_TYPE_UINT32.Enum()})
		}, false},
		{"wrong scope number", func(p, _ *descriptorpb.DescriptorProto) { p.Field[2].Number = proto.Int32(4) }, false},
		{"wrong scope name", func(p, _ *descriptorpb.DescriptorProto) { p.Field[2].Name = proto.String("other_scope") }, false},
		{"repeated scope", func(p, _ *descriptorpb.DescriptorProto) {
			p.Field[2].Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		}, false},
		{"shadowed scope", func(_, s *descriptorpb.DescriptorProto) {
			s.Field[1].Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
		}, false},
		{"wrong scope type", func(p, _ *descriptorpb.DescriptorProto) { p.Field[2].TypeName = proto.String(".google.protobuf.Empty") }, false},
		{"wrong opcode type", func(p, _ *descriptorpb.DescriptorProto) {
			p.Field[0].Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
		}, false},
		{"repeated payload", func(p, _ *descriptorpb.DescriptorProto) {
			p.Field[1].Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := modtest.WriteModule(t, root, "worker", "")
			path := filepath.Join(dir, "module.pb")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			set := new(descriptorpb.FileDescriptorSet)
			if err := proto.Unmarshal(body, set); err != nil {
				t.Fatal(err)
			}
			var packet, scope *descriptorpb.DescriptorProto
			for _, f := range set.File {
				if f.GetName() != "api/module/v1/session.proto" {
					continue
				}
				for _, m := range f.MessageType {
					switch m.GetName() {
					case "WorldPacket":
						packet = m
					case "TransportScope":
						scope = m
					}
				}
			}
			if packet == nil || scope == nil {
				t.Fatal("fixture lacks compiled Session contract")
			}
			tc.edit(packet, scope)
			body, err = proto.Marshal(set)
			if err != nil {
				t.Fatal(err)
			}
			modtest.WriteFile(t, path, body)
			_, err = Load(root)
			if (err == nil) != tc.ok {
				t.Fatalf("descriptor admission=%v, want %v: %v", err == nil, tc.ok, err)
			}
		})
	}
}
