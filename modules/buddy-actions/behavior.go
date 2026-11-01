package buddyactions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	contract "github.com/agent-wow/agent-wow/pkg/behaviorcontract"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func combatant(p *worldv1.PlayerState) contract.Combatant {
	if p == nil {
		return contract.Combatant{}
	}
	return contract.Combatant{Guid: strconv.FormatUint(p.Guid, 10), Health: p.Health, MaxHealth: p.MaxHealth, ClassId: p.ClassId, Power: p.Power, MaxPower: p.MaxPower}
}

func (s *Service) Behavior(_ context.Context, _ *emptypb.Empty) (*actionsv1.BehaviorStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.behaviorStatusLocked(), nil
}
func (s *Service) behaviorStatusLocked() *actionsv1.BehaviorStatus {
	out := &actionsv1.BehaviorStatus{SessionId: s.sessionID, SelfGuid: s.selfGUID, LastRejectedDecision: s.behaviorRejection}
	if s.behavior != nil {
		v := s.behavior.Status()
		out.Revision = v.Revision
		out.LastGoodRevision = v.LastGoodRevision
		out.QuarantinedRevision = v.QuarantinedRevision
		out.LastError = v.LastError
		out.Evaluations = v.Evaluations
		out.Failures = v.Failures
	}
	return out
}

func (s *Service) ReloadBehavior(_ context.Context, r *actionsv1.BehaviorReloadRequest) (*actionsv1.BehaviorStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || s.closed {
		return nil, status.Error(codes.FailedPrecondition, "Current owned body required")
	}
	current := s.behaviorStatusLocked().Revision
	if current != r.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "Behavior revision changed")
	}
	if r.UseBuiltin {
		if r.Source != "" {
			return nil, status.Error(codes.InvalidArgument, "Builtin rollback takes no source")
		}
		s.behavior = nil
		s.behaviorRejection = ""
		return s.behaviorStatusLocked(), nil
	}
	if s.behavior == nil {
		s.behavior = behavior.New()
	}
	if _, err := s.behavior.Reload(r.Source, r.ExpectedRevision); err != nil {
		if errors.Is(err, behavior.ErrRevisionConflict) {
			return nil, status.Error(codes.Aborted, "Behavior revision changed")
		}
		return nil, status.Error(codes.InvalidArgument, "Policy rejected; inspect behavior status")
	}
	s.behaviorRejection = ""
	return s.behaviorStatusLocked(), nil
}

func (s *Service) behaviorDecisionLocked(input contract.Input) contract.Decision {
	if s.behavior == nil || s.behavior.Status().Revision == "" {
		return contract.Decision{}
	}
	// GUIDs stay decimal strings, including values above Lua's exact integer range.
	if input.Spells == nil {
		input.Spells = []contract.Spell{}
	}
	if input.Targets == nil {
		input.Targets = []contract.Combatant{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return contract.Decision{}
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return contract.Decision{}
	}
	output, err := s.behavior.Evaluate(value)
	if err != nil {
		s.behaviorRejection = "policy_execution_failed_using_native_decision"
		return contract.Decision{}
	}
	raw, err = json.Marshal(output)
	if err != nil {
		return contract.Decision{}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var decision contract.Decision
	if decoder.Decode(&decision) != nil {
		s.behaviorRejection = "invalid_policy_decision_using_native_decision"
		return contract.Decision{}
	}
	return decision
}

func (s *Service) behaviorHealTargetLocked(self *worldv1.PlayerState, players []*worldv1.PlayerState, best *worldv1.PlayerState) *worldv1.PlayerState {
	if best == nil || s.behavior == nil {
		return best
	}
	input := contract.Input{Kind: "heal_target", Self: combatant(self), DefaultTargetGuid: strconv.FormatUint(best.Guid, 10)}
	for _, p := range players {
		input.Targets = append(input.Targets, combatant(p))
	}
	decision := s.behaviorDecisionLocked(input)
	if decision.TargetGuid == "" {
		return best
	}
	for _, p := range players {
		if decision.TargetGuid == strconv.FormatUint(p.Guid, 10) {
			return p
		}
	}
	s.behaviorRejection = "unobserved_or_ineligible_heal_target_using_native_decision"
	return best
}
