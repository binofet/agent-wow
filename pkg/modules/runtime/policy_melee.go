package modrt

import (
	"strconv"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type policyMelee struct {
	self  uint64
	visit uint64
	stop  bool
}

func (p *SessionPolicy) resolveMelee(action map[string]any, capability string) (PolicyDispatch, error) {
	sessionID, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	target, _ := action["targetGuid"].(string)
	visit, _ := action["visitId"].(string)
	selfGUID, _ := strconv.ParseUint(self, 10, 64)
	targetGUID, targetErr := strconv.ParseUint(target, 10, 64)
	visitID, visitErr := strconv.ParseUint(visit, 10, 64)
	if sessionID != p.vm.Status().SessionID || self != p.body || selfGUID == 0 ||
		targetErr != nil || targetGUID == 0 || target != strconv.FormatUint(targetGUID, 10) ||
		visitErr != nil || visitID == 0 || visit != strconv.FormatUint(visitID, 10) {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current session, current body, exact target GUID and exact visit required")
	}
	melee := &policyMelee{self: selfGUID, visit: visitID, stop: capability == "melee_stop"}
	values := map[protoreflect.Name]protoreflect.Value{
		"session_id":  protoreflect.ValueOfString(sessionID),
		"self_guid":   protoreflect.ValueOfUint64(selfGUID),
		"target_guid": protoreflect.ValueOfUint64(targetGUID),
		"visit_id":    protoreflect.ValueOfUint64(visitID),
		"stop":        protoreflect.ValueOfBool(melee.stop),
	}
	op := &policySpell{vm: p.vm, capability: capability, target: targetGUID, melee: melee}
	return p.preparePolicyDispatch("melee", values, op)
}

func (op *policySpell) meleePayload(phase string, written, observed bool) map[string]any {
	return map[string]any{
		"capability": op.capability, "phase": phase,
		"self_guid": strconv.FormatUint(op.melee.self, 10), "target_guid": strconv.FormatUint(op.target, 10),
		"visit_id": strconv.FormatUint(op.melee.visit, 10), "enabled": !op.melee.stop,
		"transport_written": written, "effect_observed": observed,
	}
}

func (op *policySpell) decodeMeleeEffect(body []byte) (behavior.ActionOutcome, map[string]any, error) {
	var native actionsv1.MeleeOperation
	if err := proto.Unmarshal(body, &native); err != nil {
		return "", nil, err
	}
	melee := op.melee
	if native.GetSelfGuid() != melee.self || native.GetTargetGuid() != op.target ||
		native.GetVisitId() != melee.visit || native.GetEnabled() == melee.stop {
		return "", nil, nil
	}
	outcome := behavior.ActionOutcome("")
	switch {
	case !melee.stop && native.GetPhase() == "confirmed_attack_start",
		melee.stop && native.GetPhase() == "confirmed_attack_stop":
		outcome = behavior.ActionConfirmed
	case native.GetPhase() == "outcome_unconfirmed_no_retry":
		outcome = behavior.ActionSettledUnverified
	default:
		return "", nil, nil
	}
	return outcome, op.meleePayload(native.GetPhase(), native.GetTransportWritten(), outcome == behavior.ActionConfirmed), nil
}
