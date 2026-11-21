package modrt

import (
	"context"
	"encoding/json"
	"strconv"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Refreshing a goal does not reserve a second game action or write packets.
// Actions executes it below its existing support and finite movement owners.
func (p *SessionPolicy) refreshFollowFormation(ctx context.Context, decision map[string]any, current policyEncounterObservation) error {
	goal, ok := decision["followFormation"].(map[string]any)
	if !ok {
		return nil
	}
	if goal["sessionId"] != p.vm.Status().SessionID || goal["selfGuid"] != p.body ||
		goal["visitId"] != strconv.FormatUint(current.visit, 10) || goal["observationSequence"] != current.sequence {
		return status.Error(codes.InvalidArgument, "formation goal must bind the current body, visit and observation")
	}
	// JSON is used only to decode the small policy object. GUID/visit values
	// retain their canonical decimal strings through protobuf JSON.
	body, err := json.Marshal(goal)
	if err != nil {
		return err
	}
	var value struct {
		SessionID             string `json:"sessionId"`
		SelfGUID              string `json:"selfGuid"`
		VisitID               string `json:"visitId"`
		OwnerGUID             string `json:"ownerGuid"`
		FollowRevision        string `json:"followRevision"`
		OwnerPositionRevision string `json:"ownerPositionRevision"`
		X, Y, Z               float32
		Orientation           float32
		StartDistance         float64 `json:"startDistance"`
		StopDistance          float64 `json:"stopDistance"`
		Phase                 string
		Ready, Hold           bool
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	parse := func(v string) (uint64, bool) {
		id, err := strconv.ParseUint(v, 10, 64)
		return id, err == nil && id != 0 && strconv.FormatUint(id, 10) == v
	}
	self, a := parse(value.SelfGUID)
	visit, b := parse(value.VisitID)
	owner, c := parse(value.OwnerGUID)
	revision, d := parse(value.OwnerPositionRevision)
	followRevision, e := parse(value.FollowRevision)
	// Follow is a counter, unlike the nonzero identities above. A freshly
	// attached body's untouched native intent has canonical revision zero.
	e = e || value.FollowRevision == "0"
	if !a || !b || !c || !d || !e {
		return status.Error(codes.InvalidArgument, "canonical formation identities and owner revision required")
	}
	inst, _, err := p.manager.lookup(actionsModule, "set_follow_formation", true)
	if err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, policyObservationInterval)
	defer cancel()
	call = modcb.WithPolicyEncounterAccess(call, inst.launch.Environment[modcb.PolicyEncounterKeyEnv], visit)
	_, err = actionsv1.NewBuddyActionsClient(inst.conn).SetFollowFormation(call, &actionsv1.FollowFormationRequest{
		SessionId: value.SessionID, SelfGuid: self, VisitId: visit, OwnerGuid: owner, OwnerPositionRevision: revision,
		X: value.X, Y: value.Y, Z: value.Z, Orientation: value.Orientation,
		Phase: value.Phase, FollowRevision: followRevision, StartDistance: value.StartDistance, StopDistance: value.StopDistance, Ready: value.Ready, Hold: value.Hold,
	})
	return err
}
