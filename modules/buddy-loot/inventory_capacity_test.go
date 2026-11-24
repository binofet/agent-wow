package buddyloot

import (
	"context"
	"reflect"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func fillBackpack(w *worldv1.SnapshotResponse) {
	for i, item := range w.Backpack {
		item.ItemGuid = 0x4000000000000100 + uint64(i)
	}
	fixtureCarriedInventory(w)
}

func extraCapacity(w *worldv1.SnapshotResponse, entry uint32) {
	b := &worldv1.InventoryBag{Bag: 1, SlotKnown: true, ContainerGuid: 0x4000000000000999, MetadataKnown: true, Entry: entry, ContentsKnown: true, SlotCount: 20}
	for slot := uint32(0); slot < b.SlotCount; slot++ {
		b.Slots = append(b.Slots, &worldv1.BackpackSlot{Bag: 1, Slot: slot})
	}
	w.InventoryBags[1] = b
}

func tickCapacityLoot(t *testing.T, s *Service, namedLegacy bool) {
	t.Helper()
	if namedLegacy {
		tickGatherWindow(t, s)
		return
	}
	tickCorpseLua(t, s)
}

func TestCapacityUsesCompleteCarriedLayoutAndOnlyGeneralSlots(t *testing.T) {
	_, f, _ := rig(t)
	fillBackpack(f.world)
	if ownKnownFreeSlot(f.world, 1) {
		t.Fatal("full backpack invented capacity")
	}
	extraCapacity(f.world, 41599)
	if !ownKnownFreeSlot(f.world, 1) {
		t.Fatal("known ordinary bag free slot ignored")
	}
	f.world.InventoryBags[1].Entry = 2101
	if ownKnownFreeSlot(f.world, 1) {
		t.Fatal("specialty bag advertised general capacity")
	}
	f.world.Backpack[0].ItemGuid = 0
	if !ownKnownFreeSlot(f.world, 1) {
		t.Fatal("known backpack capacity ignored")
	}
}

func TestCapacityRejectsPartialDuplicateUnknownAndContradictoryFrames(t *testing.T) {
	for name, mutate := range map[string]func(*worldv1.SnapshotResponse){
		"unknown":                  func(w *worldv1.SnapshotResponse) { w.InventoryKnown = false },
		"revision":                 func(w *worldv1.SnapshotResponse) { w.InventoryRevision = 0 },
		"foreign":                  func(w *worldv1.SnapshotResponse) { w.Self.Guid = 2 },
		"partial_bags":             func(w *worldv1.SnapshotResponse) { w.InventoryBags = w.InventoryBags[:4] },
		"duplicate_bag":            func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Bag = 3 },
		"unknown_slot":             func(w *worldv1.SnapshotResponse) { w.InventoryBags[2].SlotKnown = false },
		"unknown_contents":         func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].ContentsKnown = false },
		"unknown_container":        func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].MetadataKnown = false },
		"empty_container_conflict": func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].ContainerGuid = 0 },
		"oversized":                func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].SlotCount = 37 },
		"wrong_41599_size": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[1].SlotCount = 2
			w.InventoryBags[1].Slots = w.InventoryBags[1].Slots[:2]
		},
		"partial_slots":       func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots = w.InventoryBags[1].Slots[:1] },
		"duplicate_slot":      func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[1].Slot = 0 },
		"wrong_bag":           func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[1].Bag = 0 },
		"empty_item_conflict": func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[0].Entry = 1 },
		"item_guid":           func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[0].ItemGuid = 5 },
		"item_alias":          func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[0].ItemGuid = w.Backpack[0].ItemGuid },
		"container_alias":     func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].ContainerGuid = w.Backpack[0].ItemGuid },
	} {
		t.Run(name, func(t *testing.T) {
			_, f, _ := rig(t)
			fillBackpack(f.world)
			extraCapacity(f.world, 41599)
			mutate(f.world)
			if ownKnownFreeSlot(f.world, 1) {
				t.Fatal("incoherent inventory granted capacity")
			}
		})
	}
}

