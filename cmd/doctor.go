package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/agent-wow/agent-wow/internal/config"
	"github.com/spf13/cobra"
)

const doctorMaxTimeout = time.Minute

type doctorCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
	Remedy   string `json:"remedy,omitempty"`
	Required bool   `json:"required"`
}

type doctorReport struct {
	Ready  bool          `json:"ready"`
	Scope  string        `json:"scope"`
	Checks []doctorCheck `json:"checks"`
}

// Keep probes injectable so tests never need Docker or an existing WoW account.
type doctorDependencies struct {
	goos       string
	lookPath   func(string) (string, error)
	run        func(context.Context, string, ...string) (string, error)
	getenv     func(string) string
	loadConfig func() (config.Config, error)
	dial       func(context.Context, string) error
}

func newDoctorCommand() *cobra.Command {
	return newDoctorCommandWithDependencies(doctorDependencies{
		goos:     runtime.GOOS,
		lookPath: exec.LookPath,
		run:      runDoctorCommand,
		getenv:   os.Getenv,
		loadConfig: func() (config.Config, error) {
			if err := config.Init(cfgFile); err != nil {
				return config.Config{}, err
			}
			return config.Get(), nil
		},
		dial: func(ctx context.Context, address string) error {
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
			if err != nil {
				return err
			}
			return conn.Close()
		},
	})
}

func newDoctorCommandWithDependencies(deps doctorDependencies) *cobra.Command {
	var asJSON bool
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Check Linux, local Docker, Compose, and authserver TCP reachability",
		Long: `Check the documented runtime prerequisites without starting containers or logging in.

TCP reachability does not verify the WoW protocol, credentials, realm/worldserver,
or gameplay. No account or password files are read or created.`,
		Args: cobra.NoArgs,
		// Load configuration inside RunE so configuration failures also produce
		// a complete JSON report, rather than the root's plain-text error.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			report := doctorReport{
				Ready:  true,
				Scope:  "Runtime prerequisites and authserver TCP reachability only; login, worldserver, and gameplay are not checked.",
				Checks: []doctorCheck{},
			}
			if timeout <= 0 || timeout > doctorMaxTimeout {
				report.add("timeout", "fail", "--timeout must be greater than zero and at most 1m", "Use --timeout 5s (or another positive duration up to 1m).")
			} else {
				ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
				defer cancel()
				runDoctorChecks(ctx, deps, &report)
			}
			if err := writeDoctorReport(cmd.OutOrStdout(), report, asJSON); err != nil {
				return fmt.Errorf("write doctor report: %w", err)
			}
			if !report.Ready {
				return errors.New("doctor: one or more required checks failed")
			}
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "Output a JSON report, including failed checks")
	command.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "Maximum total duration for Docker/network probes (greater than zero, at most 1m)")
	return command
}

func (r *doctorReport) add(name, status, detail, remedy string) {
	r.Checks = append(r.Checks, doctorCheck{Name: name, Status: status, Detail: detail, Remedy: remedy, Required: true})
	if status != "pass" {
		r.Ready = false
	}
}

