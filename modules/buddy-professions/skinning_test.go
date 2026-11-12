package buddyprofessions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
)

func skinningFixture(t *testing.T) (*Service, *professionSessionFixture) {
	t.Helper()
	s, f := newProfessionServiceFixture(t)
	f.mu.Lock()
	w := f.world
	w.Self.PositionRevision = 11
	w.Self.MovementFlagsKnown = true
	w.Self.MovementFlags, w.Self.ExtraMovementFlags = 0, 0
	w.Self.ChannelObjectKnown, w.Self.ChannelSpellKnown = true, true
	w.SelfSkills[1] = &worldv1.SkillState{Slot: 1, SkillId: 393, Value: 75, MaxValue: 75, Step: 1}
	w.InventoryBags[0].Slots[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: 0x4000000000000099, Entry: 7005, StackCount: 1, MetadataKnown: true}
	c := &worldv1.CreatureState{Guid: skinProtocolTarget, Entry: 291, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 5, X: w.Self.X + 2, Y: w.Self.Y, Z: w.Self.Z, HealthKnown: true, Health: 0, MaxHealth: 100, AliveKnown: true, UnitFlagsKnown: true, UnitFlags: 0x04000000, DynamicFlagsKnown: true, DynamicFlags: 4 | 8, LevelKnown: true, Level: 5}
	w.SkinnableCorpses = []*worldv1.CreatureState{c}
	w.NearbyCreatures = []*worldv1.CreatureState{c}
	w.RequestedCreature = c
	f.actions.Position = &actionsv1.ClientPosition{Known: true, MapKnown: true, X: w.Self.X, Y: w.Self.Y, Z: w.Self.Z, Revision: 44}
	f.actions.InteractionLeaseRemainingMs = 30000
	f.mu.Unlock()
	professionPacket(t, s, opcode.SMSGInitialSpells, professionInitialFixture(3275, 2538, 2540, 7620, 8613))
	s.mu.Lock()
	s.skinRequested[291] = true
	s.mu.Unlock()
	professionPacket(t, s, opcode.SMSGCreatureQueryResponse, skinProtocolTemplateFixture("Dire Wolf", "", ""))
	professionPacket(t, s, opcode.SMSGLootResponse, skinProtocolLootFixture(1, 0))
	professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(skinProtocolTarget), 1))
	if err := s.observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, f
}

func skinningDirect(t *testing.T, s *Service, f *professionSessionFixture) *operation {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, _ := recipeForSpell(skinSpell)
	op := &operation{request: &professionsv1.AttemptRequest{SessionId: "body", RequestId: "skin-direct", SkillId: skinSkill, SpellId: skinSpell, TargetGuid: skinProtocolTarget, TargetPositionRevision: 5}, spec: spec, baseline: s.view, token: "skin-owned-token", ctx: ctx, cancel: cancel, stored: map[byte]bool{}, removed: map[byte]bool{}, pushed: map[byte]bool{}, pushCounts: map[uint32]uint32{}, receipt: &professionsv1.Operation{TargetGuid: skinProtocolTarget, TargetPositionRevision: 5, TargetEntry: 291, CastCount: 9, Status: professionsv1.AttemptStatus_RESERVED, BaselineInventoryRevision: s.view.inventoryRevision}}
	s.active, s.last = op, op
	f.mu.Lock()
	f.token = op.token
	f.actions.InteractionOwner = "professions"
	f.actions.InteractionHarvest = true
	f.actions.HarvestGuardGuids = []uint64{s.selfGUID, 3}
	f.actions.InteractionLeaseRemainingMs = 30000
	f.mu.Unlock()
	return op
}

