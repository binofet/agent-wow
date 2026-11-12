package buddyloot

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const woodGUID uint64 = 0xf110000000000000 | uint64(176793)<<24 | 43

func woodRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := harvestRig(t)
	f.world.SelfQuestLog[0].QuestId = 5545
	f.world.QuestObjects[0].Guid, f.world.QuestObjects[0].Entry = woodGUID, 176793
	return s, f, advance
}

func TestBundleWoodUsesBoundOpeningOwnBagItemSlotAndReleaseProof(t *testing.T) {
	s, f, advance := woodRig(t)
	// A full backpack with one actual general bag still permits a pickup.
	fillBackpack(f.world)
	extraCapacity(f.world, 41599)
	enable(t, s)
	s.Tick(context.Background())
	out := current(t, s)
	if out.LastGather == nil || out.LastGather.QuestId != 5545 || out.LastGather.ObjectEntry != 176793 || out.LastGather.ObjectGuid != woodGUID || out.LastGather.ItemId != 13872 || !out.LastGather.OpeningTransportWritten || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(f.packets[0].Payload[1:]) != 6478 {
		t.Fatal("wood recipe was not bound before opening", out, f.packets)
	}
	// A late Milly source cannot create this operation's loot window.
	emit(s, opcode.SMSGLootResponse, harvestWindow(harvestGUID, lootItem{slot: 2, id: 11119, count: 1}))
	if current(t, s).LastGather.LootWindowObserved {
		t.Fatal("foreign source window accepted")
	}
	emit(s, opcode.SMSGLootResponse, harvestWindow(woodGUID, lootItem{slot: 1, id: 11119, count: 1}, lootItem{slot: 2, id: 13872, count: 1, slotType: 4}))
	s.Tick(context.Background())
	if f.packets[len(f.packets)-1].Opcode != opcode.CMSGAutostoreLootItem || f.packets[len(f.packets)-1].Payload[0] != 2 {
		t.Fatal("stored another recipe's item", f.packets)
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(4, 13872, 1))
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 11119, 1))
	emit(s, opcode.SMSGLootRemoved, []byte{1})
	if current(t, s).LastGather.ItemPushObserved || current(t, s).LastGather.SlotRemoved {
		t.Fatal("wrong owner, item or slot became proof", current(t, s))
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 13872, 1))
	f.world.InventoryBags[1].Slots[0] = &worldv1.BackpackSlot{Bag: 1, Slot: 0, ItemGuid: 0x4000000000000888, Entry: 13872, StackCount: 1, MetadataKnown: true}
	f.world.InventoryRevision++
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	s.Tick(context.Background())
	if current(t, s).LastGather.Status != lootv1.GatherStatus_GATHER_ITEM_STORED || current(t, s).LastGather.OwnQuestCompleteObserved {
		t.Fatal("one bundle became eight or skipped receipt", current(t, s))
	}
	f.world.SelfQuestLog[1].QuestId, f.world.SelfQuestLog[1].Complete = 3904, true
	f.world.SelfQuestLogRevision++
	s.Tick(context.Background())
	if current(t, s).LastGather.OwnQuestCompleteObserved {
		t.Fatal("Milly completion satisfied wood quest")
	}
	f.world.SelfQuestLog[0].Complete = true
	f.world.SelfQuestLogRevision++
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(harvestGUID), 1))
	if current(t, s).LastGather.ReleaseObserved {
		t.Fatal("foreign source release accepted")
	}
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(woodGUID), 1))
	s.Tick(context.Background())
	out = current(t, s)
	if out.LastGather.Status != lootv1.GatherStatus_GATHER_CONFIRMED || !out.LastGather.ItemPushObserved || !out.LastGather.SlotRemoved || !out.LastGather.ReleaseObserved || !out.LastGather.OwnQuestCompleteObserved || out.LastGather.Count != 1 || out.PendingGameObjectGuid != 0 || out.LastCorpseGuid != 0 || f.leaseOwner != "" || f.world.InventoryBags[1].Slots[0].ItemGuid != 0x4000000000000888 {
		t.Fatal("missing bound receipt or carried item", out)
	}
	before := len(f.packets)
	f.world.SelfQuestLog[0].Complete = false
	advance(time.Second)
	s.Tick(context.Background())
	if len(f.packets) != before {
		t.Fatal("same wood source replayed")
	}
}

