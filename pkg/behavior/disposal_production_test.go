package behavior

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

const disposalProductModule = "github.com/agent-wow/agent-wow"
const disposalLuaModule = "github.com/yuin/gopher-lua"
const disposalProcessLimit = 5 * time.Second

var errDisposalOutputLimit = errors.New("production evaluator output limit exceeded")

const disposalDefaultDecideResource = "decide: Lua execution budget exhausted (5ms configured): context deadline exceeded\n"

type disposalProductionResourceError struct {
	phase, diagnostic string
	cause             error
}

func (e *disposalProductionResourceError) Error() string {
	return fmt.Sprintf("production evaluator: %v; stderr=%q", e.cause, e.diagnostic)
}
func (e *disposalProductionResourceError) Unwrap() error { return ErrExecutionBudget }

func disposalDefaultResourceTurn(exitCode int, processErr error, output []byte, diagnostic string) bool {
	return processErr == nil && exitCode == 1 && len(output) == 0 && diagnostic == disposalDefaultDecideResource
}

type disposalBoundedOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *disposalBoundedOutput) Len() int       { return b.buffer.Len() }
func (b *disposalBoundedOutput) Bytes() []byte  { return b.buffer.Bytes() }
func (b *disposalBoundedOutput) String() string { return b.buffer.String() }

func (b *disposalBoundedOutput) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		b.exceeded = true
		n, _ := b.buffer.Write(p[:remaining])
		return n, errDisposalOutputLimit
	}
	return b.buffer.Write(p)
}

type disposalProductIdentity struct {
	revision, luaVersion, luaSum string
}

func disposalGit(root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), disposalProcessLimit)
	defer cancel()
	command := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", root}, args...)...)
	command.WaitDelay = time.Second
	out, diagnostic := &disposalBoundedOutput{limit: MaxJSONBytes}, &disposalBoundedOutput{limit: 8192}
	command.Stdout, command.Stderr = out, diagnostic
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w; stderr=%q", strings.Join(args, " "), err, diagnostic.String())
	}
	if out.exceeded || diagnostic.exceeded {
		return nil, errDisposalOutputLimit
	}
	return out.Bytes(), nil
}

func disposalCheckoutIdentity(root string) (disposalProductIdentity, error) {
	var identity disposalProductIdentity
	head, err := disposalGit(root, "rev-parse", "HEAD")
	if err != nil {
		return identity, err
	}
	identity.revision = strings.TrimSpace(string(head))
	if identity.revision == "" || strings.ContainsAny(identity.revision, " \t\r\n") {
		return identity, errors.New("one current Git HEAD is required")
	}
	changes, err := disposalGit(root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return identity, err
	}
	if len(changes) != 0 {
		return identity, errors.New("production evaluator requires a clean tracked and untracked checkout; inspect git status --short")
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return identity, err
	}
	for _, line := range strings.Split(string(mod), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "require" {
			fields = fields[1:]
		}
		if len(fields) >= 2 && fields[0] == disposalLuaModule {
			if identity.luaVersion != "" {
				return identity, errors.New("ambiguous GopherLua module pin")
			}
			identity.luaVersion = fields[1]
		}
	}
	sums, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		return identity, err
	}
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == disposalLuaModule && fields[1] == identity.luaVersion {
			if identity.luaSum != "" {
				return identity, errors.New("ambiguous GopherLua checksum")
			}
			identity.luaSum = fields[2]
		}
	}
	if identity.luaVersion == "" || !strings.HasPrefix(identity.luaSum, "h1:") {
		return identity, errors.New("current GopherLua version and checksum are required")
	}
	return identity, nil
}

