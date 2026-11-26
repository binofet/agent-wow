package modcb

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

const OwnMovementReportMethod = "own_movement_report"
const BodyInputReportMethod = "body_input_movement_report"
const SettledBodyInputReportMethod = "settled_body_input_movement_report"
const SelfBodyInputReportMethod = "self_body_input_movement_report"
const BodyInputUpdateTrailer = "agentwow-parsed-body-input-update"
const BodyInputBarrierTrailer = "agentwow-body-input-barrier"
const BodyInputFieldTrailer = "agentwow-body-input-field"
const OwnerPositionTrailer = "agentwow-observed-owner-position-bin"
const MaxOwnerPositions = 32
const MaxBodyInputFields = 3

// A row carries only a parsed field index and optionally its public numeric
// values. The World parser never emits actor identifiers or private values.
var bodyFieldDetail = regexp.MustCompile(`^0x[01][0-9a-f]{3}(=(0x[0-9a-f]{8}|[?])>0x[0-9a-f]{8})?$`)

// Build-12340 public, non-identity scalars. Both module boundaries apply
// the same policy. An index outside this set carries no previous/new value.
func PublicBodyDiagnosticField(field int) bool {
	switch field {
	case 0x16, 0x17, 0x18, 0x20, 0x36, 0x37, 0x3b, 0x3c, 0x3d, 0x3e, 0x3f,
		0x41, 0x42, 0x43, 0x44, 0x45, 0x4a, 0x4f, 0x52, 0x53, 0x7a, 0x96:
		return true
	}
	return false
}

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
		index, err := strconv.ParseUint(field[2:6], 16, 16)
		if err != nil || index >= 255*32 || len(field) > 6 && !PublicBodyDiagnosticField(int(index)) {
			return class
		}
	}
	return class + " " + strings.Join(fields, " ")
}

const (
	BodyUpdateHarmless       = "harmless_values"
	BodyUpdateMissing        = "world_proof_missing"
	BodyUpdatePosture        = "own_stand_state"
	BodyUpdateMalformed      = "malformed_object_update"
	BodyUpdateUnbound        = "unbound_object_update"
	BodyUpdateMovement       = "object_movement"
	BodyUpdateLifecycle      = "object_lifecycle"
	BodyUpdateSelfField      = "own_restricted_field"
	BodyUpdateOwnerField     = "owner_restricted_field"
	BodyUpdateOwnerMovement  = "owner_movement"
	BodyUpdateOwnerTravel    = "owner_route_position"
	BodyUpdateOwnerLifecycle = "owner_lifecycle"
	BodyUpdateUnknownSubject = "unknown_update_subject"
)

func BodyUpdateIsOwnerOnly(class string) bool {
	return class == BodyUpdateOwnerField || class == BodyUpdateOwnerMovement || class == BodyUpdateOwnerLifecycle || class == BodyUpdateOwnerTravel
}

// Only the physical writer's definite refusal for the followed owner permits
// an independent ground STOP. A lost reply can never authorize that cleanup.
func RouteOwnerChangeBeforeWrite(err error) bool {
	reason, ok := strings.CutPrefix(status.Convert(err).Message(), "body input before write: ")
	return ok && PacketWasNotDispatched(err) && BodyUpdateIsOwnerOnly(reason)
}

// Only fixed diagnostics cross the World callback boundary.
func BodyUpdateResult(positive, reason []string) string {
	if len(positive) == 1 && positive[0] == "1" && len(reason) == 0 {
		return BodyUpdateHarmless
	}
	if len(positive) == 0 && len(reason) == 1 {
		switch reason[0] {
		case BodyUpdatePosture, BodyUpdateMalformed, BodyUpdateUnbound, BodyUpdateMovement, BodyUpdateLifecycle, BodyUpdateSelfField, BodyUpdateOwnerField, BodyUpdateOwnerMovement, BodyUpdateOwnerTravel, BodyUpdateOwnerLifecycle, BodyUpdateUnknownSubject:
			return reason[0]
		}
	}
	return BodyUpdateMissing
}

// The complete World parser supplies every ordinary owner position in a
// committed object batch. An absent or malformed list supplies no geometry.
func ObservedOwnerPositions(class string, rows []string) []movementinfo.FollowPosition {
	if class != BodyUpdateOwnerTravel || len(rows) == 0 || len(rows) > MaxOwnerPositions {
		return nil
	}
	points := make([]movementinfo.FollowPosition, 0, len(rows))
	for _, row := range rows {
		p, ok := movementinfo.ReadFollowPosition(row)
		if !ok {
			return nil
		}
		points = append(points, p)
	}
	return points
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
func ReadBodyInputReport(ctx context.Context, client FollowPolicyClient, sid string, self uint64, id string, selfOnly ...bool) (*modv1.WorldPacket, error) {
	if id != "" && !ValidMovementAttempt(id) {
		return nil, errors.New("exact body input attempt required")
	}
	method := BodyInputReportMethod
	if len(selfOnly) == 1 && selfOnly[0] {
		method = SelfBodyInputReportMethod
	}
	return readOwnMovementReport(ctx, client, sid, self, id, method)
}

// ReadSettledBodySource reads the last actual stationary STOP only for the
// source of a newly selected input. It cannot continue an interrupted frame.
func ReadSettledBodySource(ctx context.Context, client FollowPolicyClient, sid string, self uint64) (*modv1.WorldPacket, error) {
	return readOwnMovementReport(ctx, client, sid, self, "", SettledBodyInputReportMethod)
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
