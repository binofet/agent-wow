package modcb

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

const OwnMovementReportMethod = "own_movement_report"
const BodyInputReportMethod = "body_input_movement_report"
const BodyInputUpdateTrailer = "agentwow-parsed-body-input-update"
const BodyInputBarrierTrailer = "agentwow-body-input-barrier"
const BodyInputFieldTrailer = "agentwow-body-input-field"
const MaxBodyInputFields = 3

// A row carries only a parsed field index and optionally its public numeric
// values. The World parser never emits actor identifiers or private values.
var bodyFieldDetail = regexp.MustCompile(`^0x[01][0-9a-f]{3}(=(0x[0-9a-f]{8}|[?])>0x[0-9a-f]{8})?$`)

func BodyUpdateDetail(class string, fields []string) string {
	if class != BodyUpdateSelfField || len(fields) == 0 || len(fields) > MaxBodyInputFields+1 {
		return class
	}
	for i, field := range fields {
		if i == MaxBodyInputFields && field == "more" {
			continue
		}
		if i == MaxBodyInputFields || !bodyFieldDetail.MatchString(field) {
			return class
		}
	}
	return class + " " + strings.Join(fields, " ")
}

const (
	BodyUpdateHarmless       = "harmless_values"
	BodyUpdateMissing        = "world_proof_missing"
	BodyUpdateMalformed      = "malformed_object_update"
	BodyUpdateUnbound        = "unbound_object_update"
	BodyUpdateMovement       = "object_movement"
	BodyUpdateLifecycle      = "object_lifecycle"
	BodyUpdateSelfField      = "own_restricted_field"
	BodyUpdateOwnerField     = "owner_restricted_field"
	BodyUpdateUnknownSubject = "unknown_update_subject"
)

// Only fixed diagnostics cross the World callback boundary.
func BodyUpdateResult(positive, reason []string) string {
	if len(positive) == 1 && positive[0] == "1" && len(reason) == 0 {
		return BodyUpdateHarmless
	}
	if len(positive) == 0 && len(reason) == 1 {
		switch reason[0] {
		case BodyUpdateMalformed, BodyUpdateUnbound, BodyUpdateMovement, BodyUpdateLifecycle, BodyUpdateSelfField, BodyUpdateOwnerField, BodyUpdateUnknownSubject:
			return reason[0]
		}
	}
	return BodyUpdateMissing
}

// ReadOwnMovementReport reads the module's own session socket, not a public
// JSON method or a gameplay mutation. The bytes and their original transport
// correlation are inseparable; this must never freshen an old World image.
func ReadOwnMovementReport(ctx context.Context, client FollowPolicyClient, sid string, self uint64) (*modv1.WorldPacket, error) {
	return readOwnMovementReport(ctx, client, sid, self, "", OwnMovementReportMethod)
}

func ReadOwnMovementAttempt(ctx context.Context, client FollowPolicyClient, sid string, self uint64, id string) (*modv1.WorldPacket, error) {
	if !ValidMovementAttempt(id) {
		return nil, errors.New("exact movement attempt required")
	}
	return readOwnMovementReport(ctx, client, sid, self, id, OwnMovementReportMethod)
}
func ReadBodyInputReport(ctx context.Context, client FollowPolicyClient, sid string, self uint64, id string) (*modv1.WorldPacket, error) {
	if id != "" && !ValidMovementAttempt(id) {
		return nil, errors.New("exact body input attempt required")
	}
	return readOwnMovementReport(ctx, client, sid, self, id, BodyInputReportMethod)
}
func readOwnMovementReport(ctx context.Context, client FollowPolicyClient, sid string, self uint64, id, method string) (*modv1.WorldPacket, error) {
	if sid == "" || len(sid) > 128 || self == 0 {
		return nil, errors.New("current movement-report body required")
	}
	fields := map[string]any{"sessionId": sid, "selfGuid": strconv.FormatUint(self, 10)}
	if id != "" {
		fields["attemptId"] = id
	}
	value, err := structpb.NewStruct(fields)
	if err != nil {
		return nil, err
	}
	packed, err := anypb.New(value)
	if err != nil {
		return nil, err
	}
	response, err := client.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: "session", Method: method, Request: packed})
	if err != nil {
		return nil, err
	}
	packet := &modv1.WorldPacket{}
	if response.GetResult() == nil || response.Result.UnmarshalTo(packet) != nil || len(packet.Payload) == 0 || len(packet.Payload) > 256 ||
		packet.TransportScope == nil || packet.TransportScope.SelfGuid != self || packet.TransportScope.ConnectionId == "" ||
		!packet.TransportScope.VisitBound || packet.TransportScope.VisitRevision == 0 {
		return nil, errors.New("current own movement report unavailable")
	}
	return packet, nil
}
