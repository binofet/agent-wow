package modrt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

func disposalTestSource(export string) string {
	return `local calls,events=0,0
return {` + export + `
 on_event=function(e)events=events+1 return true end,
 decide=function(f)calls=calls+1 return {action={kind="test"}} end,
 export_state=function()return {version=1,data={calls=calls,events=events}} end,
 import_state=function(c)calls=c.data.calls or 0 events=c.data.events or 0 return true end}`
}

func disposalTestHost(t *testing.T, sid, source string) (*Manager, *SessionPolicy) {
	t.Helper()
	m := catalogManager(t, 4, sid)
	t.Cleanup(m.cancel)
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	return m, host
}

func disposalTestRequest(t *testing.T, fields map[string]any) *modv1.InvokeModuleRequest {
	t.Helper()
	value, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := anypb.New(value)
	if err != nil {
		t.Fatal(err)
	}
	return &modv1.InvokeModuleRequest{Module: modcb.DisposalPolicyModule, Method: modcb.DisposalPolicyMethod, Request: packed}
}

func disposalTestInput(sid string) map[string]any {
	return map[string]any{"sessionId": sid, "selfGuid": "4", "mode": "misc_destroy",
		"item": map[string]any{"itemId": float64(99000), "available": true, "itemClass": float64(15), "rawTemplateFacts": map[string]any{}}}
}

func TestPolicyDisposalActualBundleUsesCurrentAcceptedClassifier(t *testing.T) {
	ctx := context.Background()
	source := followFallbackBundle(t)
	m, host := disposalTestHost(t, "disposal-bundle", source)
	writes := 0
	m.session.SendPacket = func(context.Context, uint32, []byte) error { writes++; return nil }
	input := disposalTestInput("disposal-bundle")
	input["eligible"], input["gear"] = true, map[string]any{"eligible": true}
	check := func(want string) {
		t.Helper()
		available, revision, out, err := host.disposalDecision(ctx, input)
		if err != nil || !available || revision != host.vm.Status().Revision || out["status"] != want {
			t.Fatal("accepted bundle classifier unavailable or incorrect", available, revision, out, err)
		}
	}
	check("eligible")
	input["mode"] = "vendor_sale"
	check("keep") // This observed template has no positive sale price.
	input["mode"] = "misc_destroy"
	delete(input["item"].(map[string]any), "rawTemplateFacts")
	check("unknown") // A caller flag cannot supply absent template coverage.
	input["item"].(map[string]any)["rawTemplateFacts"] = map[string]any{}
	input["mode"] = "gray_armor_destroy"
	check("unknown") // No owned armor, quest or equipment context was supplied.
	input["mode"] = "misc_destroy"
	before, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check("eligible")
	assertFollowFallbackState(t, host, before)
	old := host.vm.Status().Revision
	changed := strings.Replace(source, `return result("eligible","ordinary_gray_miscellaneous")`, `return result("keep","edited_disposal_choice")`, 1)
	if changed == source {
		t.Fatal("actual bundled classifier edit did not match")
	}
	revision, err := host.Reload(ctx, changed, old)
	if err != nil || revision == old {
		t.Fatal("same-body accepted source did not change", revision, err)
	}
	check("keep")
	if _, err := host.Reload(ctx, source, old); !errors.Is(err, behavior.ErrRevisionConflict) {
		t.Fatal("stale source CAS was accepted", err)
	}
	if _, err := host.Reload(ctx, "return {", revision); err == nil {
		t.Fatal("invalid replacement source was accepted")
	}
	check("keep")
	if host.vm.Status().Revision != revision || writes != 0 || len(host.vm.Outstanding()) != 0 {
		t.Fatal("pure classification reserved or wrote an action", host.vm.Status(), writes)
	}
}

func TestPolicyDisposalReadsPreservePendingLedgerAndDoNotWaitForAdvance(t *testing.T) {
	ctx := context.Background()
	source := disposalTestSource(`disposal_evaluation=function(input)return {status="keep",reason="current"} end,`)
	_, host := disposalTestHost(t, "disposal-pending", source)
	_, receipt, err := host.Step(ctx, nil, func(map[string]any) (PolicyDispatch, error) {
		return PolicyDispatch{Module: "buddy-trade", Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			return behavior.ActionUnknown, map[string]any{"requestId": "pending-disposal"}
		}}, nil
	})
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionUnknown {
		t.Fatal("fixture did not reserve uncertainty", receipt, err)
	}
	if err := host.vm.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"buffered": true}}); err != nil {
		t.Fatal(err)
	}
	before, err := host.vm.State(ctx)
	if err != nil || before.Status.Buffered != 1 || len(before.Pending) != 1 {
		t.Fatal("fixture lost pending action or buffered event", before, err)
	}
	done := make(chan error, 1)
	host.advance.Lock()
	go func() {
		available, _, decision, err := host.disposalDecision(ctx, disposalTestInput("disposal-pending"))
		if err == nil && (!available || decision["status"] != "keep") {
			err = errors.New("pure disposal result unavailable")
		}
		done <- err
	}()
	select {
	case err := <-done:
		host.advance.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		host.advance.Unlock()
		t.Fatal("pure disposal read waited for persistent advancement")
	}
	assertFollowFallbackState(t, host, before)
	changed := strings.Replace(source, `reason="current"`, `reason="reloaded"`, 1)
	if _, err := host.Reload(ctx, changed, host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if available, _, out, err := host.disposalDecision(ctx, disposalTestInput("disposal-pending")); err != nil || !available || out["reason"] != "reloaded" {
		t.Fatal("reload retained older pure export", available, out, err)
	}
	pending := host.vm.Outstanding()
	if len(pending) != 1 || pending[0].ActionID != receipt.ActionID || pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("pure read or reload settled or replayed uncertainty", pending)
	}
}

