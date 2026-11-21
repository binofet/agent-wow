package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	buddyworld "github.com/agent-wow/agent-wow/modules/buddy-world"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddyidentity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	socket := os.Getenv("AGENT_WOW_MODULE_SOCKET")
	if socket == "" {
		return errors.New("AGENT_WOW_MODULE_SOCKET is required; start through agent-wow char play")
	}
	identity, err := buddyidentity.Parse(os.Getenv("AGENT_WOW_CHARACTER_GUID"), os.Getenv("BUDDY_INDEPENDENT"))
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	// The runtime creates a private per-module directory; core uses another UID.
	if err := os.Chmod(socket, 0666); err != nil {
		return err
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(4 << 20))
	service := buddyworld.NewService(identity.SelfGUID)
	if identity.Independent() {
		service = buddyworld.NewIndependentService(identity.SelfGUID)
	}
	// Retain raw combat evidence in a persistent named volume. Failure to
	// persist is exposed by the public stream; it must not disconnect a player.
	sessionID := os.Getenv("AGENT_WOW_SESSION_ID")
	if sessionID == "" || filepath.Base(sessionID) != sessionID || sessionID == "." || sessionID == ".." {
		return errors.New("AGENT_WOW_SESSION_ID must be a single nonempty path component")
	}
	journal, journalErr := os.OpenFile(filepath.Join("/var/lib/buddy-combat-events", sessionID+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if journalErr == nil {
		defer journal.Close()
		service.ConfigureCombatJournal(sessionID, journal)
	} else {
		service.ConfigureCombatJournal(sessionID, nil)
		fmt.Fprintln(os.Stderr, "combat journal persistence unavailable:", journalErr)
	}
	worldv1.RegisterBuddyWorldServer(server, service)
	readiness := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, readiness)
	readiness.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	go func() { <-ctx.Done(); readiness.Shutdown(); server.Stop() }()
	err = server.Serve(listener)
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}