func TestLootCapacityBlocksNewOpenAndRechecksAfterLease(t *testing.T) {
	for _, gather := range []bool{false, true} {
		for _, afterLease := range []bool{false, true} {
			s, f, advance := rig(t)
			if gather {
				s, f, advance = harvestRig(t)
			}
			if afterLease {
				f.afterAcquire = func() { f.world.InventoryKnown = false }
			} else {
				f.world.InventoryKnown = false
			}
			enable(t, s)
			tickCapacityLoot(t, s, gather)
			if len(f.packets) != 0 || f.leaseOwner != "" || (gather || !afterLease) && !current(t, s).InventoryPaused {
				t.Fatal(gather, afterLease, current(t, s), f.packets)
			}
			if !gather && afterLease && (s.corpseRequest == nil || !s.corpseRequest.receipt.Terminal || s.corpseRequest.receipt.OpeningTransportAttempted) {
				t.Fatal("lost exact inventory observation was not a known no-write refusal", current(t, s))
			}
			f.afterAcquire = nil
			f.world.InventoryKnown = true
			advance(time.Second)
			tickCapacityLoot(t, s, gather)
			if gather {
				// The first fresh view retires the native capacity pause; the
				// next normal Lua turn chooses the exact new source request.
				if current(t, s).InventoryPaused || len(f.packets) != 0 {
					t.Fatal("gather capacity bookkeeping emitted an opening", current(t, s), f.packets)
				}
				tickCapacityLoot(t, s, true)
			}
			if len(f.packets) != 1 || current(t, s).InventoryPaused {
				t.Fatal("actual free capacity did not resume", current(t, s), f.packets)
			}
		}
	}
}

func TestLootCapacityRechecksBeforeAutostoreAndPreservesPendingWrite(t *testing.T) {
	s, f, _ := rig(t)
	enable(t, s)
	tickCapacityLoot(t, s, false)
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 2, id: 123, count: 1}))
	fillBackpack(f.world)
	tickCapacityLoot(t, s, false)
	if !corpsePolicyPaused(t, s) || len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease {
		t.Fatal("autostore ignored full carried inventory", current(t, s), f.packets)
	}
	s, f, _ = rig(t)
	enable(t, s)
	tickCapacityLoot(t, s, false)
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 2, id: 123, count: 1}))
	tickCapacityLoot(t, s, false)
	f.world.InventoryKnown = false
	before := len(f.packets)
	tickCapacityLoot(t, s, false)
	if len(f.packets) != before || current(t, s).Phase != "waiting_item" || current(t, s).InventoryPaused {
		t.Fatal("capacity change reset an outstanding ambiguous item write", current(t, s), f.packets)
	}
}

