package buddyactions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

const practiceLuaModule = "github.com/yuin/gopher-lua"
const practiceModule = "github.com/agent-wow/agent-wow"

type practiceRuntimeArtifact struct{ revision, luaVersion, luaSum string }

func validatePracticeRuntimeArtifact(info *debug.BuildInfo, identity practiceRuntimeArtifact) error {
	if info == nil || info.Path != practiceModule+"/cmd/luapolicy" || info.Main.Path != practiceModule || info.Main.Version == "" || info.Main.Replace != nil || info.GoVersion != runtime.Version() {
		return fmt.Errorf("ordinary Runtime artifact must match the program, module and toolchain")
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		if _, duplicate := settings[setting.Key]; duplicate {
			return fmt.Errorf("duplicate artifact setting %s", setting.Key)
		}
		settings[setting.Key] = setting.Value
	}
	if settings["vcs"] != "git" || settings["vcs.revision"] != identity.revision || settings["vcs.modified"] != "false" ||
		settings["GOOS"] != runtime.GOOS || settings["GOARCH"] != runtime.GOARCH || settings["-compiler"] != "gc" || settings["-buildmode"] != "exe" {
		return fmt.Errorf("ordinary Runtime artifact must be current clean HEAD for this platform")
	}
	for _, name := range []string{"-race", "-msan", "-asan", "-cover"} {
		if value := settings[name]; value != "" && value != "false" {
			return fmt.Errorf("ordinary Runtime artifact must not use %s", name)
		}
	}
	for _, name := range []string{"-gcflags", "-asmflags", "-tags", "-ldflags", "-modfile", "-covermode", "-coverpkg", "GOEXPERIMENT"} {
		if settings[name] != "" {
			return fmt.Errorf("ordinary Runtime artifact must not override %s", name)
		}
	}
	count := 0
	for _, dependency := range info.Deps {
		if dependency != nil && dependency.Path == practiceLuaModule {
			count++
			if dependency.Replace != nil || dependency.Version != identity.luaVersion || dependency.Sum != identity.luaSum {
				return fmt.Errorf("ordinary Runtime artifact must retain the exact Lua dependency")
			}
		}
	}
	if count != 1 {
		return fmt.Errorf("one pinned Lua dependency is required")
	}
	return nil
}

// The same complete native frame goes to the ordinary artifact once. This
// explicitly exercises Runtime.Reload plus Runtime.Evaluate, rather than the
// one-shot mode, and never supplies an execution/value budget override.
func practiceDefaultRuntimeCLIParity(t *testing.T, source string) func([]byte, map[string]any) {
	t.Helper()
	return starterDefaultRuntimeCLIParity(t, source, 6)
}

func starterDefaultRuntimeCLIParity(t *testing.T, source string, outputFields int) func([]byte, map[string]any) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", root}, args...)...).Output()
		if err != nil {
			t.Fatal("artifact source identity unavailable", err)
		}
		return out
	}
	identity := practiceRuntimeArtifact{revision: strings.TrimSpace(string(git("rev-parse", "HEAD")))}
	if len(git("status", "--porcelain", "--untracked-files=all")) != 0 {
		t.Fatal("ordinary Runtime proof requires the exact clean checkout")
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(mod), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "require" {
			fields = fields[1:]
		}
		if len(fields) >= 2 && fields[0] == practiceLuaModule {
			if identity.luaVersion != "" {
				t.Fatal("ambiguous Lua module pin")
			}
			identity.luaVersion = fields[1]
		}
	}
	sums, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == practiceLuaModule && fields[1] == identity.luaVersion {
			if identity.luaSum != "" {
				t.Fatal("ambiguous Lua module checksum")
			}
			identity.luaSum = fields[2]
		}
	}
	if identity.luaVersion == "" || !strings.HasPrefix(identity.luaSum, "h1:") {
		t.Fatal("exact Lua dependency pin unavailable")
	}
	binary := os.Getenv("BUDDY_LUAPOLICY_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(root, "data", "bin", "luapolicy")
	} else if !filepath.IsAbs(binary) {
		binary = filepath.Join(root, binary)
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal("build the current clean checkout's ordinary evaluator first", err)
	}
	if err := validatePracticeRuntimeArtifact(info, identity); err != nil {
		t.Fatal(err, info.Settings)
	}
	if behavior.EvaluationLimit != 5*time.Millisecond || behavior.MaxJSONBytes != 32*1024 {
		t.Fatal("default Runtime limits changed")
	}
	path := filepath.Join(t.TempDir(), "practice.lua")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return func(input []byte, expected map[string]any) {
		t.Helper()
		if len(input) > behavior.MaxJSONBytes || outputFields <= 0 || len(expected) != outputFields {
			t.Fatal("complete native frame/output contract changed", len(input), expected)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "-mode", "evaluate-runtime", "-source", path)
		command.Dir, command.Stdin = root, bytes.NewReader(input)
		var diagnostic bytes.Buffer
		command.Stderr = &diagnostic
		out, err := command.Output()
		if err != nil {
			t.Fatalf("ordinary default Runtime rejected complete frame: source=%x frame=%x bytes=%d err=%v stderr=%q", sha256.Sum256([]byte(source)), sha256.Sum256(input), len(input), err, diagnostic.String())
		}
		var actual map[string]any
		if err := json.Unmarshal(out, &actual); err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatal("complete ordinary Runtime output differs from native semantic guard flow", actual, expected, err)
		}
	}
}

func TestPracticeRuntimeArtifactRejectsInstrumentationAndSourceOverrides(t *testing.T) {
	identity := practiceRuntimeArtifact{revision: "current", luaVersion: "v1.1.1", luaSum: "h1:pin"}
	factory := func() *debug.BuildInfo {
		return &debug.BuildInfo{GoVersion: runtime.Version(), Path: practiceModule + "/cmd/luapolicy", Main: debug.Module{Path: practiceModule, Version: "(devel)"},
			Deps: []*debug.Module{{Path: practiceLuaModule, Version: identity.luaVersion, Sum: identity.luaSum}}, Settings: []debug.BuildSetting{
				{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: identity.revision}, {Key: "vcs.modified", Value: "false"}, {Key: "GOOS", Value: runtime.GOOS}, {Key: "GOARCH", Value: runtime.GOARCH},
				{Key: "-compiler", Value: "gc"}, {Key: "-buildmode", Value: "exe"}}}
	}
	if err := validatePracticeRuntimeArtifact(factory(), identity); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"-race", "-msan", "-asan", "-cover", "-gcflags", "-asmflags", "-tags", "-ldflags", "-modfile", "-covermode", "-coverpkg", "GOEXPERIMENT"} {
		info := factory()
		info.Settings = append(info.Settings, debug.BuildSetting{Key: key, Value: "true"})
		if err := validatePracticeRuntimeArtifact(info, identity); err == nil {
			t.Fatal("instrumented/overridden artifact accepted", key)
		}
	}
	for _, change := range []func(*debug.BuildInfo){
		func(info *debug.BuildInfo) { info.Settings[1].Value = "stale" },
		func(info *debug.BuildInfo) { info.Settings[2].Value = "true" },
		func(info *debug.BuildInfo) { info.Deps[0].Replace = &debug.Module{Path: "local-replacement"} },
		func(info *debug.BuildInfo) { info.Deps[0].Sum = "h1:other" },
		func(info *debug.BuildInfo) { info.Settings = append(info.Settings, info.Settings[0]) },
	} {
		info := factory()
		change(info)
		if err := validatePracticeRuntimeArtifact(info, identity); err == nil {
			t.Fatal("stale or ambiguous artifact accepted", info)
		}
	}
}
