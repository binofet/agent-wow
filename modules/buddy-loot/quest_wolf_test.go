package buddyloot

import (
	"context"
	"math"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func wolfRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.Owner = nil
	self := f.world.Self
	self.HealthKnown, self.Health, self.MaxHealth = true, 100, 100
	self.MapKnown, self.PositionKnown, self.MovementFlagsKnown, self.SpeedsKnown = true, true, true, true
	self.RunSpeed = 7
	f.world.SelfQuestLogKnown = true
	for i := range 25 {
		f.world.SelfQuestLog = append(f.world.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i)})
	}
	f.world.SelfQuestLog[0].QuestId = 33
	f.world.LootableCorpses[0].Entry = 299
	return s, f, advance
}

func TestQuestWolfLootNeedsExplicitEnableAndOwnCompleteLog(t *testing.T) {
	s, f, _ := wolfRig(t)
	s.Tick(context.Background())
	if len(f.packets) != 0 || current(t, s).Enabled {
		t.Fatal("quest implicitly enabled loot")
	}
	for _, complete := range []bool{false, true} {
		s, f, _ := wolfRig(t)
		f.world.SelfQuestLog[0].Complete = complete
		enable(t, s)
		s.Tick(context.Background())
		if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || current(t, s).Phase != "waiting_open" {
			t.Fatalf("active quest complete%v: %+v", complete, current(t, s))
		}
	}
}

func TestQuestWolfExceptionIgnoresOwnerOnlyWithinExactScope(t *testing.T) {
	for _, mode := range []string{"nil", "absent", "afk", "combat", "far", "different_map"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := wolfRig(t)
			if mode != "nil" {
				f.world.Owner = &worldv1.PlayerState{Guid: 3, Present: true, AliveKnown: true, Alive: true, MapKnown: true, PositionKnown: true, UnitFlagsKnown: true}
			}
			switch mode {
			case "absent":
				f.world.Owner.Present = false
			case "afk":
				f.world.Owner.PlayerFlags = 2
			case "combat":
				f.world.Owner.UnitFlags = 0x80000
			case "far":
				f.world.Owner.X = 100
			case "different_map":
				f.world.Owner.MapId = 1
			}
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot {
				t.Fatalf("owner%v blocked own questwolf: %+v", mode, current(t, s))
			}
		})
	}
}

func TestQuestWolfExceptionRejectsUnknownForeignOrUnsafeState(t *testing.T) {
	tests := map[string]func(*fakeSession){
		"noquest":                func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 7 },
		"failedquest":            func(f *fakeSession) { f.world.SelfQuestLog[0].Failed = true },
		"failedstatebits":        func(f *fakeSession) { f.world.SelfQuestLog[0].State = 2 },
		"unknownlog":             func(f *fakeSession) { f.world.SelfQuestLogKnown = false },
		"partiallog":             func(f *fakeSession) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] },
		"duplicateslot":          func(f *fakeSession) { f.world.SelfQuestLog[24].Slot = 0 },
		"foreignself":            func(f *fakeSession) { f.world.Self.Guid = 3 },
		"unknownselfposition":    func(f *fakeSession) { f.world.Self.PositionKnown = false },
		"unknownmap":             func(f *fakeSession) { f.world.Self.MapKnown = false },
		"wrongmap":               func(f *fakeSession) { f.world.Self.MapId = 1 },
		"dead":                   func(f *fakeSession) { f.world.Self.Alive = false },
		"healthunknown":          func(f *fakeSession) { f.world.Self.HealthKnown = false },
		"owncombat":              func(f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"unknownmovement":        func(f *fakeSession) { f.world.Self.MovementFlagsKnown = false },
		"swimming":               func(f *fakeSession) { f.world.Self.MovementFlags = 0x200000 },
		"speedsunknown":          func(f *fakeSession) { f.world.Self.SpeedsKnown = false },
		"fastspeed":              func(f *fakeSession) { f.world.Self.RunSpeed = 8 },
		"nonfinitespeed":         func(f *fakeSession) { f.world.Self.RunSpeed = float32(math.NaN()) },
		"unknowncurrentposition": func(f *fakeSession) { f.actions.Position.Known = false },
		"unconfirmedmotion":      func(f *fakeSession) { f.actions.MotionUnconfirmed = true },
		"nonwolf":                func(f *fakeSession) { f.world.LootableCorpses[0].Entry = 6 },
		"alivewolf":              func(f *fakeSession) { f.world.LootableCorpses[0].Alive = true },
		"notlootable":            func(f *fakeSession) { f.world.LootableCorpses[0].DynamicFlags = 0 },
		"corpsewrongmap":         func(f *fakeSession) { f.world.LootableCorpses[0].MapId = 1 },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			s, f, _ := wolfRig(t)
			change(f)
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatalf("unsafe scope opened loot %+v", current(t, s))
			}
		})
	}
}

