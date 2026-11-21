package modrt

import (
	"context"
	"encoding/json"
	"strconv"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// The policy supplies an ordered geometric proposal. Native admission retains
// the current witness, corridor checks and movement custody.
func (p *SessionPolicy) refreshPortalCatchup(ctx context.Context, decision map[string]any, current policyEncounterObservation) error {
	goal, ok := decision["portalCatchup"].(map[string]any)
	if !ok {
		return nil
	}
	if goal["sessionId"] != p.vm.Status().SessionID || goal["selfGuid"] != p.body ||
		goal["visitId"] != strconv.FormatUint(current.visit, 10) || goal["observationSequence"] != current.sequence {
		return status.Error(codes.InvalidArgument, "portal goal must bind the current body, visit and observation")
	}
	start, ok := goal["start"].(map[string]any)
	if !ok {
		return status.Error(codes.InvalidArgument, "portal goal requires the observed start position")
	}
	for _, value := range []any{goal["selfGuid"], goal["visitId"], goal["ownerGuid"], goal["witnessRevision"], start["revision"]} {
		text, ok := value.(string)
		id, err := strconv.ParseUint(text, 10, 64)
		if !ok || err != nil || id == 0 || strconv.FormatUint(id, 10) != text {
			return status.Error(codes.InvalidArgument, "canonical portal identities and observed revisions required")
		}
	}
	// Follow is a counter. Zero names the body's untouched native intent,
	// while the body, visit, witness and observed start remain nonzero.
	counter, ok := goal["followRevision"].(string)
	revision, err := strconv.ParseUint(counter, 10, 64)
	if !ok || err != nil || strconv.FormatUint(revision, 10) != counter {
		return status.Error(codes.InvalidArgument, "canonical portal follow counter required")
	}
	// observationSequence belongs to the host admission check. Every other
	// field is decoded by the production protobuf schema without float IDs.
	body := make(map[string]any, len(goal)-1)
	for name, value := range goal {
		if name != "observationSequence" {
			body[name] = value
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid portal goal: %v", err)
	}
	request := &actionsv1.PortalCatchupRequest{}
	if err := protojson.Unmarshal(raw, request); err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid portal goal: %v", err)
	}
	inst, _, err := p.manager.lookup(actionsModule, "set_portal_catchup", true)
	if err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, policyObservationInterval)
	defer cancel()
	call = modcb.WithPolicyEncounterAccess(call, inst.launch.Environment[modcb.PolicyEncounterKeyEnv], current.visit)
	_, err = actionsv1.NewBuddyActionsClient(inst.conn).SetPortalCatchup(call, request)
	return err
}
