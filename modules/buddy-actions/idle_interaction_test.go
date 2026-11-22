package buddyactions

import (
	"context"
	"encoding/hex"
	"reflect"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// These snapshots deliberately avoid Status/stateLocked: either can expire or
// reconcile custody, obscuring an acquisition's otherwise observable effects.
func idleRawSnapshot(t *testing.T, s *Service) map[string]any {
	t.Helper()
	wire := func(m proto.Message) []byte {
		if m == nil || reflect.ValueOf(m).IsNil() {
			return nil
		}
		b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	out := map[string]any{
		"state": wire(&s.state), "token": s.interactionToken, "until": s.interactionUntil,
		"binding": s.idleInteraction, "lastStep": s.lastStep,
		"lastWorld": wire(s.lastWorld), "lastWorldPointer": s.lastWorld,
		"position":       []any{s.pos, s.posKnown, s.positionRevision, s.posMap, s.posEstimated, s.posAt, s.posRevision, s.movementSequence},
		"standing":       []any{s.followUntil, s.assistUntil, s.nextAssist, s.nextHeal, s.castUntil, s.castCount, s.cancelCastCount},
		"custody":        []any{s.closed, s.encounterVisit, s.movementOwner, s.bodyMoving, s.targetMovementExecuting, s.questApproaching, s.clientControlLost, s.clientControlRestoring, s.followGoalExecuting, s.recovery.token},
		"attack":         []any{s.pendingSpell, s.pendingTarget, s.autoTarget, s.autoStopping, s.encounterSwing},
		"targetMovement": wire(&s.targetMovement.view), "targetUntil": s.targetMovement.until,
		"recovery": wire(&s.recovery.state), "supportPointer": s.supportOp, "buffPointer": s.buffOp, "meleePointer": s.meleeOp,
	}
	if s.supportOp != nil {
		out["support"] = wire(&s.supportOp.view)
		out["supportPrivate"] = []any{s.supportOp.count, s.supportOp.until, s.supportOp.policySpellToken}
	}
	if s.buffOp != nil {
		out["buff"] = wire(&s.buffOp.view)
		out["buffPrivate"] = []any{s.buffOp.count, s.buffOp.until, s.buffOp.policySpellToken}
	}
	if s.meleeOp != nil {
		out["melee"] = wire(&s.meleeOp.view)
		out["meleePrivate"] = []any{s.meleeOp.token, s.meleeOp.until}
	}
	return out
}

func idleInteractionRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.IdleInteractionRequest) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = "idle-test-body"
	s.encounterVisit = encounterVisit{revision: 1, mapID: 0, bound: true}
	f.world.VisitRevision = 1
	s.state.Following, s.state.Assisting = true, true
	s.state.Reason = "standing_intent_must_survive"
	s.followUntil, s.assistUntil = s.now().Add(time.Minute), s.now().Add(time.Minute)
	s.lastWorld = proto.Clone(f.world).(*worldv1.SnapshotResponse)
	s.pos, s.posKnown, s.positionRevision = position{x: 1, y: 2, z: 3, o: .5}, true, 1
	s.posEstimated, s.posRevision, s.posAt = true, 7, s.now().Add(-time.Second)
	return s, f, advance, &actionsv1.IdleInteractionRequest{SessionId: s.sessionID, SelfGuid: 1, VisitId: 1, Owner: "trade", LeaseMs: 6000}
}

func idleNoEffects(t *testing.T, s *Service, f *fakeSession, before map[string]any) {
	t.Helper()
	if got := idleRawSnapshot(t, s); !reflect.DeepEqual(before, got) {
		t.Fatalf("refusal changed raw Actions state\nbefore: %#v\nafter: %#v", before, got)
	}
	if len(f.packets) != 0 || f.clock != 0 || len(f.movementObservations) != 0 {
		t.Fatalf("acquisition produced transport effects: packets=%d clock=%d movements=%d", len(f.packets), f.clock, len(f.movementObservations))
	}
}

