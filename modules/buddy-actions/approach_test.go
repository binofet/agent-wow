package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const approachCorpseGUID = uint64(0xf130000033000001)

func approachRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachLootRequest) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.LootableCorpses = []*worldv1.CreatureState{{Guid: approachCorpseGUID, Present: true, MapKnown: true, PositionKnown: true, X: 12, HealthKnown: true, AliveKnown: true, DynamicFlags: 1}}
	toggle(t, s, true)
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot"})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance, &actionsv1.ApproachLootRequest{Token: lease.Token, CorpseGuid: approachCorpseGUID}
}
func TestLootApproachUsesBoundedNormalStepsAndStops(t *testing.T) {
	s, f, advance, r := approachRig(t)
	last := float32(0)
	for i := 0; i < 10; i++ {
		got, err := s.ApproachLoot(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if got.Position.X-last > 1.401 {
			t.Fatalf("unbounded step %+v", got)
		}
		last = got.Position.X
		if got.Reason == "loot_approach_within_distance" {
			break
		}
		advance(200 * time.Millisecond)
	}
	got := state(t, s)
	if got.Moving || !got.Following || got.Position.X < 8.99 || got.Position.X > 9.01 {
		t.Fatalf("approach %+v", got)
	}
	if f.packets[0].Opcode != opcode.MSGMoveStartForward || f.packets[len(f.packets)-1].Opcode != opcode.MSGMoveStop {
		t.Fatal("missing ordinary start/stop")
	}
}
func TestLootApproachRejectsUnsafeTargetsAndWrongLease(t *testing.T) {
	for _, mode := range []string{"token", "living", "notlootable", "far", "vertical", "combat", "afk", "ownerfar", "map", "unknown", "transport"} {
		s, f, _, r := approachRig(t)
		switch mode {
		case "token":
			r.Token = "wrong"
		case "living":
			f.world.LootableCorpses[0].Alive = true
		case "notlootable":
			f.world.LootableCorpses[0].DynamicFlags = 0
		case "far":
			f.world.LootableCorpses[0].X = 13
		case "vertical":
			f.world.LootableCorpses[0].Z = 2
		case "combat":
			f.world.Self.UnitFlags = 0x80000
		case "afk":
			f.world.Owner.PlayerFlags = 2
		case "ownerfar":
			f.world.Owner.X = 31
		case "map":
			f.world.Self.MapId = 1
			f.world.Owner.MapId = 1
			f.world.LootableCorpses[0].MapId = 1
		case "unknown":
			f.world.LootableCorpses[0].PositionKnown = false
		case "transport":
			f.world.LootableCorpses[0].Trajectory = &worldv1.CreatureTrajectory{Known: true, TransportRelative: true}
		}
		got, err := s.ApproachLoot(context.Background(), r)
		if mode == "token" {
			if err == nil {
				t.Fatal("wrong token")
			}
		} else if err != nil || got.Moving {
			t.Fatalf("%s %+v %v", mode, got, err)
		}
		if len(f.packets) != 0 {
			t.Fatalf("unsafe %s emitted packets", mode)
		}
	}
}
func TestLootApproachLateCallStopsWithoutLeapAndCorrectionRebases(t *testing.T) {
	for _, correction := range []bool{false, true} {
		s, f, advance, r := approachRig(t)
		_, _ = s.ApproachLoot(context.Background(), r)
		advance(time.Second)
		if correction {
			f.world.Self.PositionRevision++
			f.world.Self.X = 100
		}
		got, err := s.ApproachLoot(context.Background(), r)
		if err != nil || got.Moving || !got.Following || len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGMoveStop {
			t.Fatalf("late %+v %v", got, err)
		}
		_, n, _ := readGUID(f.packets[1].Payload)
		x := math.Float32frombits(binary.LittleEndian.Uint32(f.packets[1].Payload[n+10:]))
		if correction && x != 100 || !correction && x != 0 {
			t.Fatalf("stale STOP x%v", x)
		}
	}
}

func navLootRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachLootRequest) {
	t.Helper()
	s, f, advance, r := approachRig(t)
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -8933, -136, 81
	f.world.Self.PositionRevision++
	f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = -8927, -136, 81
	c := f.world.LootableCorpses[0]
	c.X, c.Y, c.Z, c.PositionRevision = -8927, -136, 81, 1
	s.SetNavigationDataDir("/fixture/client-data")
	return s, f, advance, r
}
func TestLootNavigationPreservesCornersAndMonotonicSpeed(t *testing.T) {
	s, f, advance, r := navLootRig(t)
	calls := 0
	s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
		calls++
		return []position{a, {x: -8933, y: -134, z: 81}, {x: -8927, y: -134, z: 81}, b}, nil
	}
	out, e := s.ApproachLoot(context.Background(), r)
	if e != nil || !out.Moving || !out.NavAvailable {
		t.Fatal(out, e)
	}
	advance(200 * time.Millisecond)
	out, e = s.ApproachLoot(context.Background(), r)
	if e != nil || out.Position.X != -8933 || math.Abs(float64(out.Position.Y+134.6)) > .002 {
		t.Fatal("cut first corner", out, e)
	}
	advance(200 * time.Millisecond)
	out, e = s.ApproachLoot(context.Background(), r)
	if e != nil || out.Position.X != -8933 || math.Abs(float64(out.Position.Y+134)) > .002 {
		t.Fatal("cut bend", out, e)
	}
	if calls != 3 || len(f.packets) != 3 {
		t.Fatal(calls, f.packets)
	}
	// Repeated same-time call cannot gain movement despite a freshly verified route.
	before := len(f.packets)
	_, _ = s.ApproachLoot(context.Background(), r)
	if len(f.packets) != before {
		t.Fatal("same-time call moved")
	}
}
func TestLootNavigationFailureNeverFallsBackToStraightMovement(t *testing.T) {
	for _, mode := range []string{"unknown_start", "excess_route", "changed_corpse", "changed_self", "expired_lease", "outside_region"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := navLootRig(t)
			s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
				switch mode {
				case "unknown_start":
					return nil, errors.New("start ground unavailable")
				case "excess_route":
					return []position{a, {x: a.x, y: a.y + 5, z: a.z}, b}, nil
				case "changed_corpse":
					f.world.LootableCorpses[0].PositionRevision++
				case "changed_self":
					f.world.Self.PositionRevision++
					f.world.Self.X += .4
				case "expired_lease":
					advance(16 * time.Second)
				}
				return []position{a, b}, nil
			}
			if mode == "outside_region" {
				f.world.Self.X = -9700
				f.world.Owner.X = -9695
				f.world.LootableCorpses[0].X = -9694
			}
			out, e := s.ApproachLoot(context.Background(), r)
			if e == nil && out.Moving {
				t.Fatal("unsafe route moved", out)
			}
			if len(f.packets) != 0 {
				t.Fatal("unsafe route used direct fallback", f.packets)
			}
		})
	}
	s, f, advance, r := navLootRig(t)
	s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) { return []position{a, b}, nil }
	_, _ = s.ApproachLoot(context.Background(), r)
	advance(200 * time.Millisecond)
	s.planPath = func(context.Context, string, position, position) ([]position, error) {
		return nil, errors.New("start ground unavailable")
	}
	out, e := s.ApproachLoot(context.Background(), r)
	if e != nil || out.Moving || len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGMoveStop {
		t.Fatal("failed route did not STOP", out, e, f.packets)
	}
}
func TestLootNavigationPlannerDeadlineIsBounded(t *testing.T) {
	s, f, _, r := navLootRig(t)
	s.planPath = func(ctx context.Context, _ string, a, b position) ([]position, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	out, e := s.ApproachLoot(context.Background(), r)
	if e != nil || out.Moving || out.NavAvailable || time.Since(start) > time.Second || len(f.packets) != 0 {
		t.Fatal(out, e, time.Since(start))
	}
}

func TestLootNavigationCancellationStopsOnlyItsOwnedMovement(t *testing.T) {
	s, f, _, r := navLootRig(t)
	s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) { return []position{a, b}, nil }
	_, _ = s.ApproachLoot(context.Background(), r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := s.ApproachLoot(ctx, r)
	if e == nil || s.state.Moving || len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGMoveStop {
		t.Fatal("canceled owned approach continued", s.stateLocked(), e, f.packets)
	}
	s, f, _, r = navLootRig(t)
	s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) { return []position{a, b}, nil }
	_, _ = s.ApproachLoot(context.Background(), r)
	s.interactionToken = "replacement-token"
	_, _ = s.ApproachLoot(ctx, r)
	if len(f.packets) != 1 {
		t.Fatal("old canceled token stopped replacement lease")
	}
}

