package moddisc_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/modules/internal/testutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestRegistryAccessorsProtectValidatedConfiguration(t *testing.T) {
	root := t.TempDir()
	modtest.WriteModule(t, root, "orchestrator", "worker")
	modtest.WriteModule(t, root, "worker", "")
	r, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := r.Lookup("orchestrator")
	if !ok {
		t.Fatal("enabled module missing")
	}
	manifest := d.Manifest()
	manifest.Requires[0] = "undeclared"
	manifest.RPC["execute"] = "/invalid.Service/Method"
	delete(manifest.Packets, "SMSG_LOGIN_VERIFY_WORLD")
	order := r.Order()
	order[0] = "undeclared"

	if !d.Requires("worker") || d.Requires("undeclared") {
		t.Fatal("caller changed validated dependencies")
	}
	if got := strings.Join(r.Order(), ","); got != "worker,orchestrator" {
		t.Fatal("caller changed dependency order", got)
	}
	fresh := d.Manifest()
	md, ok := d.RPC("execute")
	if !ok || fresh.RPC["execute"] != md.Path() || fresh.Requires[0] != "worker" || len(fresh.Packets) != 2 {
		t.Fatal("caller changed validated manifest", fresh)
	}
	if _, ok := d.Packet(0x236); !ok {
		t.Fatal("caller changed validated subscription")
	}
}

func TestProductionPartyImportsMatchGeneratedContract(t *testing.T) {
	want := protodesc.ToFileDescriptorProto(partyv1.File_modules_buddy_party_party_proto)
	for _, module := range []string{"buddy-party", "buddy-actions", "buddy-inventory", "buddy-loot"} {
		t.Run(module, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "modules", module, "module.pb")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var set descriptorpb.FileDescriptorSet
			if err := proto.Unmarshal(raw, &set); err != nil {
				t.Fatal(err)
			}
			var got *descriptorpb.FileDescriptorProto
			matches := 0
			for _, file := range set.File {
				if file.GetName() == want.GetName() {
					got = file
					matches++
				}
			}
			if matches != 1 {
				t.Fatalf("%s contains %d copies of %s, want exactly one", path, matches, want.GetName())
			}
			if !proto.Equal(got, want) {
				t.Fatalf("%s embeds %s that differs from the generated Party contract", path, want.GetName())
			}
		})
	}
}
