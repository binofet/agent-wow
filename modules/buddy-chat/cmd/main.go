package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	buddychat "github.com/agent-wow/agent-wow/modules/buddy-chat"
	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	"github.com/agent-wow/agent-wow/pkg/buddyidentity"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
	moduleSocket, sessionSocket := os.Getenv("AGENT_WOW_MODULE_SOCKET"), os.Getenv("AGENT_WOW_SESSION_SOCKET")
	if moduleSocket == "" || sessionSocket == "" {
		return errors.New("AGENT_WOW_MODULE_SOCKET and AGENT_WOW_SESSION_SOCKET are required; start through agent-wow char play")
	}
	identity, err := buddyidentity.Parse(os.Getenv("AGENT_WOW_CHARACTER_GUID"), os.Getenv("BUDDY_INDEPENDENT"))
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	listener, err := net.Listen("unix", moduleSocket)
	if err != nil {
		return err
	}
	defer listener.Close()
	// The core owns a private per-module directory and uses a different UID.
	if err := os.Chmod(moduleSocket, 0666); err != nil {
		return err
	}
	connection, err := grpc.NewClient("unix://"+sessionSocket, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxRetryRPCBufferSize(0)))
	if err != nil {
		return err
	}
	defer connection.Close()
	server := grpc.NewServer()
	service := buddychat.NewService(modv1.NewSessionClient(connection), identity.SelfGUID)
	if identity.Independent() {
		service = buddychat.NewIndependentService(modv1.NewSessionClient(connection), identity.SelfGUID)
	}
	chatv1.RegisterBuddyChatServer(server, service)
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
