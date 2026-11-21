package buddyactions

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type movementOwner struct {
	owner, token string
	until        time.Time
	revision     uint64
}

func (s *Service) expireMovementOwnerLocked() {
	if s.movementOwner.owner != "" && !s.now().Before(s.movementOwner.until) {
		s.movementOwner.owner, s.movementOwner.token = "", ""
		s.movementOwner.until = time.Time{}
		s.movementOwner.revision++
		// Follow resumes from a new physical integration boundary, not a delayed
		// movement delta accumulated while another writer held custody.
		s.lastStep = s.now()
	}
}
func (s *Service) movementOwnerStateLocked() *actionsv1.MovementOwnerState {
	s.expireMovementOwnerLocked()
	out := &actionsv1.MovementOwnerState{Revision: s.movementOwner.revision}
	if s.movementOwner.owner != "" {
		out.Active, out.Owner = true, s.movementOwner.owner
		out.RemainingMs = uint64(s.movementOwner.until.Sub(s.now()).Milliseconds())
	}
	return out
}
func validMovementOwner(owner string) bool {
	return owner != "" && strings.TrimSpace(owner) == owner && utf8.ValidString(owner)
}

// AcquireMovement grants only temporary custody of otherwise autonomous idle
// and follow movement. It changes no standing intent or combat/support writer.
func (s *Service) AcquireMovement(ctx context.Context, r *actionsv1.MovementOwnerRequest) (*actionsv1.MovementOwnerLease, error) {
	if r == nil || r.SessionId == "" || r.SelfGuid == 0 || !validMovementOwner(r.Owner) || r.LeaseMs == 0 {
		return nil, status.Error(codes.InvalidArgument, "current body, nonempty owner and positive finite lease_ms required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current connected body required")
	}
	s.expireMovementOwnerLocked()
	if s.movementOwner.owner != "" {
		if r.Owner != s.movementOwner.owner || r.Token == "" || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.movementOwner.token)) != 1 {
			return nil, status.Error(codes.FailedPrecondition, "another finite movement owner is active")
		}
	} else {
		if r.Token != "" {
			return nil, status.Error(codes.FailedPrecondition, "movement lease expired; acquire a new token")
		}
		if s.bodyMoving {
			return nil, status.Error(codes.FailedPrecondition, "bounded body input still owns movement")
		}
		// Stop already-written idle/follow motion before handing that writer over.
		// This never stops a swing or a cast. Unknown transport remains unknown.
		if !s.targetMovementActiveLocked() && !s.stopLocked(ctx) {
			if s.state.MotionUnconfirmed {
				return nil, status.Error(codes.Unknown, "prior idle movement stop transport outcome is unknown")
			}
			return nil, status.Error(codes.FailedPrecondition, "prior idle movement stop is not confirmed written")
		}
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return nil, status.Error(codes.Internal, "movement token unavailable")
		}
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		s.movementOwner.owner, s.movementOwner.token = r.Owner, hex.EncodeToString(token)
		s.movementOwner.revision++
	}
	s.movementOwner.until = s.now().Add(time.Duration(r.LeaseMs) * time.Millisecond)
	s.lastStep = s.now()
	return &actionsv1.MovementOwnerLease{Token: s.movementOwner.token, State: s.stateLocked()}, nil
}

func (s *Service) ReleaseMovement(ctx context.Context, r *actionsv1.MovementOwnerRelease) (*actionsv1.State, error) {
	if r == nil || r.SessionId == "" || r.SelfGuid == 0 || !validMovementOwner(r.Owner) || r.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "current body and exact movement owner token required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if s.closed || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current connected body required")
	}
	s.expireMovementOwnerLocked()
	if r.Owner != s.movementOwner.owner || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.movementOwner.token)) != 1 {
		return nil, status.Error(codes.FailedPrecondition, "movement owner token is no longer current")
	}
	s.movementOwner.owner, s.movementOwner.token = "", ""
	s.movementOwner.until = time.Time{}
	s.movementOwner.revision++
	s.lastStep = s.now()
	return s.stateLocked(), nil
}