func validateDisposalProduct(info *debug.BuildInfo, identity disposalProductIdentity) error {
	if info == nil || info.Path != disposalProductModule+"/cmd/luapolicy" || info.Main.Path != disposalProductModule ||
		info.Main.Version == "" || info.Main.Replace != nil || info.GoVersion != runtime.Version() {
		return errors.New("evaluator program, module version and Go runtime must match this checkout/toolchain")
	}
	// Go1.27 can stamp a clean VCS-derived module version. Exact HEAD below
	// binds its source more precisely than requiring the older '(devel)' label.
	settings := make(map[string]string, len(info.Settings))
	for _, value := range info.Settings {
		if _, duplicate := settings[value.Key]; duplicate {
			return errors.New("ambiguous evaluator build settings")
		}
		settings[value.Key] = value.Value
	}
	if settings["vcs"] != "git" || settings["vcs.revision"] != identity.revision || settings["vcs.modified"] != "false" ||
		settings["GOOS"] != runtime.GOOS || settings["GOARCH"] != runtime.GOARCH {
		return errors.New("evaluator must be built from current clean Git HEAD for this platform")
	}
	for _, name := range []string{"-race", "-msan", "-asan", "-cover"} {
		if value := settings[name]; value != "" && value != "false" {
			return fmt.Errorf("production evaluator must not use %s", name)
		}
	}
	for _, name := range []string{"-gcflags", "-asmflags", "-tags", "-covermode", "-coverpkg", "GOEXPERIMENT"} {
		if settings[name] != "" {
			return fmt.Errorf("production evaluator must not override %s", name)
		}
	}
	count := 0
	for _, dependency := range info.Deps {
		if dependency != nil && dependency.Path == disposalLuaModule {
			count++
			if dependency.Replace != nil || dependency.Version != identity.luaVersion || dependency.Sum != identity.luaSum {
				return errors.New("evaluator GopherLua version/checksum must match current go.mod/go.sum without replacement")
			}
		}
	}
	if count != 1 {
		return errors.New("one pinned evaluator GopherLua dependency is required")
	}
	return nil
}

func decodeDisposalProduct(raw []byte) (map[string]any, error) {
	if len(raw) > MaxJSONBytes+1 {
		return nil, errDisposalOutputLimit
	}
	// The CLI adds one framing newline after its bounded JSON payload.
	if len(raw) > 0 && raw[len(raw)-1] == '\n' {
		raw = raw[:len(raw)-1]
	}
	if len(raw) > MaxJSONBytes {
		return nil, errDisposalOutputLimit
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("production evaluator object: %w", err)
	}
	if result == nil {
		return nil, errors.New("production evaluator returned no object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("production evaluator must return exactly one JSON object")
	}
	return result, nil
}

// CI builds the ordinary CLI from this checkout before the race suite. Its
// maintained build command supplies provenance; build metadata pins the cohort,
// rather than attesting unrecorded compiler inputs in arbitrary override files.
func evaluateDisposalProduction(t *testing.T, source string, frame map[string]any) (map[string]any, error) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	setupFailure := func(err error) {
		t.Helper()
		t.Fatalf("production disposal evaluator setup: %v; use a clean checkout, then from %s run: go build -o data/bin/luapolicy ./cmd/luapolicy; unset BUDDY_LUAPOLICY_TEST_BINARY or point it to that exact binary", err, root)
	}
	identity, err := disposalCheckoutIdentity(root)
	if err != nil {
		setupFailure(err)
	}
	binary := os.Getenv("BUDDY_LUAPOLICY_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(root, "data", "bin", "luapolicy")
	} else if !filepath.IsAbs(binary) {
		binary = filepath.Join(root, binary)
	}
	before, err := os.Stat(binary)
	if err != nil || !before.Mode().IsRegular() {
		setupFailure(fmt.Errorf("regular evaluator binary %s required: %v", binary, err))
	}
	if runtime.GOOS != "windows" && before.Mode().Perm()&0111 == 0 {
		setupFailure(errors.New("production evaluator must be executable"))
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		setupFailure(err)
	}
	if err := validateDisposalProduct(info, identity); err != nil {
		setupFailure(err)
	}
	if EvaluationLimit != 5*time.Millisecond || MaxJSONBytes != 32*1024 {
		setupFailure(errors.New("this proof requires the unchanged standalone5ms/32KiB defaults"))
	}
	if frame == nil || len(source) == 0 || len(source) > DefaultSourceBytes {
		return nil, errors.New("bounded source and object input required")
	}
	input, err := json.Marshal(frame)
	if err != nil || len(input) > MaxJSONBytes {
		return nil, fmt.Errorf("bounded production evaluator input required: %v", err)
	}
	path := filepath.Join(t.TempDir(), "disposal.lua")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), disposalProcessLimit)
	defer cancel()
	// No execution/value/source override, retry, shell or validation mode.
	command := exec.CommandContext(ctx, binary, "-source", path)
	command.Dir, command.Stdin, command.WaitDelay = root, bytes.NewReader(input), time.Second
	out, diagnostic := &disposalBoundedOutput{limit: MaxJSONBytes + 1}, &disposalBoundedOutput{limit: 8192}
	command.Stdout, command.Stderr = out, diagnostic
	runErr := command.Run()
	processErr := ctx.Err()
	cancel()
	after, statErr := os.Stat(binary)
	current, identityErr := disposalCheckoutIdentity(root)
	if statErr != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) ||
		identityErr != nil || current != identity {
		setupFailure(errors.New("evaluator artifact or source checkout changed during the single invocation"))
	}
	if processErr != nil {
		return nil, fmt.Errorf("production evaluator process: %w", processErr)
	}
	if out.exceeded || diagnostic.exceeded {
		return nil, errDisposalOutputLimit
	}
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) && disposalDefaultResourceTurn(exit.ExitCode(), processErr, out.Bytes(), diagnostic.String()) {
			// Preserve unavailability as an error and never decode or fabricate a
			// decision. Existing callers remain strict unless they explicitly test
			// the Runtime resource-turn contract.
			return nil, &disposalProductionResourceError{"decide", diagnostic.String(), runErr}
		}
		return nil, fmt.Errorf("production evaluator: %w; stderr=%q", runErr, diagnostic.String())
	}
	if diagnostic.Len() != 0 {
		return nil, fmt.Errorf("production evaluator unexpected stderr: %q", diagnostic.String())
	}
	return decodeDisposalProduct(out.Bytes())
}

