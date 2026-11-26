package modrt

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestPolicyClosePreservesOnlyFailedUnreconciledGatherCustody(t *testing.T) {
	for _, mode := range []string{"failed_unverified", "failed_after_reload", "acknowledged_reservation", "known_not_dispatched_then_failure"} {
		t.Run(mode, func(t *testing.T) {
			registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
			if err != nil {
				t.Fatal(err)
			}
			var host *SessionPolicy
			m, p, calls := policyGatherBinding(t, registry, func(ctx context.Context, _ string, request, result proto.Message) error {
				raw, _ := protojson.Marshal(request)
				typed := &lootv1.OpenGatherRequest{}
				if err := protojson.Unmarshal(raw, typed); err != nil {
					t.Fatal(err)
				}
				if mode == "failed_after_reload" {
					before := host.vm.Status().Revision
					if _, err := host.Reload(ctx, idleInteractionPolicy+"\n-- replacement while original source request waits", before); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "failed_unverified" || mode == "failed_after_reload" {
					return status.Error(codes.Unavailable, "source admission outcome unavailable")
				}
				native := &lootv1.CorpseOperation{SessionId: typed.SessionId, SelfGuid: typed.SelfGuid, SourceGuid: typed.SourceGuid, RequestId: typed.RequestId, Phase: "gather_approaching"}
				if mode == "known_not_dispatched_then_failure" {
					native.Phase = "not_dispatched"
					native.Terminal = true
				}
				raw, _ = proto.Marshal(native)
				return proto.Unmarshal(raw, result)
			})
			host = p
			original := p.vm.Status()
			_, receipt, err := p.Step(context.Background(), map[string]any{"request": policyGatherAction()}, nil)
			if err != nil || receipt == nil || *calls != 1 {
				t.Fatal(receipt, err, *calls)
			}
			if mode == "known_not_dispatched_then_failure" {
				m.fail("buddy-loot", errors.New("later independent module failure"))
			}
			// Join the exact maintained shutdown callback; no sleeps or scheduler race.
			p.close()
			state, pending := p.vm.Inspect()
			dispatch := p.dispatchSnapshot(state)
			if !state.Closed || dispatch == nil || dispatch.ActionID != strconv.FormatUint(receipt.ActionID, 10) || dispatch.Revision != original.Revision ||
				dispatch.Payload["source_guid"] != strconv.FormatUint(policyGatherSource, 10) || dispatch.Payload["request_id"] != "gather-selected-1" ||
				dispatch.Payload["opening_transport_attempted"] != false || dispatch.Payload["transport_written"] != false {
				t.Fatal(state, pending, dispatch)
			}
			failed := mode == "failed_unverified" || mode == "failed_after_reload"
			if failed {
				if m.Err() == nil || p.lifetime.Err() == nil || dispatch.TransportOutcome != behavior.ActionSettledUnverified || dispatch.Payload["reconciliationRequired"] != true ||
					state.Pending != 1 || state.Buffered != 1 || state.Cursor != 0 || len(pending) != 1 || pending[0].ActionID != receipt.ActionID || pending[0].Outcome != behavior.ActionReserved {
					t.Fatal("failed source custody was consumed", state, pending, dispatch)
				}
				if mode == "failed_after_reload" && dispatch.SameRevisionAsState {
					t.Fatal("original dispatch borrowed replacement source", dispatch)
				}
			} else if state.Pending != 0 || state.Buffered != 0 || state.Cursor != 1 || len(pending) != 0 {
				t.Fatal("normal terminal receipt was not drained", state, pending, dispatch)
			}
			if mode == "known_not_dispatched_then_failure" && dispatch.TransportOutcome != behavior.ActionNotDispatched {
				t.Fatal("known refusal became unknown", dispatch)
			}
			if mode == "acknowledged_reservation" {
				m.cancel()
			}
			if _, next, err := p.Step(context.Background(), nil, nil); !errors.Is(err, behavior.ErrSessionClosed) || next != nil || *calls != 1 ||
				!reflect.DeepEqual(dispatch, p.dispatchSnapshot(p.vm.Status())) {
				t.Fatal("closed host replayed or changed custody", err, next, *calls)
			}
		})
	}
}
