package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/internal/config"
)

type doctorFixture struct {
	deps         doctorDependencies
	commands     []string
	addresses    []string
	commandError map[string]error
	contexts     []context.Context
	configCalls  int
}

func newDoctorFixture() *doctorFixture {
	f := &doctorFixture{commandError: map[string]error{}}
	f.deps = doctorDependencies{
		goos: "linux",
		lookPath: func(name string) (string, error) {
			if name != "docker" {
				return "", errors.New("unexpected executable")
			}
			return "/usr/bin/docker", nil
		},
		getenv: func(string) string { return "" },
		loadConfig: func() (config.Config, error) {
			f.configCalls++
			return config.Config{AuthServer: config.Server{Host: "::1", Port: 3724}}, nil
		},
		run: func(ctx context.Context, path string, args ...string) (string, error) {
			call := strings.Join(args, " ")
			f.commands = append(f.commands, path+" "+call)
			f.contexts = append(f.contexts, ctx)
			if err := f.commandError[call]; err != nil {
				return "", err
			}
			if call == "context inspect --format {{.Endpoints.docker.Host}}" {
				return "unix:///var/run/docker.sock", nil
			}
			return "29.0.0", nil
		},
		dial: func(ctx context.Context, address string) error {
			f.addresses = append(f.addresses, address)
			f.contexts = append(f.contexts, ctx)
			return nil
		},
	}
	return f
}

func executeDoctor(t *testing.T, deps doctorDependencies, args ...string) (doctorReport, error) {
	t.Helper()
	command := newDoctorCommandWithDependencies(deps)
	command.SetArgs(append([]string{"--json"}, args...))
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	err := command.Execute()
	if stderr.Len() != 0 {
		t.Fatalf("doctor polluted stderr: %q", stderr.String())
	}
	var report doctorReport
	decoder := json.NewDecoder(&stdout)
	if decodeErr := decoder.Decode(&report); decodeErr != nil {
		t.Fatalf("invalid JSON report: %v; output %q", decodeErr, stdout.String())
	}
	if decodeErr := decoder.Decode(new(any)); decodeErr != io.EOF {
		t.Fatalf("extra data after JSON report: %v", decodeErr)
	}
	return report, err
}

func TestDoctorCompletePrerequisites(t *testing.T) {
	f := newDoctorFixture()
	report, err := executeDoctor(t, f.deps, "--timeout", "2s")
	if err != nil || !report.Ready {
		t.Fatalf("healthy prerequisites failed: %+v, %v", report, err)
	}
	if len(report.Checks) != 7 {
		t.Fatalf("expected every required check, got %+v", report.Checks)
	}
	for _, check := range report.Checks {
		if check.Status != "pass" || !check.Required {
			t.Errorf("unexpected check: %+v", check)
		}
	}
	if !reflect.DeepEqual(f.addresses, []string{"[::1]:3724"}) {
		t.Fatalf("did not probe the configured IPv6 TCP endpoint: %v", f.addresses)
	}
	if !strings.Contains(report.Scope, "login, worldserver, and gameplay are not checked") {
		t.Fatal("prerequisite report implies full gameplay readiness")
	}
	if got := doctorCheckNamed(t, report, "auth_tcp").Detail; !strings.Contains(got, "WoW protocol and login have not been verified") {
		t.Fatal("TCP result implies protocol/login validation")
	}
	if len(f.contexts) != 5 {
		t.Fatalf("expected bounded Docker and TCP probes, got %d", len(f.contexts))
	}
	for _, ctx := range f.contexts {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Fatal("probe did not receive the configured bounded context")
		}
	}
	wantCommands := []string{
		"/usr/bin/docker --version",
		"/usr/bin/docker context inspect --format {{.Endpoints.docker.Host}}",
		"/usr/bin/docker info --format {{.ServerVersion}}",
		"/usr/bin/docker compose version --short",
	}
	if !reflect.DeepEqual(f.commands, wantCommands) {
		t.Fatalf("unexpected commands (doctor must not start or modify containers): %v", f.commands)
	}
}

func TestDoctorFailedPrerequisitesRemainMachineReadable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check string
		setup func(*doctorFixture)
	}{
		{"daemon unavailable", "docker_daemon", func(f *doctorFixture) {
			f.commandError["info --format {{.ServerVersion}}"] = errors.New("exit status 1")
		}},
		{"compose missing", "docker_compose", func(f *doctorFixture) {
			f.commandError["compose version --short"] = errors.New("exit status 1")
		}},
		{"auth unreachable", "auth_tcp", func(f *doctorFixture) {
			f.deps.dial = func(context.Context, string) error { return errors.New("connection refused") }
		}},
		{"linux required", "linux_runtime", func(f *doctorFixture) { f.deps.goos = "darwin" }},
		{"remote engine", "docker_endpoint", func(f *doctorFixture) {
			f.deps.getenv = func(key string) string {
				if key == "DOCKER_HOST" {
					return "tcp://remote:2375"
				}
				return ""
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDoctorFixture()
			tc.setup(f)
			report, err := executeDoctor(t, f.deps)
			if err == nil || report.Ready {
				t.Fatalf("failed prerequisite reported ready: %+v, %v", report, err)
			}
			check := doctorCheckNamed(t, report, tc.check)
			if check.Status != "fail" || check.Remedy == "" {
				t.Fatalf("failure lacks a concrete remedy: %+v", check)
			}
			if len(report.Checks) != 7 {
				t.Fatal("one failed prerequisite prevented independent checks")
			}
		})
	}
}