func TestPolicyDisposalMissingOrFaultedCurrentExportNeverFallsBack(t *testing.T) {
	for _, export := range []string{"", `disposal_evaluation=true,`} {
		_, host := disposalTestHost(t, "disposal-missing", disposalTestSource(export))
		available, revision, out, err := host.disposalDecision(context.Background(), disposalTestInput("disposal-missing"))
		if err != nil || available || revision != host.vm.Status().Revision || out != nil {
			t.Fatal("absent optional function advertised a decision", available, revision, out, err)
		}
	}
	ctx := context.Background()
	m, host := disposalTestHost(t, "disposal-fault", disposalTestSource(`disposal_evaluation=function(input)return {status="eligible"}end,`))
	broken := disposalTestSource(`disposal_evaluation=function(input)error("current disposal failed")end,`)
	current, err := host.Reload(ctx, broken, host.vm.Status().Revision)
	if err != nil {
		t.Fatal(err)
	}
	before, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		available, revision, out, err := host.disposalDecision(ctx, disposalTestInput("disposal-fault"))
		if !available || revision != current || out != nil || err == nil || !strings.Contains(err.Error(), "current disposal failed") {
			t.Fatal("failed current export restored older eligibility", available, revision, out, err)
		}
	}
	request := disposalTestRequest(t, map[string]any{"sessionId": "disposal-fault", "selfGuid": "4", "input": disposalTestInput("disposal-fault")})
	if _, err := m.invokeModule(ctx, "buddy-trade", request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("faulted pure export did not fail closed at callback", err)
	}
	assertFollowFallbackState(t, host, before)
}

func TestPolicyDisposalFencesCommittedButUnpublishedSource(t *testing.T) {
	ctx := context.Background()
	source := disposalTestSource(`disposal_evaluation=function(input)return {status="eligible"}end,`)
	_, host := disposalTestHost(t, "disposal-publication", source)
	changed := strings.Replace(source, `status="eligible"`, `status="keep"`, 1)
	revision, err := host.vm.Reload(ctx, changed, host.vm.Status().Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, decision, err := host.disposalDecision(ctx, disposalTestInput("disposal-publication")); err == nil || decision != nil {
		t.Fatal("old export escaped after persistent VM commit", decision, err)
	}
	host.source = changed
	host.publishDisposalEvaluation(changed, revision)
	if available, current, out, err := host.disposalDecision(ctx, disposalTestInput("disposal-publication")); err != nil || !available || current != revision || out["status"] != "keep" {
		t.Fatal("newly published export unavailable", available, current, out, err)
	}
}

func TestPolicyDisposalMalformedExportOutputDoesNotQuarantinePersistentVM(t *testing.T) {
	ctx := context.Background()
	m, host := disposalTestHost(t, "disposal-bad-output", disposalTestSource(`disposal_evaluation=function(input)return true end,`))
	before, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := disposalTestRequest(t, map[string]any{"sessionId": "disposal-bad-output", "selfGuid": "4", "input": disposalTestInput("disposal-bad-output")})
	for range 2 {
		if _, err := m.invokeModule(ctx, "buddy-trade", request); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("nonobject pure result was accepted", err)
		}
	}
	assertFollowFallbackState(t, host, before)
	if host.vm.Status().Fault != "" {
		t.Fatal("optional pure export quarantined persistent gameplay", host.vm.Status())
	}
}