func TestQuestWolfRevalidatesAfterLeaseAndDuringTransaction(t *testing.T) {
	for _, mode := range []string{"questremoved", "corpsechanged", "combat"} {
		t.Run("afterstop_"+mode, func(t *testing.T) {
			s, f, _ := wolfRig(t)
			f.afterAcquire = func() {
				switch mode {
				case "questremoved":
					f.world.SelfQuestLog[0].QuestId = 0
				case "corpsechanged":
					f.world.LootableCorpses[0].Entry = 6
				case "combat":
					f.world.Self.UnitFlags |= 0x80000
				}
			}
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" || f.releases != 1 {
				t.Fatalf("stale scope survived stop %+v", current(t, s))
			}
		})
	}
	for _, mode := range []string{"questremoved", "owncombat", "motionunconfirmed"} {
		t.Run("duringwindow_"+mode, func(t *testing.T) {
			s, f, _ := wolfRig(t)
			enable(t, s)
			s.Tick(context.Background())
			emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 0, id: 50432, count: 1}))
			switch mode {
			case "questremoved":
				f.world.SelfQuestLog[0].QuestId = 0
			case "owncombat":
				f.world.Self.UnitFlags |= 0x80000
			case "motionunconfirmed":
				f.actions.MotionUnconfirmed = true
			}
			s.Tick(context.Background())
			if len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease || current(t, s).ItemsStoredConfirmed != 0 {
				t.Fatalf("unsafe scope stored items %+v", current(t, s))
			}
			emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
			s.Tick(context.Background())
			if f.leaseOwner != "" {
				t.Fatal("preempted questloot kept lease")
			}
		})
	}
}

func TestQuestWolfOrdinaryReceiptAndReleaseRemainAuthoritative(t *testing.T) {
	s, f, _ := wolfRig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 2, id: 50432, count: 1}))
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("write claimed stored quest item")
	}
	// A fully looted corpse can disappear from the public lootable list before
	// its pending ordinary item/release receipts finish. The bound entry stays.
	f.world.LootableCorpses = nil
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	s.Tick(context.Background())
	if current(t, s).Phase != "waiting_item" {
		t.Fatal("slot removal alone confirmed item")
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(3, 50432, 1))
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("foreign receipt confirmed own item")
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 50432, 1))
	s.Tick(context.Background())
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 1 || f.packets[len(f.packets)-1].Opcode != opcode.CMSGLootRelease || f.world.SelfQuestLog[0].Complete {
		t.Fatal("loot receipt or quest progress semantics changed")
	}
	emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
	s.Tick(context.Background())
	if f.leaseOwner != "" || f.releases != 1 || current(t, s).ReleasesObserved != 1 {
		t.Fatal("normal release not confirmed")
	}
}

func TestQuestWolfApproachesOnlyBoundEligibleCorpseBeforeOpening(t *testing.T) {
	s, f, advance := wolfRig(t)
	f.world.LootableCorpses[0].X = 12
	enable(t, s)
	s.Tick(context.Background())
	if current(t, s).Phase != "approaching" || len(f.packets) != 0 {
		t.Fatal("distant wolf opened")
	}
	for range 8 {
		if current(t, s).Phase != "approaching" {
			break
		}
		advance(200 * time.Millisecond)
		s.Tick(context.Background())
	}
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || f.actions.Moving || f.approachCalls != 7 {
		t.Fatal("wolf approach failed", current(t, s))
	}
}
