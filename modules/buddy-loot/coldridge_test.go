package buddyloot

import (
	"context"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const coldWolfGUID uint64 = 0xf1300002c1000012

func coldLootRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := wolfRig(t)
	p := f.world.Self
	p.ClassKnown, p.ClassId, p.LevelKnown, p.Level = true, 9, true, 1
	p.X, p.Y, p.Z = -6240, 331, 383
	f.actions.Position.X, f.actions.Position.Y, f.actions.Position.Z = p.X, p.Y, p.Z
	f.world.SelfQuestLog[0].QuestId = 179
	c := f.world.LootableCorpses[0]
	c.Guid, c.Entry, c.X, c.Y, c.Z = coldWolfGUID, 705, p.X+2, p.Y, p.Z
	return s, f, advance
}

func TestColdridgeOwnQuestLootRequiresExplicitIntentAndActualScope(t *testing.T) {
	s, f, _ := coldLootRig(t)
	s.Tick(context.Background())
	if len(f.packets) != 0 || current(t, s).Enabled {
		t.Fatal("quest enabled looting without intent")
	}
	for _, complete := range []bool{false, true} {
		s, f, _ := coldLootRig(t)
		f.world.SelfQuestLog[0].Complete = complete
		enable(t, s)
		s.Tick(context.Background())
		if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || !s.questColdridge || current(t, s).PendingCorpseGuid != coldWolfGUID {
			t.Fatal("actual own quest corpse blocked with owner offline", current(t, s))
		}
	}
}

func TestColdridgeLootRejectsOtherQuestsCreaturesClassesAndUnsafeState(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"foreign_self":     func(f *fakeSession) { f.world.Self.Guid = 3 },
		"wrong_class":      func(f *fakeSession) { f.world.Self.ClassId = 8 },
		"unknown_class":    func(f *fakeSession) { f.world.Self.ClassKnown = false },
		"unknown_level":    func(f *fakeSession) { f.world.Self.LevelKnown = false },
		"level_cap":        func(f *fakeSession) { f.world.Self.Level = 6 },
		"quest":            func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 33 },
		"failed":           func(f *fakeSession) { f.world.SelfQuestLog[0].Failed = true },
		"unknown_log":      func(f *fakeSession) { f.world.SelfQuestLogKnown = false },
		"partial_log":      func(f *fakeSession) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] },
		"duplicate_slot":   func(f *fakeSession) { f.world.SelfQuestLog[24].Slot = 0 },
		"creature_entry":   func(f *fakeSession) { f.world.LootableCorpses[0].Entry = 299 },
		"encoded_entry":    func(f *fakeSession) { f.world.LootableCorpses[0].Guid = corpseGUID },
		"living":           func(f *fakeSession) { f.world.LootableCorpses[0].Alive = true },
		"not_lootable":     func(f *fakeSession) { f.world.LootableCorpses[0].DynamicFlags = 0 },
		"combat":           func(f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"unknown_position": func(f *fakeSession) { f.actions.Position.Known = false },
		"uncertain_motion": func(f *fakeSession) { f.actions.MotionUnconfirmed = true },
		"region":           func(f *fakeSession) { f.world.Self.X = -8900 },
		"estimate_region":  func(f *fakeSession) { f.actions.Position.X = -8900 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := coldLootRig(t)
			change(f)
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal("unsafe independent loot", current(t, s))
			}
		})
	}
}

func TestColdridgeLootRechecksStoppedScopeAndPreemptsUnsafeWindow(t *testing.T) {
	for _, phase := range []string{"after_lease", "open_window"} {
		for _, mode := range []string{"quest", "combat", "position", "corpse"} {
			t.Run(phase+"_"+mode, func(t *testing.T) {
				s, f, _ := coldLootRig(t)
				change := func() {
					switch mode {
					case "quest":
						f.world.SelfQuestLog[0].QuestId = 0
					case "combat":
						f.world.Self.UnitFlags |= 0x80000
					case "position":
						f.actions.Position.Known = false
					case "corpse":
						f.world.LootableCorpses[0].Entry = 299
					}
				}
				if phase == "after_lease" {
					f.afterAcquire = change
				}
				enable(t, s)
				s.Tick(context.Background())
				if phase == "after_lease" {
					if len(f.packets) != 0 || f.leaseOwner != "" {
						t.Fatal(current(t, s))
					}
					return
				}
				// A corpse may leave the nearby cache after opening; the bound
				// transaction is still legitimate. Test only own safety here.
				if mode == "corpse" {
					return
				}
				emit(s, opcode.SMSGLootResponse, windowPacket(coldWolfGUID, 0, lootItem{slot: 2, id: 750, count: 1}))
				change()
				s.Tick(context.Background())
				if len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease || current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal(current(t, s))
				}
			})
		}
	}
}

func TestColdridgeMeatNeedsOwnItemPushSlotRemovalAndRelease(t *testing.T) {
	s, f, _ := coldLootRig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(coldWolfGUID, 0, lootItem{slot: 2, id: 750, count: 1}))
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("write claimed receipt")
	}
	f.world.LootableCorpses = nil
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	emit(s, opcode.SMSGItemPushResult, pushPacket(3, 750, 1))
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("foreign item receipt claimed own meat")
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 750, 1))
	s.Tick(context.Background())
	s.Tick(context.Background())
	st := current(t, s)
	if st.ItemsStoredConfirmed != 1 || len(st.RecentItems) != 1 || st.RecentItems[0].ItemId != 750 || st.RecentItems[0].Count != 1 || st.ReleasesObserved != 0 || f.world.SelfQuestLog[0].Complete {
		t.Fatal(st)
	}
	release := releasePacket()
	// Ordinary release helper uses the other fixture's GUID; bind this receipt.
	for i := range 8 {
		release[i] = byte(coldWolfGUID >> (8 * i))
	}
	emit(s, opcode.SMSGLootReleaseResponse, release)
	s.Tick(context.Background())
	if f.leaseOwner != "" || current(t, s).ReleasesObserved != 1 {
		t.Fatal(current(t, s))
	}
}
