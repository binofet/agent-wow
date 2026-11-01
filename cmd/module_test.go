package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/internal/config"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
)

func TestModuleListOffline(t *testing.T) {
	path := initTestConfig(t)
	root := t.TempDir()
	t.Setenv("AGENT_WOW_MODULE_DIR", root)
	if err := config.Init(path); err != nil {
		t.Fatal(err)
	}
	for _, asJSON := range []bool{false, true} {
		cmd := newModuleCommand()
		args := []string{"list"}
		if asJSON {
			args = append(args, "--json")
		}
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if asJSON && !strings.Contains(out.String(), `"modules": []`) {
			t.Fatal(out.String())
		}
	}
	dir := filepath.Join(root, "example")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "module.yaml"), []byte("api_version: 1\ndescription: Offline fixture\nrequires: [worker]\nrpc:\n  run: /example.Service/Run\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := newModuleCommand()
	cmd.SetArgs([]string{"list", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Modules []struct {
			Name     string
			Enabled  bool
			Requires []string
			RPC      map[string]string
		}
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Modules) != 1 || result.Modules[0].Name != "example" || result.Modules[0].Enabled || result.Modules[0].RPC["run"] != "/example.Service/Run" {
		t.Fatal(out.String())
	}
}

func TestModuleDescribeOffline(t *testing.T) {
	path := initTestConfig(t)
	root, err := filepath.Abs("../modules")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_WOW_MODULE_DIR", root)
	if err := config.Init(path); err != nil {
		t.Fatal(err)
	}
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	expected := registry.Catalog()
	if len(expected.Methods) == 0 {
		t.Fatal("fixture has no public methods")
	}
	for _, args := range [][]string{{"describe"}, {"describe", expected.Methods[0].Name}} {
		cmd := newModuleCommand()
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var got moddisc.Catalog
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		wantCatalog := expected
		if len(args) == 2 {
			wantCatalog, err = expected.ForMethod(args[1])
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Methods) != 1 || got.Methods[0].Name != args[1] || len(got.Messages) >= len(expected.Messages) {
				t.Fatal("method describe did not project the requested contract")
			}
		}
		want, _ := json.Marshal(wantCatalog)
		actual, _ := json.Marshal(got)
		if !bytes.Equal(want, actual) {
			t.Fatal("describe lost validated metadata or schemas")
		}
	}
	for _, args := range [][]string{{"describe", "not-a-public-method"}, {"describe", expected.Methods[0].Name, "extra"}} {
		cmd := newModuleCommand()
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
	// Enabled malformed modules must fail validation rather than emit a partial catalog.
	invalid := t.TempDir()
	dir := filepath.Join(invalid, "broken")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "module.yaml"), []byte("api_version: 1\nenabled: true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_WOW_MODULE_DIR", invalid)
	if err := config.Init(path); err != nil {
		t.Fatal(err)
	}
	cmd := newModuleCommand()
	cmd.SetArgs([]string{"describe"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatal("accepted invalid module")
	}
}