func TestDoctorMissingDockerStillChecksAuth(t *testing.T) {
	f := newDoctorFixture()
	f.deps.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	report, err := executeDoctor(t, f.deps)
	if err == nil || report.Ready || len(f.commands) != 0 {
		t.Fatalf("missing CLI not handled correctly: %+v, %v", report, err)
	}
	for _, name := range []string{"docker_endpoint", "docker_daemon", "docker_compose"} {
		check := doctorCheckNamed(t, report, name)
		if check.Status != "skip" || check.Remedy == "" {
			t.Errorf("missing dependency not explained: %+v", check)
		}
	}
	if doctorCheckNamed(t, report, "auth_tcp").Status != "pass" || len(f.addresses) != 1 {
		t.Fatal("missing Docker prevented independent TCP check")
	}
}

func TestDoctorConfigFailureIsReportedWithoutDialing(t *testing.T) {
	f := newDoctorFixture()
	f.deps.loadConfig = func() (config.Config, error) {
		return config.Config{}, errors.New("authserver.port must be between 1 and 65535")
	}
	report, err := executeDoctor(t, f.deps)
	if err == nil || report.Ready || len(f.addresses) != 0 {
		t.Fatalf("invalid config not handled: %+v, %v", report, err)
	}
	if doctorCheckNamed(t, report, "configuration").Status != "fail" || doctorCheckNamed(t, report, "auth_tcp").Status != "skip" {
		t.Fatal("invalid configuration or skipped TCP check not explained")
	}
	if doctorCheckNamed(t, report, "docker_daemon").Status != "pass" {
		t.Fatal("invalid auth config prevented independent Docker checks")
	}
}

func TestDoctorTimeoutBounds(t *testing.T) {
	for _, timeout := range []string{"0", "-1s", "1m1s"} {
		t.Run(timeout, func(t *testing.T) {
			f := newDoctorFixture()
			report, err := executeDoctor(t, f.deps, "--timeout", timeout)
			if err == nil || report.Ready || len(report.Checks) != 1 || report.Checks[0].Name != "timeout" {
				t.Fatalf("invalid timeout accepted: %+v, %v", report, err)
			}
			if f.configCalls != 0 || len(f.commands) != 0 || len(f.addresses) != 0 {
				t.Fatal("invalid timeout ran probes")
			}
		})
	}
}

func TestDoctorContextOverridesHost(t *testing.T) {
	f := newDoctorFixture()
	f.deps.getenv = func(key string) string {
		switch key {
		case "DOCKER_HOST":
			return "tcp://ignored:2375"
		case "DOCKER_CONTEXT":
			return "local"
		default:
			return ""
		}
	}
	report, err := executeDoctor(t, f.deps)
	if err != nil || !report.Ready {
		t.Fatalf("DOCKER_CONTEXT did not take precedence: %+v, %v", report, err)
	}
}

func TestDoctorInheritedCancellation(t *testing.T) {
	f := newDoctorFixture()
	f.deps.run = func(ctx context.Context, _ string, _ ...string) (string, error) { return "", ctx.Err() }
	f.deps.dial = func(ctx context.Context, _ string) error { return ctx.Err() }
	command := newDoctorCommandWithDependencies(f.deps)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command.SetContext(ctx)
	command.SetArgs([]string{"--json"})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(io.Discard)
	if err := command.Execute(); err == nil {
		t.Fatal("canceled probes reported success")
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if check := doctorCheckNamed(t, report, "auth_tcp"); check.Status != "fail" || !strings.Contains(check.Detail, "context canceled") {
		t.Fatalf("TCP probe did not honor cancellation: %+v", check)
	}
}

func TestDoctorTextProvidesRemediesAndScope(t *testing.T) {
	f := newDoctorFixture()
	f.deps.goos = "darwin"
	command := newDoctorCommandWithDependencies(f.deps)
	command.SetArgs([]string{})
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	if err := command.Execute(); err == nil {
		t.Fatal("non-Linux runtime reported success")
	}
	for _, want := range []string{"FAIL linux_runtime:", "Remedy: Run agent-wow inside a Linux VM", "PASS auth_tcp: TCP connection", "login, worldserver, and gameplay are not checked"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("text report missing %q: %s", want, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("rendered failure report was followed by Cobra noise: %s", stderr.String())
	}
}

func doctorCheckNamed(t *testing.T, report doctorReport, name string) doctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing check %q: %+v", name, report.Checks)
	return doctorCheck{}
}
