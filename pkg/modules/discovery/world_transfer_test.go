package moddisc

import (
	"os"
	"path/filepath"
	"testing"

	modtest "github.com/agent-wow/agent-wow/pkg/modules/internal/testutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestWorldTransferDescriptorKeepsPriorScopeAndRejectsShadowedWitness(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(scope, transfer *descriptorpb.DescriptorProto)
		ok   bool
	}{
		{"current", func(_, _ *descriptorpb.DescriptorProto) {}, true},
		{"prior_thirteen_field_scope", func(s, _ *descriptorpb.DescriptorProto) { s.Field = s.Field[:13] }, true},
		{"prior_fourteen_field_scope", func(s, _ *descriptorpb.DescriptorProto) { s.Field = s.Field[:14] }, true},
		{"prior_fifteen_shadow_transfer", func(s, w *descriptorpb.DescriptorProto) {
			s.Field = s.Field[:15]
			w.Field[7].Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
		}, false},
		{"prior_thirteen_shadow_transfer", func(s, w *descriptorpb.DescriptorProto) {
			s.Field = s.Field[:13]
			w.Field[1].Type = descriptorpb.FieldDescriptorProto_TYPE_UINT32.Enum()
		}, false},
		{"summon_sequence_wrong_scalar", func(s, _ *descriptorpb.DescriptorProto) {
			s.Field[13].Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
		}, false},
		{"prior_twelve_field_scope", func(s, _ *descriptorpb.DescriptorProto) { s.Field = s.Field[:12] }, true},
		{"prior_scope_wrong_scalar", func(s, _ *descriptorpb.DescriptorProto) {
			s.Field = s.Field[:12]
			s.Field[1].Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
		}, false},
		{"repeated_witness", func(s, _ *descriptorpb.DescriptorProto) {
			s.Field[12].Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		}, false},
		{"wrong_witness_type", func(s, _ *descriptorpb.DescriptorProto) {
			s.Field[12].TypeName = proto.String(".google.protobuf.Empty")
		}, false},
		{"shadowed_phase", func(_, w *descriptorpb.DescriptorProto) {
			w.Field[1].Type = descriptorpb.FieldDescriptorProto_TYPE_UINT32.Enum()
		}, false},
		{"shadowed_source_counter", func(_, w *descriptorpb.DescriptorProto) {
			w.Field[7].Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
		}, false},
		{"repeated_target", func(_, w *descriptorpb.DescriptorProto) {
			w.Field[15].Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		}, false},
		{"extra_witness_field", func(_, w *descriptorpb.DescriptorProto) {
			w.Field = append(w.Field, &descriptorpb.FieldDescriptorProto{Name: proto.String("extra"), Number: proto.Int32(27), Type: descriptorpb.FieldDescriptorProto_TYPE_UINT32.Enum()})
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
			var scope, transfer *descriptorpb.DescriptorProto
			for _, f := range set.File {
				if f.GetName() != "api/module/v1/session.proto" {
					continue
				}
				for _, m := range f.MessageType {
					switch m.GetName() {
					case "TransportScope":
						scope = m
					case "ObservedWorldTransfer":
						transfer = m
					}
				}
			}
			if scope == nil || transfer == nil || len(scope.Field) != 16 || scope.Field[15].GetName() != "taxi_arrival_query_revision" {
				t.Fatal("fixture lacks current transfer witness contract")
			}
			tc.edit(scope, transfer)
			body, err = proto.Marshal(set)
			if err != nil {
				t.Fatal(err)
			}
			modtest.WriteFile(t, path, body)
			_, err = Load(root)
			if (err == nil) != tc.ok {
				t.Fatalf("witness admission=%v, want %v: %v", err == nil, tc.ok, err)
			}
		})
	}
}
