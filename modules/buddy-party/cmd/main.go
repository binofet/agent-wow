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

	buddyparty "github.com/agent-wow/agent-wow/modules/buddy-party"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
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
	sessionID, selfName := os.Getenv("AGENT_WOW_SESSION_ID"), os.Getenv("AGENT_WOW_CHARACTER_NAME")
	selfGUID, err := strconv.ParseUint(os.Getenv("AGENT_WOW_CHARACTER_GUID"), 10, 64)
	if moduleSocket == "" || sessionSocket == "" || sessionID == "" || selfName == "" || err != nil || selfGUID == 0 || selfGUID>>48 != 0 || uint32(selfGUID) == 0 {
		return errors.New("module/session sockets, session ID, and player character identity are required; start through agent-wow char play")
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
	server := grpc.NewServer()
	partyv1.RegisterBuddyPartyServer(server, buddyparty.NewService(modv1.NewSessionClient(connection), sessionID, selfGUID, selfName))
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