const approachHarvestGUID uint64 = 0xf110000000000000 | uint64(161557)<<24 | 42

func harvestApproachRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachLootRequest) {
	t.Helper()
	s, f, advance, r := navLootRig(t)
	setWolfQuest(f, false)
	f.world.SelfQuestLog[0].QuestId = 3904
	f.world.Owner = nil // Own harvest quest authorizes this local interaction.
	f.world.LootableCorpses = nil
	f.world.QuestObjects = []*worldv1.GameObjectState{{Guid: approachHarvestGUID, Entry: 161557, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: -8927, Y: -136, Z: 81, TypeKnown: true, GameObjectType: 3, FlagsKnown: true}}
	r.CorpseGuid, r.GameObjectGuid = 0, approachHarvestGUID
	s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) { return []position{a, b}, nil }
	return s, f, advance, r
}

func TestHarvestApproachRequiresExactOneTargetAndOwnIncompleteQuest(t *testing.T) {
	for _, mode := range []string{"both", "neither", "wrong_token", "no_nav", "no_quest", "complete", "failed", "unknown_log", "partial_log", "duplicate_slot", "foreign_self", "absent", "wrong_entry", "wrong_guid", "wrong_type", "unknown_flags", "in_use", "not_selectable", "other_map", "unknown_position", "far", "vertical", "duplicate_object", "combat", "swimming", "unsafe_motion"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := harvestApproachRig(t)
			g := f.world.QuestObjects[0]
			switch mode {
			case "both":
				r.CorpseGuid = approachCorpseGUID
			case "neither":
				r.GameObjectGuid = 0
			case "wrong_token":
				r.Token = "other"
			case "no_nav":
				s.navDataDir = ""
			case "no_quest":
				f.world.SelfQuestLog[0].QuestId = 3903
			case "complete":
				f.world.SelfQuestLog[0].State = 1
			case "failed":
				f.world.SelfQuestLog[0].Failed = true
			case "unknown_log":
				f.world.SelfQuestLogKnown = false
			case "partial_log":
				f.world.SelfQuestLog = f.world.SelfQuestLog[:24]
			case "duplicate_slot":
				f.world.SelfQuestLog[1].Slot = 0
			case "foreign_self":
				f.world.Self.Guid = 4
			case "absent":
				f.world.QuestObjects = nil
			case "wrong_entry":
				g.Entry++
			case "wrong_guid":
				g.Guid = approachCorpseGUID
				r.GameObjectGuid = g.Guid
			case "wrong_type":
				g.GameObjectType = 19
			case "unknown_flags":
				g.FlagsKnown = false
			case "in_use":
				g.Flags = 1
			case "not_selectable":
				g.Flags = 16
			case "other_map":
				g.MapId = 1
			case "unknown_position":
				g.PositionKnown = false
			case "far":
				g.X = -8920
			case "vertical":
				g.Z = 83
			case "duplicate_object":
				f.world.QuestObjects = append(f.world.QuestObjects, g)
			case "combat":
				f.world.Self.UnitFlags |= 0x80000
			case "swimming":
				f.world.Self.MovementFlags = 0x200000
			case "unsafe_motion":
				s.state.MotionUnconfirmed = true
			}
			out, err := s.ApproachLoot(context.Background(), r)
			if err == nil && (out == nil || out.Moving) || len(f.packets) != 0 {
				t.Fatal("unsafe harvest approach", out, err, f.packets)
			}
		})
	}
}

