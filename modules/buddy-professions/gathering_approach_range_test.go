package buddyprofessions

import (
	"context"
	"fmt"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestGatheringApproachCanYieldOnlyKnownFollowingOrItsGuardedRoute(t *testing.T) {
	for _, herb := range []bool{false, true} {
		for _, mode := range []string{"following", "guarded_route", "moving_without_follow", "expired_follow", "travel", "other_lease", "unguarded_route", "expired_route", "cast", "attack", "motion_unknown", "recovery"} {
			t.Run(fmt.Sprintf("herb=%v/%s", herb, mode), func(t *testing.T) {
				fixture, spell, target := skinningFixture, skinSpell, skinProtocolTarget
				if herb {
					fixture, spell, target = herbBehaviorFixture, herbSpell, herbBehaviorTarget
				}
				s, f := fixture(t)
				f.mu.Lock()
				a := f.actions
				a.Following, a.Moving, a.FollowLeaseRemainingMs = true, true, 60000
				switch mode {
				case "guarded_route", "unguarded_route", "expired_route":
					a.InteractionOwner, a.InteractionHarvest, a.Traveling = "exploration", true, true
					a.HarvestGuardGuids, a.InteractionLeaseRemainingMs = []uint64{s.selfGUID, 3}, 15000
					if mode == "unguarded_route" {
						a.HarvestGuardGuids = []uint64{s.selfGUID}
					} else if mode == "expired_route" {
						a.InteractionLeaseRemainingMs = 0
					}
				case "moving_without_follow":
					a.Following = false
				case "expired_follow":
					a.FollowLeaseRemainingMs = 0
				case "travel":
					a.Traveling = true
				case "other_lease":
					a.InteractionOwner = "trade"
				case "cast":
					a.CastPending = true
				case "attack":
					a.AutoAttackRequested = true
				case "motion_unknown":
					a.MotionUnconfirmed = true
				case "recovery":
					a.Recovery = &actionsv1.RecoveryState{Active: true}
				}
				f.mu.Unlock()
				st, err := s.GetState(context.Background(), &emptypb.Empty{})
				if err != nil {
					t.Fatal(err)
				}
				ready, approachable := false, false
				if herb {
					ready, approachable = st.HerbTargets[0].Ready, st.HerbTargets[0].Approachable
				} else {
					ready, approachable = st.SkinningTargets[0].Ready, st.SkinningTargets[0].Approachable
				}
				if ready || approachable != (mode == "following" || mode == "guarded_route") {
					t.Fatal("advisory route qualification changed cast/unknown-movement authority", st)
				}
				r := professionRequest(s, "moving-approach-is-never-cast-authority", spell)
				r.TargetGuid, r.TargetPositionRevision = target, 5
				if _, err := s.Attempt(context.Background(), r); status.Code(err) != codes.FailedPrecondition || s.active != nil || len(s.requests) != 0 || len(f.packets) != 0 {
					t.Fatal("moving observation reserved an item/cast", err, f.packets)
				}
			})
		}
	}
}

func TestGatheringApproachFortyYardsDoesNotAuthorizeDistantCast(t *testing.T) {
	for _, herb := range []bool{false, true} {
		for _, distance := range []float32{3, 3.25, 20.25, 39.75, 40, 40.25, 80} {
			t.Run(fmt.Sprintf("herb=%v/distance=%g", herb, distance), func(t *testing.T) {
				fixture, spell, target := skinningFixture, skinSpell, skinProtocolTarget
				if herb {
					fixture, spell, target = herbBehaviorFixture, herbSpell, herbBehaviorTarget
				}
				s, f := fixture(t)
				f.mu.Lock()
				if herb {
					f.world.RequestedGameObjects[0].X = f.actions.Position.X + distance
				} else {
					f.world.SkinnableCorpses[0].X = f.actions.Position.X + distance
				}
				f.mu.Unlock()
				st, err := s.GetState(context.Background(), &emptypb.Empty{})
				if err != nil {
					t.Fatal(err)
				}
				ready, approachable := false, false
				if herb {
					if len(st.HerbTargets) != 1 {
						t.Fatal(st)
					}
					ready, approachable = st.HerbTargets[0].Ready, st.HerbTargets[0].Approachable
				} else {
					if len(st.SkinningTargets) != 1 {
						t.Fatal(st)
					}
					ready, approachable = st.SkinningTargets[0].Ready, st.SkinningTargets[0].Approachable
				}
				if ready != (distance <= 3) || approachable != (distance <= 40) || len(f.packets) != 0 {
					t.Fatal("approach eligibility changed cast range or wrote", st, f.packets)
				}
				if distance > 3 {
					r := professionRequest(s, "approachable-is-not-cast-authority", spell)
					r.TargetGuid, r.TargetPositionRevision = target, 5
					if _, err := s.Attempt(context.Background(), r); status.Code(err) != codes.FailedPrecondition || s.active != nil || len(s.requests) != 0 || len(f.packets) != 0 {
						t.Fatal("distant approach qualifier authorized a cast", err, f.packets)
					}
				}
			})
		}
	}
}

func TestGatheringApproachFortyRetainsActualSameFloorTargetGates(t *testing.T) {
	for _, herb := range []bool{false, true} {
		for _, mode := range []string{"different_floor", "map", "missing", "unknown_revision", "unknown_flags"} {
			t.Run(fmt.Sprintf("herb=%v/%s", herb, mode), func(t *testing.T) {
				fixture := skinningFixture
				if herb {
					fixture = herbBehaviorFixture
				}
				s, f := fixture(t)
				f.mu.Lock()
				if herb {
					g := f.world.RequestedGameObjects[0]
					g.X = f.actions.Position.X + 30
					switch mode {
					case "different_floor":
						g.Z += 1.51
					case "map":
						g.MapId++
					case "missing":
						g.Present = false
					case "unknown_revision":
						g.PositionRevision = 0
					case "unknown_flags":
						g.FlagsKnown = false
					}
				} else {
					c := f.world.SkinnableCorpses[0]
					c.X = f.actions.Position.X + 30
					switch mode {
					case "different_floor":
						c.Z += 1.51
					case "map":
						c.MapId++
					case "missing":
						c.Present = false
					case "unknown_revision":
						c.PositionRevision = 0
					case "unknown_flags":
						c.UnitFlagsKnown = false
					}
				}
				f.mu.Unlock()
				st, err := s.GetState(context.Background(), &emptypb.Empty{})
				if err != nil || herb && st.HerbTargets[0].Approachable || !herb && st.SkinningTargets[0].Approachable || len(f.packets) != 0 {
					t.Fatal("increased range bypassed actual target gates", st, err)
				}
			})
		}
	}
}