func TestLootFinalCapacityReadRevalidatesSelectedSourceAndBody(t *testing.T) {
	for _, gather := range []bool{false, true} {
		for _, mode := range []string{"combat", "dead", "gone", "moved", "position_revision", "map", "capacity", "quest_removed", "quest_complete", "source_revision"} {
			if !gather && (mode == "quest_removed" || mode == "quest_complete" || mode == "source_revision") {
				continue
			}
			t.Run(map[bool]string{false: "corpse_", true: "gather_"}[gather]+mode, func(t *testing.T) {
				s, f, advance := rig(t)
				if gather {
					s, f, advance = harvestRig(t)
				}
				armed, mutatedAt := false, 0
				f.afterRenew = func() { armed = true }
				f.onSnapshot = func(n int, w *worldv1.SnapshotResponse) {
					// Mutate the actual final response after exact token renewal,
					// rather than depending on how many factual reads Lua required.
					if !armed || mutatedAt != 0 {
						return
					}
					armed, mutatedAt = false, n
					switch mode {
					case "combat":
						w.Self.UnitFlags |= 0x80000
					case "dead":
						w.Self.Alive = false
					case "gone":
						w.LootableCorpses, w.QuestObjects = nil, nil
					case "moved":
						if gather {
							w.QuestObjects[0].X = 20
						} else {
							w.LootableCorpses[0].X = 20
						}
					case "position_revision":
						w.Self.PositionRevision++
					case "map":
						w.Self.MapId++
					case "capacity":
						w.InventoryKnown = false
					case "quest_removed":
						w.SelfQuestLog[0].QuestId = 0
					case "quest_complete":
						w.SelfQuestLog[0].Complete = true
					case "source_revision":
						w.QuestObjects[0].PositionRevision++
					}
				}
				enable(t, s)
				tickCapacityLoot(t, s, gather)
				if mutatedAt == 0 {
					t.Fatal("fixture never reached final post-renewal observation", f.calls)
				}
				if !gather && mode == "combat" {
					// Combat is a Lua scheduling preference, not a native OPEN
					// packet precondition. The new fact arrived after selection.
					if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || !s.corpseRequest.receipt.OpeningTransportWritten {
						t.Fatal("exact OPEN imposed a second combat policy", current(t, s), f.packets)
					}
					return
				}
				if len(f.packets) != 0 || s.token != "" || f.releases != 1 {
					t.Fatal("final observation change reached write", current(t, s), f.snapshotReads, f.packets, f.releases)
				}
				if !gather {
					// The same exact request returns its stored refusal. A distinct
					// Lua request may re-evaluate fresh facts after a known no-write.
					if s.corpseRequest == nil || s.corpseRequest.receipt.OpeningTransportAttempted {
						t.Fatal("prewrite refusal lost exact request custody", s.corpseRequest)
					}
					request := s.corpseRequest.request
					acquired := len(f.calls)
					if _, err := s.OpenCorpse(context.Background(), request); err != nil || len(f.packets) != 0 || len(f.calls) != acquired {
						t.Fatal("repeated exact request re-entered admission", err, f.calls, f.packets)
					}
				}
				f.onSnapshot = nil
				f.actions.Moving, f.actions.Position.X, f.leaseOwner = false, 0, ""
				advance(time.Second)
				if gather && mode == "capacity" {
					return
				} // A newly complete free observation may resume a capacity pause.
				tickCapacityLoot(t, s, gather)
				if gather && len(f.packets) != 0 {
					t.Fatal("rejected source retried", f.packets)
				} else if !gather && (len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || !s.corpseRequest.receipt.OpeningTransportWritten) {
					t.Fatal("fresh Lua intent could not use newly valid exact facts", current(t, s), f.packets)
				}
			})
		}
	}
}

func TestLootInventoryFullRequiresNewerFreeInventoryRevision(t *testing.T) {
	for _, gather := range []bool{false, true} {
		t.Run(map[bool]string{false: "corpse", true: "gather"}[gather], func(t *testing.T) {
			s, f, advance := rig(t)
			guid := corpseGUID
			if gather {
				s, f, advance = harvestRig(t)
				guid = harvestGUID
			}
			enable(t, s)
			tickCapacityLoot(t, s, gather)
			window := windowPacket(guid, 0, lootItem{slot: 2, id: 123, count: 1})
			if gather {
				window = harvestWindow(guid, lootItem{slot: 2, id: harvestItemID, count: 1, slotType: 4})
			}
			emit(s, opcode.SMSGLootResponse, window)
			tickCapacityLoot(t, s, gather)
			writtenRevision := f.world.InventoryRevision
			failure := make([]byte, 18)
			failure[0] = 50
			emit(s, opcode.SMSGInventoryChangeFailure, failure)
			tickCapacityLoot(t, s, gather)
			emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
			tickCapacityLoot(t, s, gather)
			before := len(f.packets)
			for range 2 {
				advance(time.Second)
				tickCapacityLoot(t, s, gather)
			}
			if (gather && !current(t, s).InventoryPaused || !gather && !corpsePolicyPaused(t, s)) || f.world.InventoryRevision != writtenRevision || len(f.packets) != before {
				t.Fatal("same cached free view retried rejected write", current(t, s), f.packets)
			}
			f.world.InventoryRevision++
			f.world.InventoryKnown = false
			advance(time.Second)
			tickCapacityLoot(t, s, gather)
			if len(f.packets) != before {
				t.Fatal("new unknown view resumed")
			}
			f.world.InventoryKnown = true
			advance(time.Second)
			tickCapacityLoot(t, s, gather)
			if gather && (current(t, s).InventoryPaused || len(f.packets) != before) {
				t.Fatal("source-bound capacity resume was not metadata-only", current(t, s), f.packets)
			}
			if len(f.packets) == before && !current(t, s).InventoryPaused {
				// A native item-store barrier or exact Lua source-capacity
				// pause can settle on this turn. Lua consumes that positively
				// updated state only on a fresh normal read, without retrying.
				tickCapacityLoot(t, s, gather)
			}
			if (gather && current(t, s).InventoryPaused || !gather && corpsePolicyPaused(t, s)) || len(f.packets) != before+1 {
				t.Fatal("newer complete free view did not resume", current(t, s), f.packets)
			}
		})
	}
}