func TestSkinningNormalFullLootProofRequiresAllActualReceipts(t *testing.T) {
	for _, mode := range []string{"success", "missing_slot", "missing_money", "wrong_release", "negative_release", "locked_slot", "skin_type"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(nil, "body", 7)
			now := time.Unix(100, 0)
			s.now = func() time.Time { return now }
			w := skinProtocolLootFixture(1, 2)
			binary.LittleEndian.PutUint32(w[9:], 3)
			if mode == "locked_slot" {
				w[14+21] = 1
			}
			if mode == "skin_type" {
				w[8] = 6
			}
			s.observeCorpseLootLocked(&modv1.WorldPacket{Opcode: opcode.SMSGLootResponse, Payload: w})
			s.observeCorpseLootLocked(&modv1.WorldPacket{Opcode: opcode.SMSGLootRemoved, Payload: []byte{0}})
			if mode != "missing_slot" {
				s.observeCorpseLootLocked(&modv1.WorldPacket{Opcode: opcode.SMSGLootRemoved, Payload: []byte{1}})
			}
			if mode != "missing_money" {
				s.observeCorpseLootLocked(&modv1.WorldPacket{Opcode: opcode.SMSGLootClearMoney})
			}
			guid, success := skinProtocolTarget, byte(1)
			if mode == "wrong_release" {
				guid++
			}
			if mode == "negative_release" {
				success = 0
			}
			s.observeCorpseLootLocked(&modv1.WorldPacket{Opcode: opcode.SMSGLootReleaseResponse, Payload: append(fullGUIDPacket(guid), success)})
			_, ok := s.fullLoot[skinProtocolTarget]
			if ok != (mode == "success") {
				t.Fatal(mode, s.fullLoot)
			}
		})
	}
	s := NewService(nil, "body", 7)
	for i := 0; i < 40; i++ {
		w := skinProtocolLootFixture(1, 0)
		g := skinProtocolTarget + uint64(i)
		binary.LittleEndian.PutUint64(w, g)
		s.observeCorpseLootLocked(&modv1.WorldPacket{Opcode: opcode.SMSGLootResponse, Payload: w})
		s.observeCorpseLootLocked(&modv1.WorldPacket{Opcode: opcode.SMSGLootReleaseResponse, Payload: append(fullGUIDPacket(g), 1)})
	}
	if len(s.fullLoot) != 32 {
		t.Fatal("unbounded own full-loot proof", len(s.fullLoot))
	}
}

func TestSkinningTargetGateUsesActualPrerequisitesAndCanonicalPureThreshold(t *testing.T) {
	for name, mutate := range map[string]func(*Service, *professionSessionFixture){
		"unknown_flags":       func(s *Service, f *professionSessionFixture) { s.view.raw.RequestedCreature.UnitFlagsKnown = false },
		"unknown_dynamic":     func(s *Service, f *professionSessionFixture) { s.view.raw.RequestedCreature.DynamicFlagsKnown = false },
		"not_skinnable":       func(s *Service, f *professionSessionFixture) { s.view.raw.RequestedCreature.UnitFlags = 0 },
		"normal_loot_remains": func(s *Service, f *professionSessionFixture) { s.view.raw.RequestedCreature.DynamicFlags = 1 },
		"alive":               func(s *Service, f *professionSessionFixture) { s.view.raw.RequestedCreature.Alive = true },
		"gone":                func(s *Service, f *professionSessionFixture) { s.view.raw.RequestedCreature.Present = false },
		"unknown_level":       func(s *Service, f *professionSessionFixture) { s.view.raw.RequestedCreature.LevelKnown = false },
		"far":                 func(s *Service, f *professionSessionFixture) { s.view.raw.RequestedCreature.X += 10 },
		"high":                func(s *Service, f *professionSessionFixture) { s.view.raw.RequestedCreature.Z += 2 },
		"revision":            func(s *Service, f *professionSessionFixture) { s.view.raw.RequestedCreature.PositionRevision++ },
		"template_unknown":    func(s *Service, f *professionSessionFixture) { delete(s.skinTemplates, 291) },
		"herb": func(s *Service, f *professionSessionFixture) {
			m := s.skinTemplates[291]
			m.typeFlags |= 0x100
			s.skinTemplates[291] = m
		},
		"mine": func(s *Service, f *professionSessionFixture) {
			m := s.skinTemplates[291]
			m.typeFlags |= 0x200
			s.skinTemplates[291] = m
		},
		"engineer": func(s *Service, f *professionSessionFixture) {
			m := s.skinTemplates[291]
			m.typeFlags |= 0x8000
			s.skinTemplates[291] = m
		},
		"party_tap_missing": func(s *Service, f *professionSessionFixture) { s.view.raw.RequestedCreature.DynamicFlags &^= 8 },
		"replayed_guid":     func(s *Service, f *professionSessionFixture) { s.skinAttempted[skinProtocolTarget] = true },
		"knife_missing":     func(s *Service, f *professionSessionFixture) { s.view.counts[7005] = 0 },
		"spell_unknown":     func(s *Service, f *professionSessionFixture) { delete(s.book.spells, 8613) },
		"skill_unknown":     func(s *Service, f *professionSessionFixture) { delete(s.view.skills, 393) },
		"channel_unknown":   func(s *Service, f *professionSessionFixture) { s.view.self.ChannelSpellKnown = false },
		"moving":            func(s *Service, f *professionSessionFixture) { s.actions.Moving = true },
	} {
		t.Run(name, func(t *testing.T) {
			s, f := skinningFixture(t)
			s.mu.Lock()
			mutate(s, f)
			c, reason := s.skinningTargetLocked(skinProtocolTarget, 5, nil)
			s.mu.Unlock()
			if c != nil || reason == "" {
				t.Fatal("unsafe target ready", c, reason)
			}
		})
	}
	for _, tc := range []struct{ level, required uint32 }{{1, 0}, {9, 0}, {10, 0}, {11, 10}, {19, 90}, {20, 100}, {60, 300}} {
		if skinRequiredSkill(tc.level) != tc.required {
			t.Fatal(tc)
		}
	}
	s, _ := skinningFixture(t)
	s.mu.Lock()
	c, reason := s.skinningTargetLocked(skinProtocolTarget, 5, nil)
	s.mu.Unlock()
	if c == nil || reason != "" {
		t.Fatal(c, reason)
	}
}

