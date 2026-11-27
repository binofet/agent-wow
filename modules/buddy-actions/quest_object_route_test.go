package buddyactions

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func questObjectRig(t *testing.T, from, to position) (*Service, *fakeSession, *actionsv1.ApproachTargetRequest, context.Context, func(time.Duration)) {
	t.Helper()
	s, f, r, ctx, advance := targetMovementFixture(t)
	setWolfQuest(f, false)
	f.world.SelfQuestLog[0].QuestId = 1360
	f.world.SelfQuestLogRevision, f.world.InventoryRevision, f.world.InventoryKnown = 4, 9, true
	for bag := uint32(0); bag < 5; bag++ {
		b := &worldv1.InventoryBag{Bag: bag, SlotKnown: true, ContentsKnown: true}
		if bag == 0 {
			b.SlotCount = 16
			for slot := uint32(0); slot < b.SlotCount; slot++ {
				b.Slots = append(b.Slots, &worldv1.BackpackSlot{Slot: slot})
			}
		}
		f.world.InventoryBags = append(f.world.InventoryBags, b)
	}
	p := f.world.Self
	p.X, p.Y, p.Z = from.x, from.y, from.z
	o := f.world.Owner
	o.X, o.Y, o.Z, o.Health = from.x+1, from.y, from.z, o.MaxHealth
	guid := uint64(0xf110)<<48 | uint64(124389)<<24 | 17
	f.world.QuestObjects = []*worldv1.GameObjectState{{Guid: guid, Entry: 124389, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: to.x, Y: to.y, Z: to.z, TypeKnown: true, GameObjectType: 3, FlagsKnown: true}}
	r.TargetGuid, r.RangeYards, r.RequireLos = guid, 3, true
	r.QuestId, r.QuestItemId, r.QuestObjectRevision, r.QuestLogRevision, r.QuestInventoryRevision, r.QuestFollowRevision, r.QuestOwnerGuid = 1360, 8027, 1, 4, 9, s.followRevision, 3
	s.openTravel = func(_ context.Context, _ string, _ uint32, _, goal buddynav.Point) (travelSurface, error) {
		return &straightTripSurface{goal: goal}, nil
	}
	s.crowdControlSight = func(context.Context, string, uint32, position, position) (bool, error) { return true, nil }
	return s, f, r, ctx, advance
}

func TestQuestContainerGroundTripProgressesPastTwentySecondsAndReplansOwnCorrection(t *testing.T) {
	s, f, r, ctx, advance := questObjectRig(t, position{}, position{x: 190})
	for step := 0; step < 180; step++ {
		if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		if step == 45 {
			p := f.world.Self
			p.PositionRevision++
			p.X, p.Y, p.Z = s.pos.x+.1, s.pos.y, s.pos.z
			p.MovementFlags = 1
		}
		state, err := s.ApproachTarget(ctx, r)
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		m := state.TargetMovement
		if m.Ready {
			if state.Moving || step < 100 || distance(s.pos, position{x: 190}) > 3 || countPackets(f, opcode.MSGMoveStop) < 1 {
				t.Fatalf("step=%d pose=%+v moving=%v", step, s.pos, state.Moving)
			}
			t.Logf("arrived after %.1f seconds across two prefixes and an authoritative revision", float64(step)*.2)
			return
		}
		if m.Phase != "approaching" && m.Phase != "quest_object_replanning" {
			t.Fatalf("step=%d phase=%s reason=%s position=%+v", step, m.Phase, state.Reason, s.pos)
		}
		advance(200 * time.Millisecond)
	}
	t.Fatal("did not reach")
}

func TestQuestContainerCopiedKromRoute(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR to copied client map assets")
	}
	start, end := position{x: -6195.6396, y: -2981.166, z: 225.02223}, position{x: -6279.4, y: -2963.6, z: 223.618}
	s, f, r, ctx, advance := questObjectRig(t, start, end)
	s.navDataDir, s.openTravel, s.planMapPath, s.crowdControlSight = dir, openGroundTravel, planMapNavigation, nil
	for step := 0; step < 155; step++ {
		if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		state, err := s.ApproachTarget(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if state.TargetMovement.Ready {
			if state.Moving || distance(s.pos, end) > 3 || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("incorrect terminal state", state)
			}
			t.Logf("quest object %.2f yd away reached in %d ordinary movement frames; stopped %.2f yd from object", distance(start, end), len(f.packets), distance(s.pos, end))
			return
		}
		if state.TargetMovement.Phase != "approaching" {
			t.Fatalf("step %d phase=%s detail=%s pos=%+v", step, state.TargetMovement.Phase, state.Reason, s.pos)
		}
		advance(200 * time.Millisecond)
	}
	t.Fatal("did not reach chest")
}