func TestBundleWoodCannotBorrowMillyQuestSourceOrInventoryCapacity(t *testing.T) {
	for _, mode := range []string{"milly_quest", "milly_object", "guid_entry", "unknown_entry", "complete", "failed", "bad_bag_guid", "bag_item_alias", "unknown_contents", "complete_after_stop", "other_recipe_after_stop", "complete_before_open"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := woodRig(t)
			switch mode {
			case "milly_quest":
				f.world.SelfQuestLog[0].QuestId = 3904
			case "milly_object":
				f.world.QuestObjects[0].Guid, f.world.QuestObjects[0].Entry = harvestGUID, 161557
			case "guid_entry":
				f.world.QuestObjects[0].Guid = harvestGUID
			case "unknown_entry":
				f.world.QuestObjects[0].Guid = 0xf110000000000000 | uint64(176794)<<24 | 43
				f.world.QuestObjects[0].Entry = 176794
			case "complete":
				f.world.SelfQuestLog[0].Complete = true
			case "failed":
				f.world.SelfQuestLog[0].State = 2
			case "bad_bag_guid", "bag_item_alias", "unknown_contents":
				fillBackpack(f.world)
				extraCapacity(f.world, 41599)
				switch mode {
				case "bad_bag_guid":
					f.world.InventoryBags[1].ContainerGuid = 5
				case "bag_item_alias":
					f.world.InventoryBags[1].ContainerGuid = f.world.Backpack[0].ItemGuid
				case "unknown_contents":
					f.world.InventoryBags[1].ContentsKnown = false
				}
			case "complete_after_stop", "other_recipe_after_stop":
				f.afterAcquire = func() {
					if mode == "complete_after_stop" {
						f.world.SelfQuestLog[0].Complete = true
					} else {
						f.world.SelfQuestLog[0].QuestId = 3904
					}
				}
			case "complete_before_open":
				f.onSnapshot = func(n int, w *worldv1.SnapshotResponse) {
					if n >= 4 {
						w.SelfQuestLog[0].Complete = true
					}
				}
			}
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal("unbound wood operation wrote", current(t, s), f.packets)
			}
		})
	}
}

func TestBundleWoodRejectsWrongItemWithoutAnyStoreOrSuccess(t *testing.T) {
	s, f, _ := woodRig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, harvestWindow(woodGUID, lootItem{slot: 2, id: 11119, count: 1}))
	s.Tick(context.Background())
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGAutostoreLootItem {
			t.Fatal("Milly item stored for wood quest")
		}
	}
	if current(t, s).LastGather.ItemPushObserved || current(t, s).LastGather.Status == lootv1.GatherStatus_GATHER_CONFIRMED {
		t.Fatal("wrong item became success", current(t, s))
	}
}

func TestBundleWoodNearestRecipeRemainsBoundWhenBothQuestsAreActive(t *testing.T) {
	s, f, _ := woodRig(t)
	f.world.SelfQuestLog[1].QuestId = 3904
	milly := proto.Clone(f.world.QuestObjects[0]).(*worldv1.GameObjectState)
	milly.Guid, milly.Entry, milly.X = harvestGUID, 161557, 3
	f.world.QuestObjects = append(f.world.QuestObjects, milly)
	enable(t, s)
	s.Tick(context.Background())
	f.world.SelfQuestLog[0].QuestId = 0 // The other active quest must not replace it.
	emit(s, opcode.SMSGLootResponse, harvestWindow(woodGUID, lootItem{slot: 2, id: 13872, count: 1}))
	s.Tick(context.Background())
	if current(t, s).LastGather.QuestId != 5545 || current(t, s).LastGather.ItemPushObserved || f.packets[len(f.packets)-1].Opcode != opcode.CMSGLootRelease {
		t.Fatal("recipe switched during an operation", current(t, s), f.packets)
	}
}
