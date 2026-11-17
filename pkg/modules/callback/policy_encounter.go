package modcb

import (
	"context"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	PolicyEncounterKeyEnv = "AGENT_WOW_POLICY_ENCOUNTER_KEY"
	policyEncounterKey    = "agentwow-encounter-key"
	policyEncounterVisit  = "agentwow-encounter-visit"
	encounterLeaseService = "agentwow.policy.encounter.v1.Lease"
	encounterLeaseMethod  = "/" + encounterLeaseService + "/Renew"
)

// The heartbeat is private to the host and Actions process. It is never
// declared in the reflected module catalog or reachable through the JSON RPC.
type EncounterLeaseServer interface {
	RenewPolicyEncounterLease(context.Context, *wrapperspb.UInt64Value) (*emptypb.Empty, error)
}

func RegisterEncounterLeaseServer(server *grpc.Server, lease EncounterLeaseServer) {
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: encounterLeaseService,
		HandlerType: (*EncounterLeaseServer)(nil),
		Methods:     []grpc.MethodDesc{{MethodName: "Renew", Handler: receiveEncounterLease}},
	}, lease)
}

func receiveEncounterLease(server any, ctx context.Context, decode func(any) error, intercept grpc.UnaryServerInterceptor) (any, error) {
	request := new(wrapperspb.UInt64Value)
	if err := decode(request); err != nil {
		return nil, err
	}
	lease := server.(EncounterLeaseServer)
	if intercept == nil {
		return lease.RenewPolicyEncounterLease(ctx, request)
	}
	return intercept(ctx, request, &grpc.UnaryServerInfo{Server: server, FullMethod: encounterLeaseMethod}, func(ctx context.Context, req any) (any, error) {
		return lease.RenewPolicyEncounterLease(ctx, req.(*wrapperspb.UInt64Value))
	})
}

func RenewPolicyEncounterLease(ctx context.Context, conn grpc.ClientConnInterface, key string, visit uint64) error {
	ctx = metadata.AppendToOutgoingContext(ctx, policyEncounterKey, key)
	return conn.Invoke(ctx, encounterLeaseMethod, wrapperspb.UInt64(visit), &emptypb.Empty{})
}

func WithPolicyEncounterAccess(ctx context.Context, key string, visit uint64) context.Context {
	return metadata.AppendToOutgoingContext(ctx, policyEncounterKey, key, policyEncounterVisit, strconv.FormatUint(visit, 10))
}

func IncomingPolicyEncounterKey(ctx context.Context) string {
	keys := metadata.ValueFromIncomingContext(ctx, policyEncounterKey)
	if len(keys) != 1 || len(keys[0]) != 64 {
		return ""
	}
	return keys[0]
}

func IncomingPolicyEncounterVisit(ctx context.Context) uint64 {
	visits := metadata.ValueFromIncomingContext(ctx, policyEncounterVisit)
	if len(visits) != 1 {
		return 0
	}
	visit, _ := strconv.ParseUint(visits[0], 10, 64)
	return visit
}