func TestQuestContainerPrewriteAndRouteRefusals(t *testing.T) {
	for _, mode := range []string{"solid wall", "unreachable floor", "owner moving", "owner falling", "quest completed", "object replaced", "party in combat", "position changed", "uncertain stop"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r, ctx, advance := questObjectRig(t, position{}, position{x: 40, z: 2})
			switch mode {
			case "solid wall":
				s.crowdControlSight = func(context.Context, string, uint32, position, position) (bool, error) { return false, nil }
			case "unreachable floor":
				s.openTravel = func(context.Context, string, uint32, buddynav.Point, buddynav.Point) (travelSurface, error) {
					return nil, errors.New("disconnected floor")
				}
			default:
				st, err := s.ApproachTarget(ctx, r)
				if err != nil || !st.Moving {
					t.Fatal(st, err)
				}
				advance(200 * time.Millisecond)
				f.onClock = func() {
					f.onClock = nil
					switch mode {
					case "owner moving":
						f.world.Owner.MovementFlags = 1
					case "owner falling":
						f.world.Owner.MovementFlags = 0x1000
					case "quest completed":
						f.world.SelfQuestLog[0].Complete = true
					case "object replaced":
						f.world.QuestObjects[0].PositionRevision++
					case "party in combat":
						f.world.Owner.UnitFlags |= 0x80000
					case "position changed":
						f.world.Self.PositionRevision++
					case "uncertain stop":
						f.world.Owner.MovementFlags = 1
						f.failSend = true
						f.failSendOpcode = opcode.MSGMoveStop
					}
				}
			}
			st, err := s.ApproachTarget(ctx, r)
			if err != nil || st.TargetMovement.Ready || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal(st, err, f.packets)
			}
			if mode == "uncertain stop" {
				was := len(f.packets)
				_, _ = s.ApproachTarget(ctx, r)
				if !st.MotionUnconfirmed || len(f.packets) != was {
					t.Fatalf("lost uncertain stop, %v", st)
				}
			}
		})
	}
}

func TestQuestContainerInitialBindingAndCompetingMotion(t *testing.T) {
	for _, mode := range []string{"owner falling", "wrong item", "stale source", "stale inventory", "already carried", "missing bag", "replacement follow", "different movement writer"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r, ctx, _ := questObjectRig(t, position{}, position{x: 40})
			switch mode {
			case "owner falling":
				f.world.Owner.MovementFlags = 0x1000
			case "wrong item":
				r.QuestItemId++
			case "stale source":
				f.world.QuestObjects[0].PositionRevision++
			case "stale inventory":
				r.QuestInventoryRevision++
			case "already carried":
				slot := f.world.InventoryBags[0].Slots[0]
				slot.ItemGuid, slot.Entry, slot.StackCount, slot.MetadataKnown = uint64(0x4000)<<48|123, 8027, 1, true
			case "missing bag":
				f.world.InventoryBags[4].ContentsKnown = false
			case "replacement follow":
				r.QuestFollowRevision++
			case "different movement writer":
				st, err := s.ApproachTarget(ctx, r)
				if err != nil || !st.Moving {
					t.Fatal(st, err)
				}
				if !s.movementLocked(ctx, opcode.MSGMoveHeartbeat, s.pos, true) {
					t.Fatal("competing movement unavailable")
				}
			}
			before := len(f.packets)
			st, err := s.ApproachTarget(ctx, r)
			if err != nil || len(f.packets) != before || st.TargetMovement.Ready {
				t.Fatal(mode, st, err, f.packets)
			}
			r.Stop = true
			_, _ = s.ApproachTarget(ctx, r)
			if len(f.packets) != before {
				t.Fatal("obsolete stop reached a different writer", mode)
			}
		})
	}
}

func TestQuestContainerStatusInvalidatesArrivalAfterOwnWorldCorrection(t *testing.T) {
	s, f, r, ctx, advance := questObjectRig(t, position{}, position{x: 7})
	for step := 0; step < 20; step++ {
		if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		st, err := s.ApproachTarget(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if st.TargetMovement.Ready {
			if st.TargetMovement.QuestArrivalWorldRevision != f.world.Self.PositionRevision || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal(st)
			}
			f.world.Self.X, f.world.Self.Y = 7, 2
			f.world.Self.PositionRevision++
			s.crowdControlSight = func(context.Context, string, uint32, position, position) (bool, error) { return false, nil }
			before := len(f.packets)
			fresh, err := s.Status(ctx, nil)
			if err != nil || fresh.TargetMovement.Ready || fresh.TargetMovement.Phase != "quest_object_replanning" || fresh.TargetMovement.QuestArrivalWorldRevision != 0 || len(f.packets) != before {
				t.Fatal(fresh, err)
			}
			return
		}
		advance(200 * time.Millisecond)
	}
	t.Fatal("no ground arrival")
}
