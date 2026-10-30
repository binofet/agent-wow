package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	buddyworld "github.com/agent-wow/agent-wow/modules/buddy-world"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
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
	guid, err := strconv.ParseUint(os.Getenv("AGENT_WOW_CHARACTER_GUID"), 10, 64)
	if err != nil || guid == 0 || guid == buddyworld.OwnerGUID {
		return errors.New("AGENT_WOW_CHARACTER_GUID must identify the companion character")
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
	worldv1.RegisterBuddyWorldServer(server, buddyworld.NewService(guid))
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
