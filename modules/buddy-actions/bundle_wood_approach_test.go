package buddyactions

import (
	"context"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const approachWoodGUID uint64 = 0xf110000000000000 | uint64(176793)<<24 | 43

func TestBundleWoodApproachRequiresTheMatchingOwnQuestAndObservedChest(t *testing.T) {
	for _, tc := range []struct {
		quest uint32
		entry uint32
		guid  uint64
		move  bool
	}{{3904, 161557, approachHarvestGUID, true}, {5545, 176793, approachWoodGUID, true}, {3904, 176793, approachWoodGUID, false}, {5545, 161557, approachHarvestGUID, false}, {5545, 176793, approachHarvestGUID, false}, {5545, 176794, 0xf110000000000000 | uint64(176794)<<24 | 43, false}} {
		s, f, _, r := harvestApproachRig(t)
		f.world.SelfQuestLog[0].QuestId = tc.quest
		f.world.QuestObjects[0].Guid, f.world.QuestObjects[0].Entry = tc.guid, tc.entry
		r.GameObjectGuid = tc.guid
		out, err := s.ApproachLoot(context.Background(), r)
		if err != nil || out.Moving != tc.move || countPackets(f, opcode.MSGMoveStartForward) != boolCount(tc.move) {
			t.Fatal(tc, out, err, f.packets)
		}
		if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGLoot) != 0 {
			t.Fatal("approach opened a chest", f.packets)
		}
	}
}

func boolCount(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestBundleWoodApproachRechecksRecipeAfterPlanAndImmediatelyBeforeMovement(t *testing.T) {
	for _, phase := range []string{"plan", "clock"} {
		for _, change := range []string{"complete", "other_quest", "other_entry", "absent", "combat", "replacement_lease"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				s, f, _, r := harvestApproachRig(t)
				f.world.SelfQuestLog[0].QuestId = 5545
				f.world.QuestObjects[0].Guid, f.world.QuestObjects[0].Entry = approachWoodGUID, 176793
				r.GameObjectGuid = approachWoodGUID
				mutate := func() {
					switch change {
					case "complete":
						f.world.SelfQuestLog[0].Complete = true
					case "other_quest":
						f.world.SelfQuestLog[0].QuestId = 3904
					case "other_entry":
						f.world.SelfQuestLog[1].QuestId = 3904
						f.world.QuestObjects[0].Entry = 161557
					case "absent":
						f.world.QuestObjects = nil
					case "combat":
						f.world.Self.UnitFlags |= 0x80000
					case "replacement_lease":
						s.interactionToken = "replacement-token"
					}
				}
				if phase == "plan" {
					s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
						mutate()
						return []position{a, b}, nil
					}
				} else {
					f.onClock = mutate
				}
				out, err := s.ApproachLoot(context.Background(), r)
				if err == nil && out.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
					t.Fatal("stale recipe/source granted movement", out, err, f.packets)
				}
			})
		}
	}
}
