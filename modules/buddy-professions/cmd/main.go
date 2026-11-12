package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	buddyprofessions "github.com/agent-wow/agent-wow/modules/buddy-professions"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	"github.com/agent-wow/agent-wow/pkg/buddyidentity"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/types/known/emptypb"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	moduleSocket, sessionSocket := os.Getenv("AGENT_WOW_MODULE_SOCKET"), os.Getenv("AGENT_WOW_SESSION_SOCKET")
	sessionID := os.Getenv("AGENT_WOW_SESSION_ID")
	if moduleSocket == "" || sessionSocket == "" || sessionID == "" {
		return errors.New("session-owned sockets and session ID are required")
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
	if err := os.Chmod(moduleSocket, 0666); err != nil {
		return err
	}
	connection, err := grpc.NewClient("unix://"+sessionSocket, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxRetryRPCBufferSize(0)))
	if err != nil {
		return err
	}
	defer connection.Close()
	service := buddyprofessions.NewService(modv1.NewSessionClient(connection), sessionID, identity.SelfGUID)
	server := grpc.NewServer()
	professionsv1.RegisterBuddyProfessionsServer(server, service)
	readiness := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, readiness)
	readiness.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	go func() {
		<-ctx.Done()
		readiness.Shutdown()
		stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = service.BeforeLogout(stop, &emptypb.Empty{})
		server.Stop()
	}()
	err = server.Serve(listener)
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}
