package buddyloot

import (
	"context"
	"strconv"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/types/known/emptypb"
)

func observedKromOpportunity(t *testing.T) (*behavior.Session, *lootv1.CorpseObservation) {
	t.Helper()
	s, f, _ := harvestRig(t)
	enable(t, s)
	f.world.SelfQuestLog[0].QuestId = 1360
	f.world.QuestObjects[0].Entry = 124389
	f.world.QuestObjects[0].Guid = uint64(0xf110)<<48 | uint64(124389)<<24 | 17
	f.world.QuestObjects[0].X, f.world.QuestObjects[0].Z = 45, 6
	f.world.Self.SpeedsKnown, f.world.Self.RunSpeed = true, 7
	f.world.Owner = &worldv1.PlayerState{Guid: 3, Present: true, PositionKnown: true, MapKnown: true, AliveKnown: true, Alive: true, UnitFlagsKnown: true, MovementFlagsKnown: true}
	o, err := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if err != nil || !o.ItemCountsKnown {
		t.Fatal("missing known carried-item counts", err)
	}
	o.Self.PositionKnown, o.Self.PositionRevision = true, 2
	return corpsePolicyVM(t, o.SessionId), o
}

func TestQuestObjectTripLuaReturnsToNormalOpeningAfterFreshSettledArrival(t *testing.T) {
	vm, o := observedKromOpportunity(t)
	g := o.QuestObjects[0]
	guid := strconv.FormatUint(g.Guid, 10)
	for step := 0; step <= 30; step++ {
		o.Actions.Position.X = float32(step) * 1.5
		o.Actions.Position.Z = float32(step) / 5
		o.Actions.Position.Revision++
		if step > 0 {
			o.Actions.Moving = true
			o.Actions.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: g.Guid, VisitId: o.VisitId, Phase: "approaching"}
		}
		if step == 30 {
			o.Actions.Moving = false
			o.Actions.TargetMovement.Phase = "in_position"
			o.Actions.TargetMovement.Ready = true
			o.Actions.TargetMovement.QuestArrivalWorldRevision = o.Self.PositionRevision
		}
		d := corpsePolicyDecision(t, vm, o, true, false, 1000+int64(step)*1000)
		a, _ := d["action"].(map[string]any)
		if step < 30 {
			if a["capability"] != "approach_target" || a["targetGuid"] != guid || a["requireLos"] != true {
				t.Fatalf("step %d: %v", step, d)
			}
		} else if a["capability"] != "loot_gather" || a["sourceGuid"] != guid || a["approach"] != false || a["itemId"] != float64(8027) || a["questId"] != float64(1360) {
			t.Fatal(d)
		}
	}
}

func TestQuestObjectTripLuaYieldsWithoutOpeningOrRepeatingUncertainMovement(t *testing.T) {
	for _, mode := range []string{"owner moves", "owner starts distant", "combat", "stall", "already carried", "changed object", "unconfirmed motion", "other controller"} {
		t.Run(mode, func(t *testing.T) {
			vm, o := observedKromOpportunity(t)
			if mode == "owner starts distant" {
				o.Owner.X = 60
			}
			if mode == "already carried" {
				o.ItemCounts = []*lootv1.OwnedItemCount{{ItemId: 8027, Count: 1}}
			}
			first := corpsePolicyDecision(t, vm, o, true, false, 1000)
			a, _ := first["action"].(map[string]any)
			if mode == "owner starts distant" || mode == "already carried" {
				if a != nil {
					t.Fatal(first)
				}
				return
			}
			if a["capability"] != "approach_target" {
				t.Fatal(first)
			}
			o.Actions.Moving = true
			o.Actions.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: o.QuestObjects[0].Guid, VisitId: o.VisitId, Phase: "approaching"}
			now := int64(1200)
			quiet, urgent := true, false
			switch mode {
			case "owner moves":
				o.Owner.MovementFlags = 1
			case "combat":
				quiet, urgent = false, true
			case "stall":
				now = 7000
			case "changed object":
				o.QuestObjects[0].PositionRevision++
			case "unconfirmed motion":
				o.Actions.MotionUnconfirmed = true
			case "other controller":
				o.Actions.TargetMovement.TargetGuid++
				o.Owner.MovementFlags = 1
			}
			d := corpsePolicyDecision(t, vm, o, quiet, urgent, now)
			a, _ = d["action"].(map[string]any)
			if mode == "unconfirmed motion" || mode == "other controller" {
				if a != nil {
					t.Fatal(d)
				}
			} else if a["capability"] != "stop_movement" {
				t.Fatal(d)
			}
			o.Actions.Moving = false
			if next := corpsePolicyDecision(t, vm, o, true, false, now+200); next["action"] != nil {
				t.Fatal("aborted detour replayed", next)
			}
		})
	}
}