func runDoctorChecks(ctx context.Context, deps doctorDependencies, report *doctorReport) {
	if deps.goos == "linux" {
		report.add("linux_runtime", "pass", "Client is running on Linux.", "")
	} else {
		report.add("linux_runtime", "fail", "Client is running on "+deps.goos+"; gameplay modules require Linux.", "Run agent-wow inside a Linux VM or on a Linux host with a local Docker Engine.")
	}

	cfg, configErr := deps.loadConfig()
	if configErr != nil {
		report.add("configuration", "fail", "Could not load configuration: "+configErr.Error(), "Correct --config and the AGENT_WOW_* settings, including authserver.host and authserver.port.")
	} else {
		report.add("configuration", "pass", "Configuration loaded; no credential files inspected.", "")
	}

	dockerPath, pathErr := deps.lookPath("docker")
	if pathErr != nil {
		report.add("docker_cli", "fail", "Docker CLI is not available on PATH.", "Install Docker Engine and the Docker Compose plugin on the Linux host, then make docker available on PATH.")
		for _, name := range []string{"docker_endpoint", "docker_daemon", "docker_compose"} {
			report.add(name, "skip", "Not checked because the Docker CLI is unavailable.", "Install Docker Engine and its Compose plugin, then rerun agent-wow doctor.")
		}
	} else {
		version, err := deps.run(ctx, dockerPath, "--version")
		if err != nil {
			report.add("docker_cli", "fail", "Docker CLI could not run: "+err.Error(), "Check the docker executable on PATH and reinstall Docker Engine if necessary.")
		} else {
			report.add("docker_cli", "pass", "Docker CLI: "+version, "")
		}
		checkDoctorDockerEndpoint(ctx, deps, dockerPath, report)
		version, err = deps.run(ctx, dockerPath, "info", "--format", "{{.ServerVersion}}")
		if err != nil {
			report.add("docker_daemon", "fail", "Docker daemon is not reachable: "+err.Error(), "Start the local Docker Engine and grant this user access to its Unix socket; verify docker info succeeds.")
		} else {
			report.add("docker_daemon", "pass", "Docker daemon responds; server version: "+version, "")
		}
		version, err = deps.run(ctx, dockerPath, "compose", "version", "--short")
		if err != nil {
			report.add("docker_compose", "fail", "Docker Compose plugin could not run: "+err.Error(), "Install the Docker Compose plugin for this Docker CLI; verify docker compose version succeeds.")
		} else {
			report.add("docker_compose", "pass", "Docker Compose version: "+version, "")
		}
	}

	if configErr != nil {
		report.add("auth_tcp", "skip", "Authserver TCP reachability was not checked because configuration is invalid.", "Correct the configuration, then rerun agent-wow doctor.")
		return
	}
	address := net.JoinHostPort(cfg.AuthServer.Host, strconv.Itoa(cfg.AuthServer.Port))
	if err := deps.dial(ctx, address); err != nil {
		report.add("auth_tcp", "fail", "Authserver TCP connection to "+address+" failed: "+err.Error(), "Check authserver.host/port, start the existing authserver if needed, and check the route/firewall to its TCP port.")
	} else {
		report.add("auth_tcp", "pass", "TCP connection to "+address+" succeeded; WoW protocol and login have not been verified.", "")
	}
}

func checkDoctorDockerEndpoint(ctx context.Context, deps doctorDependencies, dockerPath string, report *doctorReport) {
	host := deps.getenv("DOCKER_HOST")
	if host == "" || deps.getenv("DOCKER_CONTEXT") != "" {
		var err error
		host, err = deps.run(ctx, dockerPath, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
		if err != nil {
			report.add("docker_endpoint", "fail", "Could not inspect the Docker endpoint: "+err.Error(), "Select a local Docker context on the Linux host; verify docker context inspect succeeds.")
			return
		}
	}
	if !strings.HasPrefix(host, "unix://") {
		report.add("docker_endpoint", "fail", "The selected Docker endpoint is not a local Unix socket; module socket mounts require a local engine.", "Select a local Docker context or set DOCKER_HOST to its unix:/// socket; remove any conflicting DOCKER_CONTEXT override.")
		return
	}
	report.add("docker_endpoint", "pass", "Docker uses a local Unix socket endpoint.", "")
}

// Limit both probe duration and captured output. WaitDelay also bounds waiting
// for inherited output pipes after the context kills an unresponsive command.
func runDoctorCommand(ctx context.Context, path string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, path, args...)
	command.WaitDelay = time.Second
	var output doctorOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	err := command.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return strings.TrimSpace(string(output)), err
}

type doctorOutput []byte

func (out *doctorOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - len(*out); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		*out = append(*out, p...)
	}
	return n, nil
}

func writeDoctorReport(out io.Writer, report doctorReport, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	var text strings.Builder
	for _, check := range report.Checks {
		fmt.Fprintf(&text, "%s %s: %s\n", strings.ToUpper(check.Status), check.Name, check.Detail)
		if check.Remedy != "" {
			fmt.Fprintf(&text, "  Remedy: %s\n", check.Remedy)
		}
	}
	if report.Ready {
		fmt.Fprintln(&text, "All prerequisite checks passed.")
	} else {
		fmt.Fprintln(&text, "Required prerequisites are unavailable; resolve the checks above and rerun doctor.")
	}
	fmt.Fprintln(&text, report.Scope)
	_, err := io.WriteString(out, text.String())
	return err
}

func init() { rootCmd.AddCommand(newDoctorCommand()) }
