package modrt

import (
	"context"
	"strconv"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
)

func TestPolicyQuestContainerMovementCarriesExactObjectiveAndUnknownReceipt(t *testing.T) {
	x := newInstanceEntryFixture(t)
	guid := uint64(0xf110)<<48 | uint64(124389)<<24 | 17
	x.handle = func(_ context.Context, method string, request, result proto.Message) error {
		q := &actionsv1.ApproachTargetRequest{}
		data, e := proto.Marshal(request)
		if e != nil {
			t.Fatal(e)
		}
		if err := proto.Unmarshal(data, q); err != nil {
			t.Fatal(err)
		}
		if q.TargetGuid != guid || q.SelfGuid != 22 || q.QuestId != 1360 || q.QuestItemId != 8027 || q.QuestObjectRevision != 7 ||
			q.QuestLogRevision != 9 || q.QuestInventoryRevision != 11 || q.QuestFollowRevision != 2 || q.QuestOwnerGuid != 3 {
			t.Fatal(method, q)
		}
		proto.Merge(result, &actionsv1.State{MotionUnconfirmed: true})
		return nil
	}
	a := map[string]any{"capability": "approach_target", "sessionId": friendlyEncounterSession, "selfGuid": "22", "targetGuid": strconv.FormatUint(guid, 10),
		"visitId": "1", "rangeYards": float64(3), "requireLos": true, "questId": float64(1360), "questItemId": float64(8027), "questObjectRevision": "7", "questLogRevision": "9",
		"questInventoryRevision": "11", "questFollowRevision": "2", "questOwnerGuid": "3"}
	receipt := x.step(t, a)
	if receipt == nil || receipt.Payload["reconciliationRequired"] != true || receipt.Payload["target_guid"] != a["targetGuid"] || len(x.calls) != 1 {
		t.Fatal(receipt, x.calls)
	}
	delete(a, "questObjectRevision")
	if _, err := x.p.resolveMovement(a, "approach_target"); err == nil {
		t.Fatal("accepted an incomplete source binding")
	}
}
