package buddyactions

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// The host samples every 250 ms. Allow a momentary stall and resume native
// selection if an entire second passes without a fresh decision.
const encounterLeaseDuration = time.Second

// UseHostEncounterWriter enables lease requests from this session's host. Call
// it before starting the service or exposing its RPCs.
func (s *Service) UseHostEncounterWriter(key string) error {
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		return errors.New("host encounter writer requires a session-issued key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostEncounterKey = key
	return nil
}

func (s *Service) hostEncounterActiveLocked() bool {
	return s.hostEncounterKey != "" && !s.closed && s.encounterVisit.bound && s.hostEncounterVisit != 0 && s.hostEncounterVisit == s.encounterVisit.revision &&
		s.now().Before(s.hostEncounterUntil)
}

func (s *Service) trustedEncounterHostLocked(ctx context.Context) bool {
	return s.hostEncounterKey != "" && subtle.ConstantTimeCompare([]byte(s.hostEncounterKey), []byte(modcb.IncomingPolicyEncounterKey(ctx))) == 1
}

func (s *Service) matchesCurrentEncounterVisitLocked(w *worldv1.SnapshotResponse) bool {
	self, visit := w.GetSelf(), s.encounterVisit
	return s.selfGUID != 0 && self.GetGuid() == s.selfGUID && self.GetPresent() && self.GetMapKnown() &&
		visit.bound && w.GetVisitRevision() != 0 && w.GetVisitRevision() == visit.revision && self.GetMapId() == visit.mapID
}

// RenewPolicyEncounterLease runs on the private module UDS after the attached
// host successfully consumes a current encounter feed and evaluates Lua.
func (s *Service) RenewPolicyEncounterLease(ctx context.Context, request *wrapperspb.UInt64Value) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.trustedEncounterHostLocked(ctx) {
		return nil, status.Error(codes.PermissionDenied, "current encounter host required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	visit := request.GetValue()
	if visit == 0 {
		s.hostEncounterVisit, s.hostEncounterUntil = 0, time.Time{}
		s.stopEncounterMeleeLocked(ctx)
		return &emptypb.Empty{}, nil
	}
	if s.closed || !s.encounterVisit.bound || s.encounterVisit.revision != visit {
		return nil, status.Error(codes.FailedPrecondition, "current observed encounter visit required")
	}
	if !s.hostEncounterActiveLocked() {
		if s.encounterSwing.target != 0 {
			s.stopEncounterMeleeLocked(ctx)
			return nil, status.Error(codes.FailedPrecondition, "previous encounter swing awaits server stop")
		}
		if s.crowdControl.active {
			return nil, status.Error(codes.FailedPrecondition, "native crowd control approach still owns movement")
		}
		w, err := s.readWorldSnapshot(ctx)
		if err != nil || !s.matchesCurrentEncounterVisitLocked(w) {
			return nil, status.Error(codes.FailedPrecondition, "current encounter visit must match World observations")
		}
		// A previous native swing must settle before any exact Lua cast can
		// be admitted. Do not clear uncertain transport or pending casts.
		s.stopAutoAttackLocked(ctx)
	}
	s.hostEncounterVisit, s.hostEncounterUntil = visit, s.now().Add(encounterLeaseDuration)
	s.hostResurrectionOwned, s.hostInterruptOwned = modcb.IncomingPolicySupportOwnership(ctx)
	return &emptypb.Empty{}, nil
}

func (s *Service) hasPolicyEncounterAccessLocked(ctx context.Context) bool {
	visit := modcb.IncomingPolicyEncounterVisit(ctx)
	return visit != 0 && s.trustedEncounterHostLocked(ctx) && s.hostEncounterActiveLocked() && visit == s.hostEncounterVisit
}

func (s *Service) admitsPolicyEncounterLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if s.hostEncounterKey == "" {
		return true // Existing exact policy bindings on baseline profiles.
	}
	return s.hasPolicyEncounterAccessLocked(ctx) && s.matchesCurrentEncounterVisitLocked(w)
}

func (s *Service) inactiveNativeEncounterLocked(enable bool) (*actionsv1.State, error) {
	if enable {
		return nil, status.Error(codes.FailedPrecondition, "host encounter profile cannot arm a separate native combat controller")
	}
	// There is no native intent to stop. An exact host action can be in flight.
	return s.stateLocked(), nil
}
