package modrt

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

const movementPointsHook = "movement_points"

func (p *SessionPolicy) movementPointsAvailable() bool {
	if !p.vm.HasExport(movementPointsHook) {
		return false
	}
	_, method, err := p.manager.lookup(actionsModule, "set_movement_points", true)
	return err == nil && method.Path() == "/warcraftbuddy.actions.v1.BuddyActions/SetMovementPoints"
}

// This is an observation-only pass through the SAME persistent VM. Ingress
// and receipts retain their original order; no action is resolved or reserved.
func (p *SessionPolicy) stepMovementPointsObservation(ctx context.Context, sid string) error {
	if !p.vm.HasExport(movementPointsHook) {
		return nil
	}
	before := p.vm.Status()
	observed, err := p.manager.readPolicyObservation(ctx, sid, encounterObservationMethod)
	if err != nil {
		return err
	}
	if observed["selfGuid"] != p.body {
		return errors.New("movement points observation body changed")
	}
	p.advance.Lock()
	defer p.advance.Unlock()
	current := p.vm.Status()
	if p.lifetime != nil && p.lifetime.Err() != nil || current.Revision != before.Revision || current.Epoch != before.Epoch || !p.vm.HasExport(movementPointsHook) {
		return errors.New("movement points source changed")
	}
	// Only an actual in-flight dispatch needs the advisory pass. Normal Step
	// publishes the same data with its ordinary decision.
	if p.dispatchDone == nil {
		return nil
	}
	sequence, err := p.Enqueue(behavior.Event{Kind: "encounter_observation", Value: observed})
	if err != nil {
		return err
	}
	decision, err := p.vm.Decide(ctx, map[string]any{"kind": "movement_points", "observationSequence": strconv.FormatUint(sequence, 10)})
	if err != nil {
		return err
	}
	for key := range decision.Output {
		if key != "movementPoints" && key != "movementPointStatus" && key != "encounterOwnership" {
			return errors.New("movement points advisory output contains gameplay or unrelated fields")
		}
	}
	visitText, _ := observed["visitId"].(string)
	visit, parseErr := strconv.ParseUint(visitText, 10, 64)
	if parseErr != nil || visit == 0 || strconv.FormatUint(visit, 10) != visitText {
		return errors.New("current advisory visit required")
	}
	currentContext := context.WithValue(ctx, policyEncounterVisitKey{}, policyEncounterObservation{visit: visit, sequence: strconv.FormatUint(sequence, 10)})
	if err := p.renewEncounterWriter(currentContext, decision.Output, nil, PolicyDispatch{}); err != nil {
		return err
	}
	return p.refreshMovementPoints(currentContext, decision.Output)
}

// Source-owned geometry is installed only after this observation renews its
// existing writer. A chosen escape requires a successful current update; other
// urgent actions do not depend on this optional data channel.
func (p *SessionPolicy) deliverDecisionMovementPoints(ctx context.Context, output, action map[string]any) error {
	escape := action != nil && action["capability"] == "movement_points_step"
	_, data := output["movementPoints"].(map[string]any)
	if escape && (!p.vm.HasExport(movementPointsHook) || !data) {
		return errors.New("movement points step requires current source-owned point input")
	}
	if !p.vm.HasExport(movementPointsHook) {
		return nil
	}
	err := p.refreshMovementPoints(ctx, output)
	if err != nil {
		p.manager.logger.Warn("Lua movement points delivery failed", "error", err)
		if escape {
			return err
		}
	}
	return nil
}

