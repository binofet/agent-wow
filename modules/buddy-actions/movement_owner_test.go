package buddyactions

import (
	"context"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func acquireMovementForTest(t *testing.T, s *Service, ms uint32) *actionsv1.MovementOwnerLease {
	t.Helper()
	lease, err := s.AcquireMovement(context.Background(), &actionsv1.MovementOwnerRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, Owner: "task:wait-for-pull", LeaseMs: ms})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}
func TestMovementOwnerExpiresWithoutChangingFollowIntentOrRequiringModel(t *testing.T) {
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	toggle(t, s, true)
	s.Tick(context.Background())
	if !s.state.Moving {
		t.Fatal("fixture did not follow")
	}
	lease := acquireMovementForTest(t, s, 1000)
	if lease.Token == "" || !lease.State.MovementOwner.Active || lease.State.Moving || !lease.State.Following {
		t.Fatal(lease)
	}
	before := len(f.packets)
	advance(500 * time.Millisecond)
	s.Tick(context.Background())
	if len(f.packets) != before || s.state.Moving || !s.state.Following || s.state.Reason != "finite_movement_owner_active" {
		t.Fatal("lease failed to exclude only follow movement", s.stateLocked(), f.packets)
	}
	// No controller task or model is running to release it: expiry alone resumes.
	advance(501 * time.Millisecond)
	s.Tick(context.Background())
	if state(t, s).MovementOwner.Active || !s.state.Following || !s.state.Moving || len(f.packets) != before+1 || f.packets[before].Opcode != opcode.MSGMoveStartForward {
		t.Fatal("finite custody became a sticky follow disable", s.stateLocked(), f.packets)
	}
}
func TestMovementOwnerRenewsAndReleasesOnlyExactCurrentSessionToken(t *testing.T) {
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	lease := acquireMovementForTest(t, s, 1000)
	for _, mode := range []string{"session", "body", "owner", "token"} {
		r := &actionsv1.MovementOwnerRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, Owner: "task:wait-for-pull", Token: lease.Token, LeaseMs: 2000}
		switch mode {
		case "session":
			r.SessionId = "old"
		case "body":
			r.SelfGuid++
		case "owner":
			r.Owner = "peer"
		case "token":
			r.Token = "wrong"
		}
		if _, err := s.AcquireMovement(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("foreign renewal accepted", mode, err)
		}
	}
	advance(750 * time.Millisecond)
	renewed, err := s.AcquireMovement(context.Background(), &actionsv1.MovementOwnerRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, Owner: "task:wait-for-pull", Token: lease.Token, LeaseMs: 2000})
	if err != nil || renewed.Token != lease.Token {
		t.Fatal(renewed, err)
	}
	raw, _ := protojson.Marshal(state(t, s))
	if strings.Contains(string(raw), lease.Token) {
		t.Fatal("private lease token appeared in public observation")
	}
	if _, err = s.ReleaseMovement(context.Background(), &actionsv1.MovementOwnerRelease{SessionId: s.sessionID, SelfGuid: s.selfGUID, Owner: "task:wait-for-pull", Token: "wrong"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("foreign release accepted", err)
	}
	out, err := s.ReleaseMovement(context.Background(), &actionsv1.MovementOwnerRelease{SessionId: s.sessionID, SelfGuid: s.selfGUID, Owner: "task:wait-for-pull", Token: lease.Token})
	if err != nil || out.MovementOwner.Active {
		t.Fatal(out, err)
	}
	if _, err = s.AcquireMovement(context.Background(), &actionsv1.MovementOwnerRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, Owner: "task:wait-for-pull", Token: lease.Token, LeaseMs: 1000}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("retired token reacquired custody", err)
	}
}
func TestMovementOwnerLeavesLearnedHealingAndPartyDefenseAvailable(t *testing.T) {
	t.Run("heal", func(t *testing.T) {
		s, f, _ := rig(t)
		s.sessionID = f.party.SessionId
		learn(s)
		toggle(t, s, true)
		acquireMovementForTest(t, s, 3000)
		s.Tick(context.Background())
		if !s.state.CastPending || s.state.LastCastTargetGuid != 3 || !s.state.Following || !s.stateLocked().MovementOwner.Active {
			t.Fatal("movement custody disabled healing", s.stateLocked(), f.packets)
		}
	})
	t.Run("defense", func(t *testing.T) {
		s, f, c, _ := partyDefenseRig(t, 8)
		s.sessionID = f.party.SessionId
		acquireMovementForTest(t, s, 3000)
		s.Tick(context.Background())
		if s.state.AssistAttempts != 1 || s.state.LastCastTargetGuid != c.Guid || lastCast(f) == 0 || !s.stateLocked().MovementOwner.Active {
			t.Fatal("movement custody disabled actual party defense", s.stateLocked(), f.packets)
		}
	})
}
func TestMovementOwnerAllowsExplicitLuaApproachAndFiniteBodyInput(t *testing.T) {
	s, f, r, ctx, _ := targetMovementFixture(t)
	acquireMovementForTest(t, s, 3000)
	out, err := s.ApproachTarget(ctx, r)
	if err != nil || !out.Moving || out.TargetMovement.Phase != "approaching" || !out.MovementOwner.Active {
		t.Fatal("lease blocked explicit current-body movement", out, err, f.packets)
	}
}
func TestMovementOwnerDoesNotFakeStopOrStealFiniteHop(t *testing.T) {
	for _, mode := range []string{"clock", "transport", "hop"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			s.sessionID = f.party.SessionId
			toggle(t, s, true)
			s.Tick(context.Background())
			before := len(f.packets)
			switch mode {
			case "clock":
				f.failClock = true
			case "transport":
				f.failSend = true
			case "hop":
				s.bodyMoving = true
			}
			lease, err := s.AcquireMovement(context.Background(), &actionsv1.MovementOwnerRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, Owner: "task:wait", LeaseMs: 3000})
			want := codes.FailedPrecondition
			if mode == "transport" {
				want = codes.Unknown
			}
			if status.Code(err) != want || lease != nil || s.movementOwner.owner != "" {
				t.Fatal("failed stop granted custody", lease, err)
			}
			if mode == "transport" {
				if !s.state.MotionUnconfirmed || len(f.packets) != before+1 {
					t.Fatal("ambiguous stop was not fenced", s.stateLocked())
				}
			} else if s.state.MotionUnconfirmed || !s.state.Following || len(f.packets) != before {
				t.Fatal("known no-write stop lost standing intent", s.stateLocked())
			}
		})
	}
}
