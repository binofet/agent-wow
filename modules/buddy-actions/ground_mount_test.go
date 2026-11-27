package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func mountForTravel(t *testing.T, s *Service, p *worldv1.PlayerState, spell uint32) {
	t.Helper()
	p.UnitFlags |= movementflags.Mounted
	p.RunSpeed = 14
	_, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdateAll, Payload: buffAura(p.Guid, p.Guid, 7, spell, 0)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryGroundMountFollowAndStop(t *testing.T) {
	for _, who := range []string{"self", "owner", "both"} {
		t.Run(who, func(t *testing.T) {
			s, f, advance := formationRig(t, 1, 5)
			f.world.Owner.X += 15
			if who != "owner" {
				mountForTravel(t, s, f.world.Self, 23238)
			}
			if who != "self" {
				mountForTravel(t, s, f.world.Owner, 23240)
			}
			toggle(t, s, true)
			for i := 0; i < 4; i++ {
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
			}
			if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) == 0 {
				t.Fatal(s.stateLocked(), f.packets)
			}
			if _, err := s.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: false}); err != nil {
				t.Fatal(err)
			}
			if s.state.Moving || s.state.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal(s.stateLocked())
			}
		})
	}
}

func TestOrdinaryGroundMountTravelTo(t *testing.T) {
	for _, tc := range []struct {
		name               string
		spell, move, other uint32
		goes               bool
	}{
		{"ordinary", 23238, 0, 0, true}, {"mount flag alone", 0, 0, 0, false},
		{"passenger mount", 60424, 0, 0, false}, {"flight mount", 32235, 0, 0, false},
		{"taxi", 23238, 0, 0x00100000, false}, {"transport", 23238, 0x200, 0, false},
		{"swimming", 23238, 0x200000, 0, false}, {"falling", 23238, 0x1000, 0, false}, {"combat", 23238, 0, 0x80000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _, r := explorationRig(t)
			mountForTravel(t, s, f.world.Self, tc.spell)
			f.world.Self.MovementFlags = tc.move
			f.world.Self.UnitFlags |= tc.other
			got, err := s.TravelTo(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if (countPackets(f, opcode.MSGMoveStartForward) > 0) != tc.goes || tc.goes && (got.Reason != "exploration_within_distance" || got.Moving || got.MotionUnconfirmed) {
				t.Fatal(got, f.packets)
			}
		})
	}
}