func TestIdleInteractionAcquiresAndRenewsWithoutAdoptingStandingIntent(t *testing.T) {
	s, f, advance, r := idleInteractionRig(t)
	// Expired foreign movement metadata is retained, not expired by stateLocked.
	s.movementOwner = movementOwner{owner: "expired-owner", token: "old", until: s.now().Add(-time.Second), revision: 9}
	s.supportOp = &supportAttempt{view: actionsv1.SpellOperation{Phase: "confirmed_effect", SpellId: 118, TargetGuid: 17}, count: 4, until: s.now().Add(-time.Second)}
	before := idleRawSnapshot(t, s)
	started := s.now()
	lease, err := s.AcquireIdleInteraction(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if token, err := hex.DecodeString(lease.Token); err != nil || len(token) != 16 {
		t.Fatalf("not a finite fresh opaque token: %q", lease.Token)
	}
	if s.interactionUntil != started.Add(6*time.Second) || s.lastStep != started || s.idleInteraction != (idleInteractionBinding{s.sessionID, 1, 1, lease.Token}) {
		t.Fatal("lease was not bound to the exact body and visit")
	}
	for _, k := range []string{"state", "token", "until", "binding", "lastStep"} {
		delete(before, k)
	}
	after := idleRawSnapshot(t, s)
	for _, k := range []string{"state", "token", "until", "binding", "lastStep"} {
		delete(after, k)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("success changed unrelated raw state")
	}
	if !s.state.Following || !s.state.Assisting || s.state.InteractionOwner != "trade" || s.state.Reason != "standing_intent_must_survive" {
		t.Fatal("standing intent was adopted or cleared")
	}
	if lease.State.Position == nil || !lease.State.Position.Estimated || lease.State.Position.Revision != 7 || lease.State.Position.X != 1 {
		t.Fatal("detached response lost accepted position observation")
	}
	lease.State.Reason = "caller mutation"
	lease.State.Position.X = 999
	if s.state.Reason == "caller mutation" || s.pos.x != 1 {
		t.Fatal("response aliases native state")
	}
	advance(time.Second)
	r.Token = lease.Token
	renewed, err := s.AcquireIdleInteraction(context.Background(), r)
	if err != nil || renewed.Token != r.Token || s.interactionUntil != s.now().Add(6*time.Second) {
		t.Fatalf("exact renewal failed: %v", err)
	}
	if len(f.packets) != 0 || f.clock != 0 || len(f.movementObservations) != 0 {
		t.Fatal("idle acquire/renew must not stop, move or attack")
	}
}

func TestIdleInteractionRejectsUnsettledActionsWithoutRetiringThem(t *testing.T) {
	cases := map[string]func(*Service, *fakeSession){
		"cast_pending":   func(s *Service, _ *fakeSession) { s.state.CastPending = true },
		"pending_spell":  func(s *Service, _ *fakeSession) { s.pendingSpell = 118 },
		"pending_target": func(s *Service, _ *fakeSession) { s.pendingTarget = 9 },
		"moving":         func(s *Service, _ *fakeSession) { s.state.Moving = true },
		"unknown_motion": func(s *Service, _ *fakeSession) { s.state.MotionUnconfirmed = true },
		"body_moving":    func(s *Service, _ *fakeSession) { s.bodyMoving = true },
		"autoattack":     func(s *Service, _ *fakeSession) { s.state.AutoAttackRequested = true; s.autoTarget = 9 },
		"melee_pending": func(s *Service, _ *fakeSession) {
			s.meleeOp = &meleeAttempt{view: actionsv1.MeleeOperation{Phase: "waiting_server", TargetGuid: 9}, token: "pending", until: s.now().Add(-time.Second)}
		},
		"foreign_movement": func(s *Service, _ *fakeSession) {
			s.movementOwner = movementOwner{owner: "task", token: "foreign", until: s.now().Add(time.Second)}
		},
		"target_movement_stale_host": func(s *Service, _ *fakeSession) {
			s.targetMovement.view.TargetGuid = 9
			s.targetMovement.view.VisitId = 1
			s.targetMovement.until = s.now().Add(time.Second)
			s.hostEncounterKey = "expired"
			s.hostEncounterUntil = s.now().Add(-time.Second)
		},
		"recovery":     func(s *Service, _ *fakeSession) { s.recovery.token = "pending-reclaim" },
		"harvest":      func(s *Service, _ *fakeSession) { s.state.HarvestGuardGuids = []uint64{9} },
		"control_lost": func(s *Service, _ *fakeSession) { s.clientControlLost = true },
	}
	for _, phase := range []string{"waiting_server", "waiting_channel_observation", "channeling", "waiting_cleanse_effect", "waiting_created_item", "transport_unconfirmed_no_retry", "outcome_unconfirmed_no_retry"} {
		phase := phase
		cases["support_"+phase] = func(s *Service, _ *fakeSession) {
			s.supportOp = &supportAttempt{view: actionsv1.SpellOperation{Phase: phase, SpellId: 118}, count: 8, until: s.now().Add(-time.Minute)}
		}
	}
	for _, phase := range []string{"waiting_server", "outcome_unconfirmed"} {
		phase := phase
		cases["buff_"+phase] = func(s *Service, _ *fakeSession) {
			s.buffOp = &buffAttempt{view: actionsv1.BuffOperation{Phase: phase, SpellId: 1243}, count: 3, until: s.now().Add(-time.Minute)}
		}
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, _, r := idleInteractionRig(t)
			mutate(s, f)
			before := idleRawSnapshot(t, s)
			if _, err := s.AcquireIdleInteraction(context.Background(), r); err == nil {
				t.Fatal("unsettled custody admitted")
			}
			idleNoEffects(t, s, f, before)
		})
	}
}