func TestSkinningFinalWorldReadAndExactLeaseRejectChangesBeforeCast(t *testing.T) {
	for _, mode := range []string{"success", "dead", "combat", "self_pose", "map", "unknown_inventory", "inventory_revision", "knife", "skill", "gone", "target_pose", "target_far", "target_level", "capacity", "replacement_token", "expired_lease", "party_tap_lost", "skinnable_cleared", "normal_loot_returned"} {
		t.Run(mode, func(t *testing.T) {
			s, f := skinningFixture(t)
			op := skinningDirect(t, s, f)
			if mode == "replacement_token" {
				f.mu.Lock()
				f.token = "replacement-token"
				f.mu.Unlock()
			}
			if mode == "expired_lease" {
				f.mu.Lock()
				f.actions.InteractionLeaseRemainingMs = 0
				f.mu.Unlock()
			}
			f.mu.Lock()
			f.worldHook = func(r *worldv1.SnapshotRequest) {
				if r.CreatureGuid != skinProtocolTarget {
					t.Error("final read omitted exact creature")
				}
				w := f.world
				c := w.RequestedCreature
				switch mode {
				case "dead":
					w.Self.Alive = false
				case "combat":
					w.Self.UnitFlags |= 0x80000
				case "self_pose":
					w.Self.PositionRevision++
				case "map":
					w.Self.MapId++
				case "unknown_inventory":
					w.InventoryKnown = false
				case "inventory_revision":
					w.InventoryRevision++
				case "knife":
					w.InventoryBags[0].Slots[2] = &worldv1.BackpackSlot{Slot: 2}
				case "skill":
					w.SelfSkills[1].Value = 0
				case "gone":
					c.Present = false
				case "target_pose":
					c.PositionRevision++
				case "target_far":
					c.X += 10
				case "target_level":
					c.Level = 60
				case "party_tap_lost":
					c.DynamicFlags &^= 8
				case "skinnable_cleared":
					c.UnitFlags &^= 0x04000000
				case "normal_loot_returned":
					c.DynamicFlags |= 1
				case "capacity":
					for i, row := range w.InventoryBags[0].Slots {
						if row.ItemGuid == 0 {
							w.InventoryBags[0].Slots[i] = &worldv1.BackpackSlot{Slot: uint32(i), ItemGuid: 0x4000000000001000 + uint64(i), Entry: 7070, StackCount: 1, MetadataKnown: true}
						}
					}
				}
			}
			f.mu.Unlock()
			attempted, err := s.skinWrite(op.ctx, op, opcode.CMSGCastSpell, skinCastPacket(9, skinProtocolTarget))
			if mode == "success" {
				if !attempted || err != nil {
					t.Fatal(attempted, err)
				}
				f.mu.Lock()
				events := append([]string(nil), f.rpcEvents...)
				f.mu.Unlock()
				want := []string{"buddy-actions/acquire_interaction", "buddy-world/snapshot", fmt.Sprintf("send:%x", opcode.CMSGCastSpell)}
				if !reflect.DeepEqual(events[len(events)-3:], want) {
					t.Fatal("RPC after finalworld", events)
				}
			} else if attempted || err == nil {
				t.Fatal("unsafe last-read change wrote", attempted, err)
			}
			f.mu.Lock()
			packets := len(f.packets)
			f.mu.Unlock()
			if mode != "success" && packets != 0 {
				t.Fatal("unexpected packet", packets)
			}
			s.mu.Lock()
			s.active = nil
			s.mu.Unlock()
		})
	}
}