func TestFollowMountPreparationRechecksSelectionAndStopsOnce(t *testing.T) {
	for _, change := range []string{"current", "manual_stop", "combat", "swimming", "another_mount", "unknown_motion"} {
		t.Run(change, func(t *testing.T) {
			s, f, _, ctx, advance := targetMovementFixture(t)
			toggle(t, s, true)
			learnSpells(s, 23238)
			mountForTravel(t, s, f.world.Owner, 23240)
			f.world.Owner.X += 15
			for i := 0; i < 8 && !s.state.Moving; i++ {
				s.Tick(ctx)
				advance(100 * time.Millisecond)
			}
			obs := s.followMountObservationLocked(ctx, f.world)
			if !obs.Ready || !obs.FollowMoving || countPackets(f, opcode.MSGMoveStartForward) != 1 {
				t.Fatal(s.stateLocked(), obs, f.packets)
			}
			guard := &actionsv1.FollowMountGuard{OwnerGuid: f.world.Owner.Guid, FollowRevision: s.followRevision, OwnerMountRevision: obs.OwnerMountRevision}
			r := &actionsv1.MovementOwnerRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: f.world.VisitRevision, SpellId: 23238, FollowMount: guard, Owner: "ground_mount_follow", LeaseMs: 3000}
			switch change {
			case "manual_stop":
				toggle(t, s, false)
			case "combat":
				f.world.Self.UnitFlags |= 0x80000
			case "swimming":
				f.world.Self.MovementFlags = 0x200000
			case "another_mount":
				mountForTravel(t, s, f.world.Owner, 458)
			case "unknown_motion":
				s.state.MotionUnconfirmed = true
			}
			before := countPackets(f, opcode.MSGMoveStop)
			_, err := s.AcquireMovement(ctx, r)
			if change != "current" {
				if err == nil || countPackets(f, opcode.MSGMoveStop) != before {
					t.Fatal(change, err, s.stateLocked(), f.packets)
				}
				return
			}
			if err != nil || countPackets(f, opcode.MSGMoveStop) != 1 || s.state.Moving || !s.state.Following {
				t.Fatal(err, s.stateLocked(), f.packets)
			}
			if _, err = s.CastSpell(ctx, &actionsv1.CastSpellRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: r.VisitId, SpellId: r.SpellId, FollowMount: guard}); err != nil || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal(err, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestOwnerDismountDoesNotCancelNewOwnMount(t *testing.T) {
	for _, prior := range []bool{false, true} {
		s, f, _, ctx, _ := targetMovementFixture(t)
		toggle(t, s, true)
		mountForTravel(t, s, f.world.Owner, 23240)
		if prior {
			mountForTravel(t, s, f.world.Self, 23238)
		}
		previous := s.followMountObservationLocked(ctx, f.world)
		if !previous.GetOwnerMounted() {
			t.Fatal(previous)
		}
		f.world.Owner.UnitFlags &^= movementflags.Mounted
		s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdateAll, Payload: packedGUID(f.world.Owner.Guid)})
		edge := s.followMountObservationLocked(ctx, f.world)
		if !edge.OwnerDismounted {
			t.Fatal(edge)
		}
		// Remove and reapply the same rank between observations; the earlier
		// owner transition cannot cancel an independently selected new mount.
		s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdateAll, Payload: packedGUID(s.selfGUID)})
		mountForTravel(t, s, f.world.Self, 23238)
		guard := &actionsv1.FollowMountGuard{OwnerGuid: f.world.Owner.Guid, FollowRevision: s.followRevision, OwnerMountRevision: edge.OwnerMountRevision}
		_, err := s.DismountGround(ctx, &actionsv1.DismountGroundRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SpellId: 23238, FollowMount: guard})
		if err == nil || countPackets(f, opcode.CMSGCancelMountAura) != 0 {
			t.Fatal(prior, err, f.packets)
		}
	}
}

func TestFollowMountRevalidatesTheBodyAndActualOwner(t *testing.T) {
	for _, reason := range []string{"speed_only", "bare_flag", "foreign_aura", "owner_taxi", "self_swim", "owner_flight", "passenger_mount", "self_control", "intervening_stop", "cast_unknown", "stale_owner", "foreign_visit", "unlearned", "flight_spell"} {
		t.Run(reason, func(t *testing.T) {
			s, f, _, ctx, _ := targetMovementFixture(t)
			learnSpells(s, 23238, 32235)
			toggle(t, s, true)
			owner, self := f.world.Owner, f.world.Self
			mountForTravel(t, s, owner, 23240)
			v := s.followMountObservationLocked(ctx, f.world)
			r := &actionsv1.CastSpellRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: f.world.VisitRevision, SpellId: 23238, FollowMount: &actionsv1.FollowMountGuard{OwnerGuid: owner.Guid, FollowRevision: s.followRevision, OwnerMountRevision: v.OwnerMountRevision}}
			switch reason {
			case "speed_only", "bare_flag":
				s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdateAll, Payload: packedGUID(owner.Guid)})
				if reason == "speed_only" {
					owner.UnitFlags &^= movementflags.Mounted
				}
			case "foreign_aura":
				s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdateAll, Payload: buffAura(owner.Guid, self.Guid, 7, 23240, 0)})
			case "owner_taxi":
				owner.UnitFlags |= 0x100000
			case "self_swim":
				self.MovementFlags = 0x200000
			case "owner_flight":
				owner.MovementFlags = 0x2000000
			case "passenger_mount":
				mountForTravel(t, s, owner, 60424)
			case "self_control":
				s.clientControlLost = true
			case "intervening_stop":
				toggle(t, s, false)
			case "cast_unknown":
				s.state.CastPending = true
			case "stale_owner":
				owner.MovementFlags = 1
				owner.PositionAgeMs = 3000
			case "foreign_visit":
				r.VisitId++
			case "unlearned":
				r.SpellId = 458
			case "flight_spell":
				r.SpellId = 32235
			}
			if _, err := s.CastSpell(ctx, r); err == nil || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal(reason, err, f.packets)
			}
		})
	}
}