// The native setter accepts declarative geometry only. It has no packet or
// gameplay-action authority; transport failure is not an action retry.
func (p *SessionPolicy) refreshMovementPoints(ctx context.Context, output map[string]any) error {
	value, ok := output["movementPoints"].(map[string]any)
	if !ok {
		return nil
	}
	sid, _ := value["sessionId"].(string)
	body, _ := value["selfGuid"].(string)
	visitText, _ := value["visitId"].(string)
	mapID, mapKnown := value["mapId"].(float64)
	if !mapKnown || math.IsNaN(mapID) || math.IsInf(mapID, 0) || mapID < 0 || mapID > math.MaxUint32 || math.Trunc(mapID) != mapID {
		return errors.New("explicit current movement points map required")
	}
	if _, present := value["points"]; !present {
		return errors.New("explicit replacement points required")
	}
	visit, err := strconv.ParseUint(visitText, 10, 64)
	if sid != p.vm.Status().SessionID || body != p.body || err != nil || visit == 0 || strconv.FormatUint(visit, 10) != visitText {
		return errors.New("movement points must bind the current body and visit")
	}
	inst, method, err := p.manager.lookup(actionsModule, "set_movement_points", true)
	if err != nil {
		return err
	}
	if method.Path() != "/warcraftbuddy.actions.v1.BuddyActions/SetMovementPoints" || string(method.Descriptor().Input().FullName()) != "warcraftbuddy.actions.v1.MovementPointsRequest" {
		return errors.New("incompatible movement points setter")
	}
	// Normalize only Lua's empty table and the existing cloud_points expiry
	// spelling. All other values retain the source's exact authored data.
	request := make(map[string]any, len(value))
	for key, v := range value {
		request[key] = v
	}
	if empty, ok := value["points"].(map[string]any); ok && len(empty) == 0 {
		request["points"] = []any{}
	}
	if _, ok := request["points"].([]any); !ok {
		return errors.New("movement points replacement must be an explicit list")
	}
	if rows, ok := request["points"].([]any); ok {
		detached := make([]any, len(rows))
		for i, row := range rows {
			original, ok := row.(map[string]any)
			if !ok {
				return errors.New("movement point object required")
			}
			point := make(map[string]any, len(original))
			for key, v := range original {
				point[key] = v
			}
			if expires, ok := point["expiresAtMs"]; ok {
				if _, conflict := point["expiresAtUnixMs"]; conflict {
					return errors.New("ambiguous movement point expiry")
				}
				delete(point, "expiresAtMs")
				point["expiresAtUnixMs"] = expires
			}
			detached[i] = point
		}
		request["points"] = detached
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req := dynamicpb.NewMessage(method.Descriptor().Input())
	if err = (protojson.UnmarshalOptions{Resolver: inst.definition.Types()}).Unmarshal(raw, req); err != nil {
		return err
	}
	result := dynamicpb.NewMessage(method.Descriptor().Output())
	call, cancel := context.WithTimeout(ctx, policyObservationInterval)
	defer cancel()
	call = modcb.WithPolicyEncounterAccess(call, inst.launch.Environment[modcb.PolicyEncounterKeyEnv], visit)
	// advance fences reload until this bounded setter returns. Remember even an
	// uncertain data update so a subsequent source reload attempts an empty clear.
	p.movementPointsScope = map[string]any{"sessionId": sid, "selfGuid": body, "visitId": visitText, "mapId": value["mapId"], "points": []any{}}
	return inst.conn.Invoke(call, method.Path(), req, result)
}

func (p *SessionPolicy) clearMovementPoints(ctx context.Context) error {
	if p.movementPointsScope == nil {
		return nil
	}
	scope := p.movementPointsScope
	p.movementPointsScope = nil
	bounded, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	err := p.refreshMovementPoints(bounded, map[string]any{"movementPoints": scope})
	if err == nil {
		p.movementPointsScope = nil
	}
	return err
}

// A Lua-selected safe-yield step uses the existing single-use movement packet
// ledger and body-input permission; the geometry setter itself cannot invoke it.
func (p *SessionPolicy) resolveMovementPointsStep(action map[string]any) (PolicyDispatch, error) {
	allowed := map[string]bool{"capability": true, "sessionId": true, "selfGuid": true, "mapId": true, "visitId": true, "sequenceId": true, "movementOwner": true, "movementToken": true, "targetConstraints": true}
	for key := range action {
		if !allowed[key] {
			return PolicyDispatch{}, errors.New("unsupported movement points step field")
		}
	}

	sid, _ := action["sessionId"].(string)
	body, _ := action["selfGuid"].(string)
	visitText, _ := action["visitId"].(string)
	sequenceText, _ := action["sequenceId"].(string)
	self, a := strconv.ParseUint(body, 10, 64)
	visit, b := strconv.ParseUint(visitText, 10, 64)
	sequence, c := strconv.ParseUint(sequenceText, 10, 64)
	mapID, ok := action["mapId"].(float64)
	if sid != p.vm.Status().SessionID || body != p.body || a != nil || self == 0 || b != nil || visit == 0 || c != nil || sequence == 0 || strconv.FormatUint(visit, 10) != visitText || strconv.FormatUint(sequence, 10) != sequenceText || !ok || mapID < 0 || mapID > math.MaxUint32 || math.Trunc(mapID) != mapID {
		return PolicyDispatch{}, errors.New("current body, map, visit and sequence required for movement points step")
	}
	_, method, lookupErr := p.manager.lookup(actionsModule, "step_movement_points", true)
	if lookupErr != nil {
		return PolicyDispatch{}, lookupErr
	}
	if method.Path() != "/warcraftbuddy.actions.v1.BuddyActions/StepMovementPoints" || string(method.Descriptor().Input().FullName()) != "warcraftbuddy.actions.v1.MovementPointsStepRequest" {
		return PolicyDispatch{}, errors.New("incompatible movement points step")
	}

	values := map[protoreflect.Name]protoreflect.Value{"session_id": protoreflect.ValueOfString(sid), "self_guid": protoreflect.ValueOfUint64(self), "map_id": protoreflect.ValueOfUint32(uint32(mapID)), "visit_id": protoreflect.ValueOfUint64(visit), "sequence_id": protoreflect.ValueOfUint64(sequence)}
	for field, key := range map[protoreflect.Name]string{"movement_owner": "movementOwner", "movement_token": "movementToken"} {
		if raw, present := action[key]; present {
			value, ok := raw.(string)
			if !ok {
				return PolicyDispatch{}, errors.New("movement owner and token must be strings")
			}
			values[field] = protoreflect.ValueOfString(value)
		}
	}
	if raw, present := action["targetConstraints"]; present {
		object, ok := raw.(map[string]any)
		if !ok {
			return PolicyDispatch{}, errors.New("explicit hard target constraints object required")
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			return PolicyDispatch{}, err
		}
		band := &actionsv1.ApproachTargetRequest{}
		if err = protojson.Unmarshal(encoded, band); err != nil {
			return PolicyDispatch{}, err
		}
		if band.SessionId != sid || band.SelfGuid != self || band.VisitId != visit || band.TargetGuid == 0 || band.Stop || band.Behind || band.RequireLos {
			return PolicyDispatch{}, errors.New("hard geometry must match current body/visit without stop, behind or LOS")
		}
		values["target_constraints"] = protoreflect.ValueOfMessage(band.ProtoReflect())
	}

	op := &policySpell{vm: p.vm, capability: "movement_points_step", encounterVisit: visit, movement: &policyMovement{self: self, body: true, sequence: sequence}}
	dispatch, err := p.preparePolicyDispatch("step_movement_points", values, op)
	if err == nil {
		dispatch.spell = nil
	}
	return dispatch, err
}

func (op *policySpell) movementPointsNoDispatch(method moddisc.Method, state *actionsv1.State) (string, bool) {
	if op.capability != "movement_points_step" || op.movement == nil || op.attempted || op.written || op.movement.unknown || state.MotionUnconfirmed || state.SessionId != op.origin.SessionID || state.SelfGuid != op.movement.self {
		return "", false
	}
	if prior := state.BodyMovement; prior != nil && prior.VisitId == op.encounterVisit && prior.SequenceId == op.movement.sequence {
		return "", false
	}
	descriptor := method.Descriptor().Output()
	field := descriptor.Fields().ByName("movement_points")
	if field == nil || field.Number() != 122 || field.Kind() != protoreflect.MessageKind || string(field.Message().FullName()) != "warcraftbuddy.actions.v1.MovementPointsState" {
		return "", false
	}
	raw, err := proto.Marshal(state)
	if err != nil {
		return "", false
	}
	decoded := dynamicpb.NewMessage(descriptor)
	if proto.Unmarshal(raw, decoded) != nil || !decoded.Has(field) {
		return "", false
	}
	points := decoded.Get(field).Message()
	reasonField := points.Descriptor().Fields().ByName("reason")
	if reasonField == nil || reasonField.Kind() != protoreflect.StringKind {
		return "", false
	}
	reason := points.Get(reasonField).String()
	switch reason {
	case "already_clear", "no_current_unexpired_points", "protected_cast_pending", "protected_operation_pending", "no_navigable_escape", "hard_constraints_or_navigation_blocked":
		return reason, true
	}
	return "", false
}