func skinningRequest(s *Service, id string) *professionsv1.AttemptRequest {
	r := professionRequest(s, id, skinSpell)
	r.TargetGuid, r.TargetPositionRevision = skinProtocolTarget, 5
	return r
}

func TestSkinningOneFiniteFullLastSlotCycleRequiresGOAndAllMaterialReceipts(t *testing.T) {
	s, f := skinningFixture(t)
	// Joel cleared ordinary loot; Rook received no normal loot-window receipt.
	s.mu.Lock()
	delete(s.fullLoot, skinProtocolTarget)
	s.mu.Unlock()
	f.mu.Lock()
	for i, row := range f.world.InventoryBags[0].Slots {
		if row.ItemGuid == 0 && i != 3 {
			f.world.InventoryBags[0].Slots[i] = &worldv1.BackpackSlot{Slot: uint32(i), ItemGuid: 0x4000000000001000 + uint64(i), Entry: 7070, StackCount: 1, MetadataKnown: true}
		}
	}
	f.mu.Unlock()
	if err := s.observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.beforeSend = func(p *modv1.SendPacketRequest) {
		s.mu.Lock()
		op := s.active
		count := byte(op.receipt.CastCount)
		s.mu.Unlock()
		switch p.Opcode {
		case opcode.CMSGCastSpell:
			for _, code := range []uint32{opcode.SMSGSpellStart, opcode.SMSGSpellGo} {
				b := skinProtocolCastFixture(code == opcode.SMSGSpellGo, 7, skinProtocolTarget, 0x100)
				b[2*len(protocolPackedFixture(7))] = count
				professionPacket(t, s, code, b)
			}
			f.mu.Lock()
			f.world.RequestedCreature.UnitFlags = 0
			f.world.RequestedCreature.DynamicFlags = 1
			f.world.SkinnableCorpses = nil
			f.mu.Unlock()
			professionPacket(t, s, opcode.SMSGLootResponse, skinProtocolLootFixture(6, 1))
		case opcode.CMSGAutostoreLootItem:
			f.mu.Lock()
			f.world.InventoryBags[0].Slots[3] = &worldv1.BackpackSlot{Slot: 3, ItemGuid: 0x4000000000005555, Entry: 2318, StackCount: 2, MetadataKnown: true}
			f.world.InventoryRevision++
			f.world.BackpackRevision++
			f.mu.Unlock()
			professionPacket(t, s, opcode.SMSGLootRemoved, []byte{0})
			push := professionPushReceipt(7, false, false, 2318, 2, 2)
			binary.LittleEndian.PutUint32(push[21:], 26)
			professionPacket(t, s, opcode.SMSGItemPushResult, push)
		case opcode.CMSGLootRelease:
			professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(skinProtocolTarget), 1))
		}
	}
	r := skinningRequest(s, "skin-full-cycle")
	if _, err := s.Attempt(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	op := s.last
	s.mu.Unlock()
	professionWaitDone(t, op)
	receipt := professionReceipt(s, op)
	if receipt.Status != professionsv1.AttemptStatus_COMPLETED || !receipt.CastTransportWritten || !receipt.OwnGoObserved || !receipt.LootWindowObserved || receipt.OwnLootedItemsObserved != 2 || receipt.LootSlotsRemoved != 1 || !receipt.LootReleaseObserved || !receipt.OutputInventoryObserved || receipt.ObservedInventoryRevision <= receipt.BaselineInventoryRevision || !receipt.LeaseReleased || receipt.CleanupUnconfirmed || receipt.SkillIncreased {
		t.Fatal(receipt)
	}
	f.mu.Lock()
	packets := append([]*modv1.SendPacketRequest(nil), f.packets...)
	f.mu.Unlock()
	if len(packets) != 3 || packets[0].Opcode != opcode.CMSGCastSpell || packets[1].Opcode != opcode.CMSGAutostoreLootItem || packets[2].Opcode != opcode.CMSGLootRelease {
		t.Fatal(packets)
	}
	if _, err := s.Attempt(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	n := len(f.packets)
	f.mu.Unlock()
	if n != 3 {
		t.Fatal("same request replayed", n)
	}
}

func TestSkinningExactGOAndNewerInventoryAreSeparateFromSkillRank(t *testing.T) {
	s, f := skinningFixture(t)
	op := skinningDirect(t, s, f)
	op.castReserved = true
	foreign := skinProtocolCastFixture(true, 7, skinProtocolTarget+1, 0x100)
	professionPacket(t, s, opcode.SMSGSpellGo, foreign)
	professionPacket(t, s, opcode.SMSGLootResponse, skinProtocolLootFixture(6, 1))
	if s.stepSkin(op) || op.receipt.OwnGoObserved || len(f.packets) != 0 {
		t.Fatal("foreign target GO authorized store")
	}
	professionPacket(t, s, opcode.SMSGSpellGo, skinProtocolCastFixture(true, 7, skinProtocolTarget, 0x100))
	s.mu.Lock()
	op.stored[0], op.removed[0], op.pushed[0] = true, true, true
	op.releaseReserved = true
	op.receipt.LootReleaseObserved = true
	op.receipt.SkillIncreased = true
	s.view.inventoryRevision++
	s.mu.Unlock()
	if s.stepSkin(op) || op.receipt.OutputInventoryObserved {
		t.Fatal("skill-up or newer unchanged inventory forged materials")
	}
	s.mu.Lock()
	counts := make(map[uint32]uint32)
	for entry, count := range s.view.counts {
		counts[entry] = count
	}
	s.view.counts = counts
	s.view.counts[2318] = 2
	s.mu.Unlock()
	if !s.stepSkin(op) || op.receipt.Status != professionsv1.AttemptStatus_COMPLETED {
		t.Fatal(op.receipt)
	}
	s.mu.Lock()
	s.active = nil
	s.mu.Unlock()
}

func TestSkinningAmbiguousCastCleanupWaitsForAutomaticWindowAndNeverReplays(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("late%t", late), func(t *testing.T) {
			s, f := skinningFixture(t)
			f.sendErr[opcode.CMSGCastSpell] = errors.New("ambiguous cast write")
			f.beforeSend = func(p *modv1.SendPacketRequest) {
				if p.Opcode == opcode.CMSGCastSpell {
					s.mu.Lock()
					count := byte(s.active.receipt.CastCount)
					s.mu.Unlock()
					b := skinProtocolCastFixture(true, 7, skinProtocolTarget, 0x100)
					b[2*len(protocolPackedFixture(7))] = count
					professionPacket(t, s, opcode.SMSGSpellGo, b)
					if late {
						go func() {
							time.Sleep(80 * time.Millisecond)
							professionPacket(t, s, opcode.SMSGLootResponse, skinProtocolLootFixture(6, 0))
						}()
					}
				}
				if p.Opcode == opcode.CMSGLootRelease {
					professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(skinProtocolTarget), 1))
				}
			}
			r := skinningRequest(s, "skin-ambiguous")
			if _, err := s.Attempt(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			op := s.last
			s.mu.Unlock()
			professionWaitDone(t, op)
			receipt := professionReceipt(s, op)
			if receipt.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED || !receipt.OwnGoObserved || !receipt.LeaseReleased || receipt.CleanupUnconfirmed == late {
				t.Fatal(receipt)
			}
			f.mu.Lock()
			codes := []uint32{}
			for _, p := range f.packets {
				codes = append(codes, p.Opcode)
			}
			f.mu.Unlock()
			want := []uint32{opcode.CMSGCastSpell}
			if late {
				want = append(want, opcode.CMSGLootRelease)
			}
			if !reflect.DeepEqual(codes, want) {
				t.Fatal(codes)
			}
			if _, err := s.Attempt(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Attempt(context.Background(), skinningRequest(s, "another-id")); err == nil {
				t.Fatal("ambiguous target retried with new ID")
			}
		})
	}
}