func TestIdleInteractionRequiresCurrentKnownLivingStationaryWorld(t *testing.T) {
	cases := map[string]func(*Service, *fakeSession){
		"provider_error":         func(_ *Service, f *fakeSession) { f.failSnapshot = true },
		"different_body":         func(_ *Service, f *fakeSession) { f.world.Self.Guid = 2 },
		"same_map_new_visit":     func(_ *Service, f *fakeSession) { f.world.VisitRevision = 2 },
		"local_visit_changed":    func(s *Service, _ *fakeSession) { s.encounterVisit.revision = 2 },
		"visit_unbound":          func(s *Service, _ *fakeSession) { s.encounterVisit.bound = false },
		"health_unknown":         func(_ *Service, f *fakeSession) { f.world.Self.HealthKnown = false },
		"dead":                   func(_ *Service, f *fakeSession) { f.world.Self.Health = 0 },
		"invalid_health":         func(_ *Service, f *fakeSession) { f.world.Self.Health = 101 },
		"unknown_movement":       func(_ *Service, f *fakeSession) { f.world.Self.MovementFlagsKnown = false },
		"motion":                 func(_ *Service, f *fakeSession) { f.world.Self.MovementFlags = 1 },
		"combat":                 func(_ *Service, f *fakeSession) { f.world.Self.UnitFlags = 0x80000 },
		"pose_unversioned":       func(_ *Service, f *fakeSession) { f.world.Self.PositionRevision = 0 },
		"channel_unknown":        func(_ *Service, f *fakeSession) { f.world.Self.ChannelSpellKnown = false },
		"channel_active":         func(_ *Service, f *fakeSession) { f.world.Self.ChannelSpellId = 10 },
		"channel_object_unknown": func(_ *Service, f *fakeSession) { f.world.Self.ChannelObjectKnown = false },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, _, r := idleInteractionRig(t)
			mutate(s, f)
			before := idleRawSnapshot(t, s)
			if _, err := s.AcquireIdleInteraction(context.Background(), r); err == nil {
				t.Fatal("unsafe/unknown World admitted")
			}
			idleNoEffects(t, s, f, before)
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		s, f, _, r := idleInteractionRig(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		before := idleRawSnapshot(t, s)
		if _, err := s.AcquireIdleInteraction(ctx, r); err == nil {
			t.Fatal("cancelled request admitted")
		}
		idleNoEffects(t, s, f, before)
	})
}