func TestLootFinalCapacityReadRetainsOwnWolfQuestAuthority(t *testing.T) {
	s, f, _ := wolfRig(t)
	f.onSnapshot = func(n int, w *worldv1.SnapshotResponse) {
		if n == 4 {
			w.SelfQuestLog[0].QuestId = 0
		}
	}
	enable(t, s)
	// This is the explicitly named native wolf workflow, whose own-quest
	// authority must still hold at its final world read. Generic Lua corpse
	// opening intentionally does not borrow that named controller's authority.
	s.Tick(context.Background())
	if f.snapshotReads != 4 || len(f.packets) != 0 || s.token != "" || f.releases != 1 {
		t.Fatal("final quest loss reached corpse loot write", current(t, s), f.snapshotReads, f.packets)
	}
}

func TestLootOpeningRenewsExactTokenBeforeFinalWorldRead(t *testing.T) {
	for _, gather := range []bool{false, true} {
		for _, mode := range []string{"success", "same_owner_replacement", "changed_return_token", "moving_return", "position_return", "unsafe_status"} {
			t.Run(map[bool]string{false: "corpse_", true: "gather_"}[gather]+mode, func(t *testing.T) {
				s, f, _ := rig(t)
				if gather {
					s, f, _ = harvestRig(t)
				}
				f.beforeStatus = func() {
					if s.token == "" {
						return
					}
					if mode == "same_owner_replacement" {
						f.leaseToken = "replacement-token"
					}
					if mode == "unsafe_status" {
						f.actions.MotionUnconfirmed = true
					}
				}
				f.afterRenew = func() {
					switch mode {
					case "changed_return_token":
						f.leaseToken = "replacement-token"
					case "moving_return":
						f.actions.Moving = true
					case "position_return":
						f.actions.Position.X = 20
					}
				}
				enable(t, s)
				tickCapacityLoot(t, s, gather)
				if mode == "success" {
					want := []string{"buddy-world.snapshot", "buddy-actions.status", "buddy-actions.acquire_interaction", "buddy-world.snapshot", "packet"}
					write := -1
					for i, call := range f.calls {
						if call == "packet" {
							write = i
							break
						}
					}
					if len(f.packets) != 1 || write+1 < len(want) || !reflect.DeepEqual(f.calls[write+1-len(want):write+1], want) {
						t.Fatal("final observation was superseded before opening write", f.calls, f.packets)
					}
				} else if len(f.packets) != 0 || s.token != "" {
					t.Fatal("lost exact lease or unsafe renewed position wrote", current(t, s), f.calls, f.packets)
				}
				if (mode == "same_owner_replacement" || mode == "changed_return_token") && (f.leaseOwner != "loot" || f.leaseToken != "replacement-token" || f.releases != 0) {
					t.Fatal("cleanup cleared replacement same-owner lease", f.leaseOwner, f.leaseToken, f.releases)
				}
			})
		}
	}
}