func TestSkinningDefinitiveFailureSurvivesLateWindowAndCleanup(t *testing.T) {
	s, f := skinningFixture(t)
	op := skinningDirect(t, s, f)
	op.castReserved = true
	professionPacket(t, s, opcode.SMSGCastFailed, []byte{9, 0xa5, 0x21, 0, 0, 121})
	professionPacket(t, s, opcode.SMSGLootResponse, skinProtocolLootFixture(6, 0))
	if op.receipt.Status != professionsv1.AttemptStatus_SERVER_REJECTED || !strings.Contains(op.receipt.Reason, "121") {
		t.Fatal(op.receipt)
	}
	f.beforeSend = func(p *modv1.SendPacketRequest) {
		if p.Opcode == opcode.CMSGLootRelease {
			professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(skinProtocolTarget), 1))
		}
	}
	s.finishSkin(op)
	if op.receipt.Status != professionsv1.AttemptStatus_SERVER_REJECTED || !op.receipt.LeaseReleased || op.receipt.CleanupUnconfirmed {
		t.Fatal(op.receipt)
	}
}

func TestSkinningForeignInventoryErrorCannotSettleMissingAutomaticWindow(t *testing.T) {
	s, f := skinningFixture(t)
	op := skinningDirect(t, s, f)
	op.castReserved = true
	professionPacket(t, s, opcode.SMSGSpellGo, skinProtocolCastFixture(true, 7, skinProtocolTarget, 0x100))
	p := make([]byte, 18)
	p[0] = 50
	binary.LittleEndian.PutUint64(p[1:], 0x400000000000dead)
	professionPacket(t, s, opcode.SMSGInventoryChangeFailure, p)
	if op.receipt.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED || !skinCleanupPending(op) {
		t.Fatal(op.receipt)
	}
	f.beforeSend = func(p *modv1.SendPacketRequest) {
		if p.Opcode == opcode.CMSGLootRelease {
			professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(skinProtocolTarget), 1))
		}
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		professionPacket(t, s, opcode.SMSGLootResponse, skinProtocolLootFixture(6, 0))
	}()
	s.finishSkin(op)
	if op.receipt.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED || !op.receipt.LootReleaseObserved || op.receipt.CleanupUnconfirmed || !op.receipt.LeaseReleased {
		t.Fatal(op.receipt)
	}
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLootRelease {
		t.Fatal(f.packets)
	}
}

