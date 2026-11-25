package modrt

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestPolicyMovementCarriesExactRangeBandAndClearanceGUIDs(t *testing.T) {
	x := newInstanceEntryFixture(t)
	x.handle = func(ctx context.Context, method string, request, result proto.Message) error {
		q := &actionsv1.ApproachTargetRequest{}
		raw, err := proto.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if err = proto.Unmarshal(raw, q); err != nil {
			t.Fatal(err)
		}
		if method != actionsv1.BuddyActions_ApproachTarget_FullMethodName || q.SessionId != friendlyEncounterSession || q.SelfGuid != 22 || q.TargetGuid != 123 || q.VisitId != 1 || q.RangeYards != 23 || q.MinRangeYards != 17 || q.ClearanceYards != 17 || len(q.ClearanceTargetGuids) != 2 || q.ClearanceTargetGuids[0] != 123 || q.ClearanceTargetGuids[1] != 124 {
			t.Fatal("typed production movement lost band", method, q)
		}
		proto.Merge(result, &actionsv1.State{TargetMovement: &actionsv1.TargetMovementState{TargetGuid: q.TargetGuid, VisitId: 1, RangeYards: 23, MinRangeYards: 17}})
		return nil
	}
	a := map[string]any{"capability": "approach_target", "sessionId": friendlyEncounterSession, "selfGuid": "22", "targetGuid": "123", "visitId": "1", "rangeYards": float64(23), "minRangeYards": float64(17), "clearanceYards": float64(17), "clearanceTargetGuids": []any{"123", "124"}}
	receipt := x.step(t, a)
	if receipt == nil || len(x.calls) != 1 || len(x.callback.writes) != 0 {
		t.Fatal(receipt, x.calls, x.callback.writes)
	}
	for _, bad := range []any{[]any{"123", "123"}, []any{"123", "22"}, []any{"0123"}, []any{float64(123)}, []any{}} {
		a["clearanceTargetGuids"] = bad
		if _, err := x.p.resolveMovement(a, "approach_target"); err == nil {
			t.Fatal("malformed clearance accepted", bad)
		}
	}
}