func TestDisposalProductionResourceTurnRequiresExactNoDecision(t *testing.T) {
	if !disposalDefaultResourceTurn(1, nil, nil, disposalDefaultDecideResource) {
		t.Fatal("exact default decide resource turn was not recognized")
	}
	for _, tc := range []struct {
		name       string
		exit       int
		processErr error
		output     []byte
		diagnostic string
	}{
		{"success", 0, nil, nil, disposalDefaultDecideResource},
		{"other_exit", 2, nil, nil, disposalDefaultDecideResource},
		{"process_deadline", 1, context.DeadlineExceeded, nil, disposalDefaultDecideResource},
		{"partial_output", 1, nil, []byte(`{"kind":`), disposalDefaultDecideResource},
		{"empty_decision", 1, nil, []byte("{}\n"), disposalDefaultDecideResource},
		{"accept", 1, nil, []byte(`{"kind":"accept"}`), disposalDefaultDecideResource},
		{"other_budget", 1, nil, nil, strings.Replace(disposalDefaultDecideResource, "5ms", "50ms", 1)},
		{"other_phase", 1, nil, nil, strings.Replace(disposalDefaultDecideResource, "decide:", "chunk_initialize:", 1)},
		{"lua_error", 1, nil, nil, "policy:2: " + disposalDefaultDecideResource + "stack traceback\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if disposalDefaultResourceTurn(tc.exit, tc.processErr, tc.output, tc.diagnostic) {
				t.Fatal("noncanonical failure or production output became safe unavailability")
			}
		})
	}
}