func TestSkinningTemplateQueriesOnlyObservedBoundedReservedEntries(t *testing.T) {
	s, f := skinningFixture(t)
	s.mu.Lock()
	s.skinRequested = map[uint32]bool{}
	s.mu.Unlock()
	s.querySkinTemplates(context.Background(), f.world)
	s.querySkinTemplates(context.Background(), f.world)
	f.mu.Lock()
	n := 0
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGCreatureQuery {
			n++
		}
	}
	f.mu.Unlock()
	if n != 1 {
		t.Fatal("query replayed", n)
	}
	if _, err := s.GetState(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatal(err)
	}
}

func TestSkinningCasterBearingReceiptsRequireExactOwnActor(t *testing.T) {
	for _, caster := range []uint64{0, 8} {
		for _, code := range []uint32{opcode.SMSGSpellStart, opcode.SMSGSpellGo, opcode.SMSGSpellFailure} {
			t.Run(fmt.Sprintf("caster%d_code%x", caster, code), func(t *testing.T) {
				s, f := skinningFixture(t)
				op := skinningDirect(t, s, f)
				op.castReserved = true
				p := skinProtocolCastFixture(code == opcode.SMSGSpellGo, caster, skinProtocolTarget, 0x100)
				if code == opcode.SMSGSpellFailure {
					p = append(protocolPackedFixture(caster), 9, 0xa5, 0x21, 0, 0, 121)
				}
				professionPacket(t, s, code, p)
				if op.receipt.OwnStartObserved || op.receipt.OwnGoObserved || op.receipt.FailureCode != 0 || op.receipt.Status != professionsv1.AttemptStatus_RESERVED {
					t.Fatal("foreign or zero actor became own receipt", op.receipt)
				}
				s.mu.Lock()
				s.active = nil
				s.mu.Unlock()
			})
		}
	}
}
