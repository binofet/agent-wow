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

	buddyactions "github.com/agent-wow/agent-wow/modules/buddy-actions"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
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
	if moduleSocket == "" || sessionSocket == "" {
		return errors.New("session-owned module/session sockets are required")
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
	service := buddyactions.NewService(modv1.NewSessionClient(connection), identity.SelfGUID)
	if identity.Independent() {
		service = buddyactions.NewIndependentService(modv1.NewSessionClient(connection), identity.SelfGUID)
	}
	service.SetNavigationDataDir(os.Getenv("BUDDY_NAV_DATA_DIR"))
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, service)
	readiness := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, readiness)
	readiness.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	go service.Run(ctx)
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
