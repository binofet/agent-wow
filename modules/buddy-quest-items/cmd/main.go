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
	"time"

	buddyquestitems "github.com/agent-wow/agent-wow/modules/buddy-quest-items"
	questitemsv1 "github.com/agent-wow/agent-wow/modules/buddy-quest-items/api"
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

type runtimeConfig struct {
	moduleSocket, sessionSocket, sessionID string
	self                                   uint64
}

func readRuntimeConfig(getenv func(string) string) (runtimeConfig, error) {
	config := runtimeConfig{moduleSocket: getenv("AGENT_WOW_MODULE_SOCKET"), sessionSocket: getenv("AGENT_WOW_SESSION_SOCKET"), sessionID: getenv("AGENT_WOW_SESSION_ID")}
	self, err := strconv.ParseUint(getenv("AGENT_WOW_CHARACTER_GUID"), 10, 64)
	if config.moduleSocket == "" || config.sessionSocket == "" || len(config.sessionID) == 0 || len(config.sessionID) > 128 || err != nil || self == 0 || self>>48 != 0 {
		return runtimeConfig{}, errors.New("session-owned sockets, session ID and player identity are required")
	}
	for _, b := range []byte(config.sessionID) {
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.') {
			return runtimeConfig{}, errors.New("bounded runtime session ID is required")
		}
	}
	config.self = self
	return config, nil
}

func run() error {
	config, err := readRuntimeConfig(os.Getenv)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	listener, err := net.Listen("unix", config.moduleSocket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(config.moduleSocket, 0666); err != nil {
		return err
	}
	connection, err := grpc.NewClient("unix://"+config.sessionSocket, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxRetryRPCBufferSize(0)))
	if err != nil {
		return err
	}
	defer connection.Close()
	service := buddyquestitems.NewService(modv1.NewSessionClient(connection), config.sessionID, config.self)
	server := grpc.NewServer()
	questitemsv1.RegisterBuddyQuestItemsServer(server, service)
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
