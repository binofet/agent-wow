package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestInteractionStopRebasesAuthoritativeCorrection(t *testing.T) {
	s, f, _ := rig(t)
	toggle(t, s, true)
	s.Tick(context.Background())
	f.world.Self.PositionRevision++
	f.world.Self.X = 100
	got, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "quests"})
	if err != nil || got.State.Position.X != 100 || !got.State.Following || got.State.Moving {
		t.Fatalf("corrected lease %+v err %v", got, err)
	}
	if len(f.packets) != 2 {
		t.Fatalf("packets %d", len(f.packets))
	}
	_, n, _ := readGUID(f.packets[1].Payload)
	if x := math.Float32frombits(binary.LittleEndian.Uint32(f.packets[1].Payload[n+10:])); x != 100 {
		t.Fatalf("STOP used superseded estimate: %v", x)
	}
}

func TestFollowPausesTransientJumpAndAFKThenResumes(t *testing.T) {
	s, f, advance := rig(t)
	toggle(t, s, true)
	s.Tick(context.Background())
	for _, flags := range []uint32{0x1000, 0} {
		f.world.Owner.MovementFlags = flags
		if flags == 0 {
			f.world.Owner.PlayerFlags = 2
		}
		advance(200 * time.Millisecond)
		s.Tick(context.Background())
		if got := state(t, s); !got.Following || got.Moving {
			t.Fatalf("unsafe intent %+v", got)
		}
	}
	f.world.Owner.PlayerFlags = 0
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if got := state(t, s); !got.Following || !got.Moving || got.FollowLeaseRemainingMs < uint64((3*time.Hour)/time.Millisecond) {
		t.Fatalf("safe follow did not resume %+v", got)
	}
}

func TestPositionReportsAuthoritativeFallbackAndClientEstimate(t *testing.T) {
	s, f, advance := rig(t)
	initial := state(t, s).Position
	if !initial.Known || initial.Estimated || initial.X != 0 {
		t.Fatalf("initial %+v", initial)
	}
	toggle(t, s, true)
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	pose := state(t, s).Position
	if !pose.Known || !pose.Estimated || pose.X < 1.39 || pose.X > 1.41 || pose.MapId != 0 {
		t.Fatalf("estimate %+v", pose)
	}
	advance(time.Second)
	aged := state(t, s).Position
	if aged.AgeMs != 1000 {
		t.Fatalf("age %+v", aged)
	}
	f.world.Self.PositionRevision++
	f.world.Self.X = 100
	pose = state(t, s).Position
	if pose.Estimated || pose.X != 100 {
		t.Fatalf("correction fallback %+v", pose)
	}
	if !state(t, s).Following {
		t.Fatal("read-only status changed follow intent")
	}
	f.world.Self.PositionKnown = false
	if state(t, s).Position.Known {
		t.Fatal("invalid authoritative base preserved estimate")
	}
}

func TestInteractionLeasePausesAndResumesIntentExclusively(t *testing.T) {
	s, f, advance := rig(t)
	f.world.Owner.Health = 100
	toggle(t, s, true)
	_, _ = s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
	s.Tick(context.Background())
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000})
	if err != nil || len(lease.Token) != 32 || !lease.State.Following || !lease.State.Healing || lease.State.Moving || lease.State.InteractionOwner != "loot" {
		t.Fatalf("acquire %+v err%v", lease, err)
	}
	if len(f.packets) != 2 {
		t.Fatal("lease did not stop movement")
	}
	advance(5 * time.Second)
	s.Tick(context.Background())
	if len(f.packets) != 2 {
		t.Fatal("movement during interaction")
	}
	f.world.Owner.Health = 50
	learn(s)
	blocked, _ := s.HealOnce(context.Background(), &emptypb.Empty{})
	if blocked.CastPending || len(f.packets) != 2 {
		t.Fatal("heal_once bypassed lease")
	}
	if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "trade"}); err == nil {
		t.Fatal("other owner stole lease")
	}
	if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "loot", Token: "wrong"}); err == nil {
		t.Fatal("wrong token released lease")
	}
	renewed, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", Token: lease.Token, LeaseMs: 30000})
	if err != nil || renewed.Token != lease.Token || renewed.State.InteractionLeaseRemainingMs != 30000 {
		t.Fatalf("renew %+v err%v", renewed, err)
	}
	wire, _ := protojson.Marshal(state(t, s))
	if strings.Contains(string(wire), lease.Token) {
		t.Fatal("token leaked in status")
	}
	f.world.Owner.Health = 100
	if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "loot", Token: lease.Token}); err != nil {
		t.Fatal(err)
	}
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if !state(t, s).Moving || !state(t, s).Following || !state(t, s).Healing {
		t.Fatalf("intent not resumed %+v", state(t, s))
	}
}

func TestInteractionExpiryAndFailedStop(t *testing.T) {
	s, f, advance := rig(t)
	toggle(t, s, true)
	s.Tick(context.Background())
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "quests", LeaseMs: 1000})
	if err != nil {
		t.Fatal(err)
	}
	advance(2 * time.Second)
	s.Tick(context.Background())
	if got := state(t, s); got.InteractionOwner != "" || !got.Moving || !got.Following {
		t.Fatalf("expiry did not resume %+v", got)
	}
	if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "quests", Token: lease.Token}); err == nil {
		t.Fatal("expired token accepted")
	}
	f.failClock = true
	if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot"}); err == nil {
		t.Fatal("lease granted after stop failure")
	}
	if !state(t, s).MotionUnconfirmed {
		t.Fatal("stop failure not latched")
	}
}

func TestInteractionInputsAreBoundedAndLogoutClearsLease(t *testing.T) {
	s, _, _ := rig(t)
	for _, r := range []*actionsv1.InteractionRequest{nil, {Owner: "shell"}, {Owner: "loot", LeaseMs: 999}, {Owner: "trade", LeaseMs: 30001}, {Owner: "quests", Token: "stale"}} {
		if _, err := s.AcquireInteraction(context.Background(), r); err == nil {
			t.Fatalf("accepted invalid %+v", r)
		}
	}
	if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "trade"}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.BeforeLogout(context.Background(), &emptypb.Empty{})
	if state(t, s).InteractionOwner != "" {
		t.Fatal("logout retained lease")
	}
}