func TestHarvestApproachUsesVerifiedCornersAndStopsBeforeOpeningDistance(t *testing.T) {
	s, f, advance, r := harvestApproachRig(t)
	s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
		if a.y < -134.005 {
			return []position{a, {x: -8933, y: -134, z: 81}, {x: -8927, y: -134, z: 81}, b}, nil
		}
		return []position{a, {x: -8927, y: -134, z: 81}, b}, nil
	}
	var out *actionsv1.State
	for i := 0; i < 12; i++ {
		var err error
		out, err = s.ApproachLoot(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && (out.Position.X != -8933 || math.Abs(float64(out.Position.Y+134.6)) > .002) {
			t.Fatal("cut crate corner", out)
		}
		if !out.Moving {
			break
		}
		advance(200 * time.Millisecond)
	}
	if out.Moving || out.Reason != "loot_approach_within_distance" || f.packets[len(f.packets)-1].Opcode != opcode.MSGMoveStop {
		t.Fatal(out, f.packets)
	}
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGCastSpell || p.Opcode == opcode.CMSGLoot {
			t.Fatal("movement actuator opened loot")
		}
	}
}

func TestHarvestApproachRechecksQuestObjectLeaseAndGroundAfterPlanning(t *testing.T) {
	for _, mode := range []string{"ground", "quest", "object", "removed", "lease"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := harvestApproachRig(t)
			s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
				switch mode {
				case "ground":
					return nil, errors.New("start poly unavailable")
				case "quest":
					f.world.SelfQuestLog[0].Complete = true
				case "object":
					f.world.QuestObjects[0].PositionRevision++
				case "removed":
					f.world.QuestObjects = nil
				case "lease":
					advance(16 * time.Second)
				}
				return []position{a, b}, nil
			}
			out, err := s.ApproachLoot(context.Background(), r)
			if err == nil && out.Moving || len(f.packets) != 0 {
				t.Fatal(out, err, f.packets)
			}
		})
	}
}