func TestPolicyDisposalCallbackRequiresExactTradeCallerBodyAndSession(t *testing.T) {
	ctx := context.Background()
	m, host := disposalTestHost(t, "disposal-callback", disposalTestSource(`disposal_evaluation=function(input)return {status="keep",reason=input.selfGuid}end,`))
	valid := disposalTestRequest(t, map[string]any{"sessionId": "disposal-callback", "selfGuid": "4", "input": disposalTestInput("disposal-callback")})
	response, err := m.invokeModule(ctx, "buddy-trade", valid)
	if err != nil {
		t.Fatal(err)
	}
	value := &structpb.Struct{}
	if response.GetResult().UnmarshalTo(value) != nil {
		t.Fatal("disposal callback returned an untyped result")
	}
	fields := value.AsMap()
	decision, ok := fields["decision"].(map[string]any)
	if len(fields) != 3 || fields["available"] != true || fields["revision"] != host.vm.Status().Revision || !ok || decision["status"] != "keep" {
		t.Fatal("exact accepted export result lost", fields)
	}
	for _, caller := range []string{"fixture", "buddy-actions", "buddy-inventory", "jsonrpc"} {
		if _, err := m.invokeModule(ctx, caller, valid); status.Code(err) != codes.PermissionDenied {
			t.Fatal("foreign caller used the Trade-only callback", caller, err)
		}
	}
	for name, test := range map[string]struct {
		sid, self, inputSID, inputSelf string
		code                           codes.Code
	}{
		"old_session":    {"previous", "4", "previous", "4", codes.FailedPrecondition},
		"other_body":     {"disposal-callback", "5", "disposal-callback", "5", codes.FailedPrecondition},
		"nested_body":    {"disposal-callback", "4", "disposal-callback", "5", codes.InvalidArgument},
		"nested_session": {"disposal-callback", "4", "previous", "4", codes.InvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			input := disposalTestInput(test.inputSID)
			input["selfGuid"] = test.inputSelf
			request := disposalTestRequest(t, map[string]any{"sessionId": test.sid, "selfGuid": test.self, "input": input})
			if _, err := m.invokeModule(ctx, "buddy-trade", request); status.Code(err) != test.code {
				t.Fatal("stale or inconsistent body accepted", err)
			}
		})
	}
	for _, field := range []string{"sessionId", "selfGuid"} {
		input := disposalTestInput("disposal-callback")
		delete(input, field)
		request := disposalTestRequest(t, map[string]any{"sessionId": "disposal-callback", "selfGuid": "4", "input": input})
		if _, err := m.invokeModule(ctx, "buddy-trade", request); status.Code(err) != codes.InvalidArgument {
			t.Fatal("missing nested identity accepted", field, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.invokeModule(canceled, "buddy-trade", valid); status.Code(err) != codes.Canceled {
		t.Fatal("canceled callback evaluated policy", err)
	}
	for _, lifecycle := range []string{"closing", "preparing", "prepared", "replaced_body", "replaced_session", "host_body"} {
		t.Run(lifecycle, func(t *testing.T) {
			dir, body, identity := m.dir, host.body, m.session.Identity.CharacterGUID
			switch lifecycle {
			case "closing":
				m.closing = true
			case "preparing":
				m.preparing = true
			case "prepared":
				m.prepared = true
			case "replaced_body":
				m.session.Identity.CharacterGUID = 5
			case "replaced_session":
				m.dir = "/private/replacement"
			case "host_body":
				host.body = "5"
			}
			_, err := m.invokeModule(ctx, "buddy-trade", valid)
			m.closing, m.preparing, m.prepared = false, false, false
			m.dir, host.body, m.session.Identity.CharacterGUID = dir, body, identity
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatal("callback escaped current lifecycle", err)
			}
		})
	}
}

func TestPolicyDisposalCallbackRejectsUntypedAndOversizedInputAndKeepsNoSourceExplicit(t *testing.T) {
	ctx := context.Background()
	m := catalogManager(t, 4, "disposal-no-source")
	t.Cleanup(m.cancel)
	input := disposalTestInput("disposal-no-source")
	valid := disposalTestRequest(t, map[string]any{"sessionId": "disposal-no-source", "selfGuid": "4", "input": input})
	response, err := m.invokeModule(ctx, "buddy-trade", valid)
	value := &structpb.Struct{}
	if err != nil || response.GetResult().UnmarshalTo(value) != nil || value.AsMap()["available"] != false || value.AsMap()["revision"] != "" {
		t.Fatal("no attached source was not explicit", response, err)
	}
	wrongType, err := anypb.New(&emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*modv1.InvokeModuleRequest{
		{Module: modcb.DisposalPolicyModule, Method: modcb.DisposalPolicyMethod},
		{Module: modcb.DisposalPolicyModule, Method: modcb.DisposalPolicyMethod, Request: wrongType},
		disposalTestRequest(t, map[string]any{"sessionId": "disposal-no-source", "selfGuid": "4", "input": []any{}}),
		disposalTestRequest(t, map[string]any{"sessionId": "disposal-no-source", "selfGuid": "4", "input": input, "eligible": true}),
		disposalTestRequest(t, map[string]any{"sessionId": "disposal-no-source", "selfGuid": "4", "input": map[string]any{"selfGuid": "4", "sessionId": "disposal-no-source", "payload": strings.Repeat("x", modcb.DisposalPolicyMaxBytes)}}),
	} {
		if _, err := m.invokeModule(ctx, "buddy-trade", request); status.Code(err) != codes.InvalidArgument {
			t.Fatal("unbounded or ambiguous callback input accepted", err)
		}
	}
}