func TestDisposalProductionArtifactIdentityRejectsWrongCohortAndInstrumentation(t *testing.T) {
	identity := disposalProductIdentity{"0123456789abcdef0123456789abcdef01234567", "v1.1.2", "h1:pin"}
	valid := func() *debug.BuildInfo {
		return &debug.BuildInfo{GoVersion: runtime.Version(), Path: disposalProductModule + "/cmd/luapolicy",
			Main: debug.Module{Path: disposalProductModule, Version: "v0.0.0-20261121000000-0123456789ab"},
			Deps: []*debug.Module{{Path: disposalLuaModule, Version: identity.luaVersion, Sum: identity.luaSum}},
			Settings: []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: identity.revision},
				{Key: "vcs.modified", Value: "false"}, {Key: "GOOS", Value: runtime.GOOS}, {Key: "GOARCH", Value: runtime.GOARCH}}}
	}
	if err := validateDisposalProduct(valid(), identity); err != nil {
		t.Fatal("current clean VCS-derived product version was refused", err)
	}
	for name, change := range map[string]func(*debug.BuildInfo){
		"old commit": func(v *debug.BuildInfo) { v.Settings[1].Value = "older" },
		"dirty":      func(v *debug.BuildInfo) { v.Settings[2].Value = "true" },
		"race": func(v *debug.BuildInfo) {
			v.Settings = append(v.Settings, debug.BuildSetting{Key: "-race", Value: "true"})
		},
		"debug": func(v *debug.BuildInfo) {
			v.Settings = append(v.Settings, debug.BuildSetting{Key: "-gcflags", Value: "all=-N -l"})
		},
		"program":   func(v *debug.BuildInfo) { v.Path += "-other" },
		"module":    func(v *debug.BuildInfo) { v.Main.Path += "-other" },
		"toolchain": func(v *debug.BuildInfo) { v.GoVersion += "-other" },
		"engine":    func(v *debug.BuildInfo) { v.Deps[0].Version = "v0.0.0" },
		"checksum":  func(v *debug.BuildInfo) { v.Deps[0].Sum = "h1:other" },
		"engine replacement": func(v *debug.BuildInfo) {
			v.Deps[0].Replace = &debug.Module{Path: "/tmp/other-engine"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := valid()
			change(value)
			if err := validateDisposalProduct(value, identity); err == nil {
				t.Fatal("unbound or instrumented evaluator was accepted")
			}
		})
	}
}

func TestDisposalProductionOutputIsBoundedAndOneObject(t *testing.T) {
	out := &disposalBoundedOutput{limit: 4}
	if _, err := out.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("cdef")); !errors.Is(err, errDisposalOutputLimit) || !out.exceeded || out.String() != "abcd" {
		t.Fatal("output was silently truncated or grew past its limit", out.String(), err)
	}
	stream := &disposalBoundedOutput{limit: 4}
	if n, err := io.Copy(stream, io.LimitReader(strings.NewReader("abcdef"), 6)); n != 4 || !errors.Is(err, errDisposalOutputLimit) || stream.String() != "abcd" {
		t.Fatal("stream copy bypassed the output bound", n, err, stream.String())
	}
	for _, invalid := range []string{"", "null\n", "[]\n", "{}\n{}\n", strings.Repeat("x", MaxJSONBytes+2)} {
		if _, err := decodeDisposalProduct([]byte(invalid)); err == nil {
			t.Fatal("non-object, extra output or oversized stream was accepted")
		}
	}
	prefix, suffix := `{"value":"`, `"}`
	payload := prefix + strings.Repeat("x", MaxJSONBytes-len(prefix)-len(suffix)) + suffix
	if _, err := decodeDisposalProduct([]byte(payload + "\n")); err != nil {
		t.Fatal("bounded payload plus its framing newline was refused", err)
	}
}

func TestDisposalProductionEvaluatorUsesExactCurrentDefaults(t *testing.T) {
	out, err := evaluateDisposalProduction(t, `return {decide=function(input)return {status="keep",value=input.value}end}`,
		map[string]any{"value": "current-cohort"})
	if err != nil || out["status"] != "keep" || out["value"] != "current-cohort" {
		t.Fatal("single real production evaluator invocation failed", out, err)
	}
}