func TestIdleInteractionRenewalRejectsOldForeignOrMalformedBindingsInertly(t *testing.T) {
	cases := map[string]func(*Service, *actionsv1.IdleInteractionRequest, func(time.Duration)){
		"wrong_token":   func(_ *Service, r *actionsv1.IdleInteractionRequest, _ func(time.Duration)) { r.Token = "wrong" },
		"missing_token": func(_ *Service, r *actionsv1.IdleInteractionRequest, _ func(time.Duration)) { r.Token = "" },
		"foreign_owner": func(_ *Service, r *actionsv1.IdleInteractionRequest, _ func(time.Duration)) { r.Owner = "mail" },
		"foreign_session": func(_ *Service, r *actionsv1.IdleInteractionRequest, _ func(time.Duration)) {
			r.SessionId = "replacement"
		},
		"foreign_body":  func(_ *Service, r *actionsv1.IdleInteractionRequest, _ func(time.Duration)) { r.SelfGuid = 2 },
		"foreign_visit": func(_ *Service, r *actionsv1.IdleInteractionRequest, _ func(time.Duration)) { r.VisitId = 2 },
		"expired": func(_ *Service, _ *actionsv1.IdleInteractionRequest, advance func(time.Duration)) {
			advance(6 * time.Second)
		},
		"binding_malformed": func(s *Service, _ *actionsv1.IdleInteractionRequest, _ func(time.Duration)) {
			s.idleInteraction.token = "other"
		},
		"legacy_token": func(s *Service, _ *actionsv1.IdleInteractionRequest, _ func(time.Duration)) {
			s.idleInteraction = idleInteractionBinding{}
		},
		"orphan_token": func(s *Service, _ *actionsv1.IdleInteractionRequest, _ func(time.Duration)) {
			s.state.InteractionOwner = ""
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, advance, r := idleInteractionRig(t)
			lease, err := s.AcquireIdleInteraction(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			r.Token = lease.Token
			mutate(s, r, advance)
			before := idleRawSnapshot(t, s)
			if _, err := s.AcquireIdleInteraction(context.Background(), r); err == nil {
				t.Fatal("invalid renewal admitted")
			}
			idleNoEffects(t, s, f, before)
		})
	}
}

// The callback is synchronous and runs while Actions owns mu. It models a
// final-read boundary without sleeps, Status side effects or a second peer read.
type idleReadSession struct {
	*fakeSession
	calls     []string
	afterRead func()
}

func (f *idleReadSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, options ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	f.calls = append(f.calls, r.Module+"."+r.Method)
	response, err := f.fakeSession.InvokeModule(ctx, r, options...)
	if f.afterRead != nil {
		f.afterRead()
	}
	return response, err
}

func TestIdleInteractionFinalReadDeadlineCancellationAndLocalRebind(t *testing.T) {
	for _, name := range []string{"deadline", "clock_regression", "cancelled", "local_visit", "foreign_custody", "renewal_expires"} {
		t.Run(name, func(t *testing.T) {
			s, f, advance, r := idleInteractionRig(t)
			if name == "renewal_expires" {
				lease, err := s.AcquireIdleInteraction(context.Background(), r)
				if err != nil {
					t.Fatal(err)
				}
				r.Token = lease.Token
				advance(5 * time.Second)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wrapper := &idleReadSession{fakeSession: f}
			s.session = wrapper
			var expected map[string]any
			wrapper.afterRead = func() {
				switch name {
				case "deadline":
					advance(6 * time.Second)
				case "clock_regression":
					advance(-time.Second)
				case "cancelled":
					cancel()
				case "local_visit":
					s.encounterVisit.revision = 2
				case "foreign_custody":
					s.movementOwner = movementOwner{owner: "new-task", until: s.now().Add(time.Second)}
				case "renewal_expires":
					advance(time.Second)
				}
				expected = idleRawSnapshot(t, s)
			}
			if _, err := s.AcquireIdleInteraction(ctx, r); err == nil {
				t.Fatal("final boundary admitted stale custody")
			}
			if !reflect.DeepEqual(wrapper.calls, []string{"buddy-world.snapshot"}) {
				t.Fatalf("unexpected dependencies: %v", wrapper.calls)
			}
			idleNoEffects(t, s, f, expected)
		})
	}
	t.Run("success_last_dependency", func(t *testing.T) {
		s, f, _, r := idleInteractionRig(t)
		wrapper := &idleReadSession{fakeSession: f}
		s.session = wrapper
		if _, err := s.AcquireIdleInteraction(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(wrapper.calls, []string{"buddy-world.snapshot"}) || len(f.packets) != 0 || f.clock != 0 {
			t.Fatal("acquire performed another dependency or write")
		}
	})
}

func TestIdleInteractionDoesNotAddALegacyRenewalFence(t *testing.T) {
	s, f, _, r := idleInteractionRig(t)
	lease, err := s.AcquireIdleInteraction(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy renewal intentionally keeps its original contract, even if the
	// new-route binding no longer matches. It must not call the new admission.
	s.idleInteraction.visit = 2
	f.failSnapshot = true
	legacy, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: r.Owner, Token: lease.Token, LeaseMs: 7000})
	if err != nil || legacy.Token != lease.Token || s.interactionUntil != s.now().Add(7*time.Second) {
		t.Fatalf("legacy renewal changed: %v", err)
	}
	if len(f.packets) != 0 {
		t.Fatal("legacy renewal unexpectedly wrote")
	}
}
