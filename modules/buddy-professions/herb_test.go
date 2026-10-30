package buddyprofessions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
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

const herbBehaviorTarget uint64 = 0xf11000065200002a

func herbBehaviorTemplate() []byte {
	p := binary.LittleEndian.AppendUint32(nil, 1618)
	p = binary.LittleEndian.AppendUint32(p, 3)
	p = binary.LittleEndian.AppendUint32(p, 1)
	p = append(p, []byte("Observed test herb")...)
	p = append(p, 0, 0, 0, 0, 0, 0, 0)
	data := [24]uint32{}
	data[0] = 29
	data[1] = 1618
	data[3] = 1
	for _, v := range data {
		p = binary.LittleEndian.AppendUint32(p, v)
	}
	p = binary.LittleEndian.AppendUint32(p, math.Float32bits(1))
	return append(p, make([]byte, 24)...)
}
func herbBehaviorCast(goEvent bool, caster, target uint64, flags uint32) []byte {
	p := skinProtocolCastFixture(goEvent, caster, target, flags)
	header := 2 * len(protocolPackedFixture(caster))
	binary.LittleEndian.PutUint32(p[header+1:], 2366)
	offset := header + 13
	if goEvent {
		offset += 10
	}
	binary.LittleEndian.PutUint32(p[offset:], 0x800)
	return p
}
func herbBehaviorLoot(kind byte, n int) []byte {
	p := skinProtocolLootFixture(kind, n)
	binary.LittleEndian.PutUint64(p, herbBehaviorTarget)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint32(p[14+i*22+1:], 2447+uint32(i))
	}
	return p
}
func herbBehaviorFixture(t *testing.T) (*Service, *professionSessionFixture) {
	t.Helper()
	s, f := newProfessionServiceFixture(t)
	f.mu.Lock()
	w := f.world
	w.Self.PositionRevision = 11
	w.Self.ChannelObjectKnown, w.Self.ChannelSpellKnown = true, true
	w.SelfSkills[1] = &worldv1.SkillState{Slot: 1, SkillId: 182, Value: 75, MaxValue: 75, Step: 1}
	g := &worldv1.GameObjectState{Guid: herbBehaviorTarget, Entry: 1618, Present: true, TypeKnown: true, GameObjectType: 3, MapKnown: true, PositionKnown: true, PositionRevision: 5, X: w.Self.X + 2, Y: w.Self.Y, Z: w.Self.Z, FlagsKnown: true, Flags: 2, DynamicFlagsKnown: true, DynamicFlags: 0xffff0000, StateKnown: true, State: 1, CreatedByKnown: true}
	w.RequestedGameObjects = []*worldv1.GameObjectState{g}
	f.actions.Position = &actionsv1.ClientPosition{Known: true, MapKnown: true, X: w.Self.X, Y: w.Self.Y, Z: w.Self.Z, Revision: 44}
	f.actions.InteractionLeaseRemainingMs = 30000
	f.mu.Unlock()
	professionPacket(t, s, opcode.SMSGInitialSpells, professionInitialFixture(3275, 2538, 2540, 7620, 8613, 2366))
	s.mu.Lock()
	s.herbRequested[1618] = true
	s.mu.Unlock()
	professionPacket(t, s, opcode.SMSGGameobjectQueryResponse, herbBehaviorTemplate())
	if err := s.observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, f
}

func herbBehaviorDirect(t *testing.T, s *Service, f *professionSessionFixture) *operation {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, _ := recipeForSpell(herbSpell)
	op := &operation{request: &professionsv1.AttemptRequest{SessionId: "body", RequestId: "herb-direct", SkillId: herbSkill, SpellId: herbSpell, TargetGuid: herbBehaviorTarget, TargetPositionRevision: 5}, spec: spec, baseline: s.view, token: "herb-owned-token", ctx: ctx, cancel: cancel, stored: map[byte]bool{}, removed: map[byte]bool{}, pushed: map[byte]bool{}, pushCounts: map[uint32]uint32{}, receipt: &professionsv1.Operation{TargetGuid: herbBehaviorTarget, TargetPositionRevision: 5, TargetEntry: 1618, CastCount: 9, Status: professionsv1.AttemptStatus_RESERVED, BaselineInventoryRevision: s.view.inventoryRevision}}
	s.active, s.last = op, op
	f.mu.Lock()
	f.token = op.token
	f.actions.InteractionOwner = "professions"
	f.actions.InteractionLeaseRemainingMs = 30000
	f.mu.Unlock()
	return op
}

func TestHerbFinalWorldReadAndExactLeaseRejectChangesBeforeCast(t *testing.T) {
	for _, mode := range []string{"success", "dead", "combat", "self_pose", "map", "unknown_inventory", "inventory_revision", "flags", "skill", "gone", "target_pose", "target_far", "target_state", "capacity", "replacement_token", "expired_lease"} {
		t.Run(mode, func(t *testing.T) {
			s, f := herbBehaviorFixture(t)
			op := herbBehaviorDirect(t, s, f)
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
				if r.GameObjectEntry != 1618 {
					t.Error("final read omitted exact object")
				}
				w := f.world
				c := w.RequestedGameObjects[0]
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
				case "flags":
					w.RequestedGameObjects[0].FlagsKnown = false
				case "skill":
					w.SelfSkills[1].Value = 0
				case "gone":
					c.Present = false
				case "target_pose":
					c.PositionRevision++
				case "target_far":
					c.X += 10
				case "target_state":
					w.RequestedGameObjects[0].State = 0
				case "capacity":
					for i, row := range w.InventoryBags[0].Slots {
						if row.ItemGuid == 0 {
							w.InventoryBags[0].Slots[i] = &worldv1.BackpackSlot{Slot: uint32(i), ItemGuid: 0x4000000000001000 + uint64(i), Entry: 7070, StackCount: 1, MetadataKnown: true}
						}
					}
				}
			}
			f.mu.Unlock()
			attempted, err := s.herbWrite(op.ctx, op, opcode.CMSGCastSpell, herbCastPacket(9, herbBehaviorTarget))
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

func herbBehaviorRequest(s *Service, id string) *professionsv1.AttemptRequest {
	r := professionRequest(s, id, herbSpell)
	r.TargetGuid, r.TargetPositionRevision = herbBehaviorTarget, 5
	return r
}

func TestHerbOneFiniteFullLastSlotCycleRequiresGOAndAllMaterialReceipts(t *testing.T) {
	s, f := herbBehaviorFixture(t)
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
				b := herbBehaviorCast(code == opcode.SMSGSpellGo, 7, herbBehaviorTarget, 0x100)
				b[2*len(protocolPackedFixture(7))] = count
				professionPacket(t, s, code, b)
			}
			f.mu.Lock()
			f.world.RequestedGameObjects[0].Flags = 1
			f.world.RequestedGameObjects[0].State = 0
			f.mu.Unlock()
			professionPacket(t, s, opcode.SMSGLootResponse, herbBehaviorLoot(6, 1))
		case opcode.CMSGAutostoreLootItem:
			f.mu.Lock()
			f.world.InventoryBags[0].Slots[3] = &worldv1.BackpackSlot{Slot: 3, ItemGuid: 0x4000000000005555, Entry: 2447, StackCount: 2, MetadataKnown: true}
			f.world.InventoryRevision++
			f.world.BackpackRevision++
			f.mu.Unlock()
			professionPacket(t, s, opcode.SMSGLootRemoved, []byte{0})
			push := professionPushReceipt(7, false, false, 2447, 2, 2)
			binary.LittleEndian.PutUint32(push[21:], 26)
			professionPacket(t, s, opcode.SMSGItemPushResult, push)
		case opcode.CMSGLootRelease:
			professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(herbBehaviorTarget), 1))
		}
	}
	r := herbBehaviorRequest(s, "herb-full-cycle")
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

func TestHerbExactGOAndNewerInventoryAreSeparateFromSkillRank(t *testing.T) {
	s, f := herbBehaviorFixture(t)
	op := herbBehaviorDirect(t, s, f)
	op.castReserved = true
	foreign := herbBehaviorCast(true, 7, herbBehaviorTarget+1, 0x100)
	professionPacket(t, s, opcode.SMSGSpellGo, foreign)
	professionPacket(t, s, opcode.SMSGLootResponse, herbBehaviorLoot(6, 1))
	if s.stepHerb(op) || op.receipt.OwnGoObserved || len(f.packets) != 0 {
		t.Fatal("foreign target GO authorized store")
	}
	professionPacket(t, s, opcode.SMSGSpellGo, herbBehaviorCast(true, 7, herbBehaviorTarget, 0x100))
	s.mu.Lock()
	op.stored[0], op.removed[0], op.pushed[0] = true, true, true
	op.releaseReserved = true
	op.receipt.LootReleaseObserved = true
	op.receipt.SkillIncreased = true
	s.view.inventoryRevision++
	s.mu.Unlock()
	if s.stepHerb(op) || op.receipt.OutputInventoryObserved {
		t.Fatal("skill-up or newer unchanged inventory forged materials")
	}
	s.mu.Lock()
	counts := make(map[uint32]uint32)
	for entry, count := range s.view.counts {
		counts[entry] = count
	}
	s.view.counts = counts
	s.view.counts[2447] = 2
	s.mu.Unlock()
	if !s.stepHerb(op) || op.receipt.Status != professionsv1.AttemptStatus_COMPLETED {
		t.Fatal(op.receipt)
	}
	s.mu.Lock()
	s.active = nil
	s.mu.Unlock()
}

func TestHerbAmbiguousCastCleanupWaitsForAutomaticWindowAndNeverReplays(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("late%t", late), func(t *testing.T) {
			s, f := herbBehaviorFixture(t)
			f.sendErr[opcode.CMSGCastSpell] = errors.New("ambiguous cast write")
			f.beforeSend = func(p *modv1.SendPacketRequest) {
				if p.Opcode == opcode.CMSGCastSpell {
					s.mu.Lock()
					count := byte(s.active.receipt.CastCount)
					s.mu.Unlock()
					b := herbBehaviorCast(true, 7, herbBehaviorTarget, 0x100)
					b[2*len(protocolPackedFixture(7))] = count
					professionPacket(t, s, opcode.SMSGSpellGo, b)
					if late {
						go func() {
							time.Sleep(80 * time.Millisecond)
							professionPacket(t, s, opcode.SMSGLootResponse, herbBehaviorLoot(6, 0))
						}()
					}
				}
				if p.Opcode == opcode.CMSGLootRelease {
					professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(herbBehaviorTarget), 1))
				}
			}
			r := herbBehaviorRequest(s, "herb-ambiguous")
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
			if _, err := s.Attempt(context.Background(), herbBehaviorRequest(s, "another-id")); err == nil {
				t.Fatal("ambiguous target retried with new ID")
			}
		})
	}
}

func TestHerbDefinitiveFailureSurvivesLateWindowAndCleanup(t *testing.T) {
	s, f := herbBehaviorFixture(t)
	op := herbBehaviorDirect(t, s, f)
	op.castReserved = true
	professionPacket(t, s, opcode.SMSGCastFailed, []byte{9, 0x3e, 0x09, 0, 0, 121})
	professionPacket(t, s, opcode.SMSGLootResponse, herbBehaviorLoot(6, 0))
	if op.receipt.Status != professionsv1.AttemptStatus_SERVER_REJECTED || !strings.Contains(op.receipt.Reason, "121") {
		t.Fatal(op.receipt)
	}
	f.beforeSend = func(p *modv1.SendPacketRequest) {
		if p.Opcode == opcode.CMSGLootRelease {
			professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(herbBehaviorTarget), 1))
		}
	}
	s.finishHerb(op)
	if op.receipt.Status != professionsv1.AttemptStatus_SERVER_REJECTED || !op.receipt.LeaseReleased || op.receipt.CleanupUnconfirmed {
		t.Fatal(op.receipt)
	}
}

func TestHerbForeignInventoryErrorCannotSettleMissingAutomaticWindow(t *testing.T) {
	s, f := herbBehaviorFixture(t)
	op := herbBehaviorDirect(t, s, f)
	op.castReserved = true
	professionPacket(t, s, opcode.SMSGSpellGo, herbBehaviorCast(true, 7, herbBehaviorTarget, 0x100))
	p := make([]byte, 18)
	p[0] = 50
	binary.LittleEndian.PutUint64(p[1:], 0x400000000000dead)
	professionPacket(t, s, opcode.SMSGInventoryChangeFailure, p)
	if op.receipt.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED || !herbCleanupPending(op) {
		t.Fatal(op.receipt)
	}
	f.beforeSend = func(p *modv1.SendPacketRequest) {
		if p.Opcode == opcode.CMSGLootRelease {
			professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(herbBehaviorTarget), 1))
		}
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		professionPacket(t, s, opcode.SMSGLootResponse, herbBehaviorLoot(6, 0))
	}()
	s.finishHerb(op)
	if op.receipt.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED || !op.receipt.LootReleaseObserved || op.receipt.CleanupUnconfirmed || !op.receipt.LeaseReleased {
		t.Fatal(op.receipt)
	}
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLootRelease {
		t.Fatal(f.packets)
	}
}

func TestHerbTemplateQueriesOnlyObservedBoundedReservedEntries(t *testing.T) {
	s, f := herbBehaviorFixture(t)
	s.mu.Lock()
	s.herbRequested = map[uint32]bool{}
	s.mu.Unlock()
	s.queryHerbTemplates(context.Background(), f.world)
	s.queryHerbTemplates(context.Background(), f.world)
	f.mu.Lock()
	n := 0
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGGameobjectQuery {
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

func TestHerbCasterBearingReceiptsRequireExactOwnActor(t *testing.T) {
	for _, caster := range []uint64{0, 8} {
		for _, code := range []uint32{opcode.SMSGSpellStart, opcode.SMSGSpellGo, opcode.SMSGSpellFailure} {
			t.Run(fmt.Sprintf("caster%d_code%x", caster, code), func(t *testing.T) {
				s, f := herbBehaviorFixture(t)
				op := herbBehaviorDirect(t, s, f)
				op.castReserved = true
				p := herbBehaviorCast(code == opcode.SMSGSpellGo, caster, herbBehaviorTarget, 0x100)
				if code == opcode.SMSGSpellFailure {
					p = append(protocolPackedFixture(caster), 9, 0x3e, 0x09, 0, 0, 121)
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

func TestHerbTargetRequiresActualOrdinaryChestAndVerifiedPureLock(t *testing.T) {
	for _, mode := range []string{"locked_ready", "unlocked_ready", "quest_ui_bits", "no_knife_needed", "unknown_flags", "in_use", "no_interact", "unknown_dynamic", "unknown_state", "active_state", "unknown_creator", "player_created", "unknown_map", "unknown_pose", "wrong_entry", "wrong_type", "duplicate_guid", "template_missing", "pure_below", "pure_exact", "already_attempted", "finite_capacity"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := herbBehaviorFixture(t)
			s.mu.Lock()
			g := s.view.raw.RequestedGameObjects[0]
			switch mode {
			case "unlocked_ready":
				g.Flags = 0
			case "quest_ui_bits":
				g.DynamicFlags |= 1 | 8
			case "no_knife_needed":
				delete(s.view.counts, 7005)
			case "unknown_flags":
				g.FlagsKnown = false
			case "in_use":
				g.Flags |= 1
			case "no_interact":
				g.DynamicFlags |= 4
			case "unknown_dynamic":
				g.DynamicFlagsKnown = false
			case "unknown_state":
				g.StateKnown = false
			case "active_state":
				g.State = 0
			case "unknown_creator":
				g.CreatedByKnown = false
			case "player_created":
				g.CreatedByGuid = 7
			case "unknown_map":
				g.MapKnown = false
			case "unknown_pose":
				g.PositionKnown = false
			case "wrong_entry":
				g.Entry++
			case "wrong_type":
				g.GameObjectType = 8
			case "duplicate_guid":
				s.view.raw.RequestedGameObjects = append(s.view.raw.RequestedGameObjects, g)
			case "template_missing":
				delete(s.herbTemplates, g.Entry)
			case "pure_below", "pure_exact":
				m := s.herbTemplates[g.Entry]
				m.requiredSkill, m.lockID = 75, 10
				s.herbTemplates[g.Entry] = m
				if mode == "pure_below" {
					rank := s.view.skills[herbSkill]
					rank.value = 74
					s.view.skills[herbSkill] = rank
				}
			case "already_attempted":
				s.herbAttempted[g.Guid] = true
			case "finite_capacity":
				for i := uint64(1); i <= 256; i++ {
					s.herbAttempted[g.Guid+i] = true
				}
			}
			_, reason := s.herbalismTargetLocked(herbBehaviorTarget, 5, nil)
			rows := s.herbTargetsLocked()
			s.mu.Unlock()
			wantReady := mode == "locked_ready" || mode == "unlocked_ready" || mode == "quest_ui_bits" || mode == "no_knife_needed" || mode == "pure_exact"
			if (reason == "") != wantReady || len(rows) == 0 || rows[0].Ready != wantReady {
				t.Fatal(mode, reason, rows)
			}
		})
	}
}

func TestHerbContradictoryWindowStopsImplicitSlotAndCleanupWrites(t *testing.T) {
	for _, mode := range []string{"foreign", "normal_type", "malformed", "duplicate", "final_world_foreign", "prior_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, f := herbBehaviorFixture(t)
			op := herbBehaviorDirect(t, s, f)
			op.castReserved = true
			professionPacket(t, s, opcode.SMSGSpellGo, herbBehaviorCast(true, 7, herbBehaviorTarget, 0x100))
			professionPacket(t, s, opcode.SMSGLootResponse, herbBehaviorLoot(6, 1))
			p := herbBehaviorLoot(6, 1)
			switch mode {
			case "foreign", "final_world_foreign", "prior_failure":
				binary.LittleEndian.PutUint64(p, herbBehaviorTarget+1)
			case "normal_type":
				p[8] = 1
			case "malformed":
				p = p[:13]
			}
			if mode == "prior_failure" {
				professionPacket(t, s, opcode.SMSGCastFailed, []byte{9, 0x3e, 0x09, 0, 0, 121})
			}
			if mode == "final_world_foreign" {
				f.mu.Lock()
				f.worldHook = func(_ *worldv1.SnapshotRequest) {
					professionPacket(t, s, opcode.SMSGLootResponse, p)
				}
				f.mu.Unlock()
			} else {
				professionPacket(t, s, opcode.SMSGLootResponse, p)
			}
			wantStatus := professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
			if mode == "prior_failure" {
				wantStatus = professionsv1.AttemptStatus_SERVER_REJECTED
				if op.receipt.FailureCode != 121 || !strings.Contains(op.receipt.Reason, "121") {
					t.Fatal("foreign window overwrote definitive cast failure", op.receipt)
				}
			}
			if !s.stepHerb(op) || !op.windowInvalid || !herbCleanupPending(op) || op.receipt.Status != wantStatus {
				t.Fatal("contradictory window retained slot authority", op.receipt)
			}
			// A later response cannot re-arm this operation after ambiguity.
			professionPacket(t, s, opcode.SMSGLootResponse, herbBehaviorLoot(6, 1))
			// Slot/push messages have no loot GUID. Even matching old rows, and
			// an exact-old-GUID release, cannot repair lost current-window proof.
			op.stored[0], op.releaseReserved = true, true
			professionPacket(t, s, opcode.SMSGLootRemoved, []byte{0})
			push := professionPushReceipt(7, false, false, 2447, 2, 2)
			binary.LittleEndian.PutUint32(push[21:], 26)
			professionPacket(t, s, opcode.SMSGItemPushResult, push)
			professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(herbBehaviorTarget), 1))
			if op.receipt.LootSlotsRemoved != 0 || op.receipt.OwnLootedItemsObserved != 0 || op.receipt.LootReleaseObserved || op.removed[0] || op.pushed[0] {
				t.Fatal("foreign window inflated old receipt", op.receipt)
			}
			if err := s.herbCleanupWrite(context.Background(), op, opcode.CMSGLootRelease, releaseLootPacket(herbBehaviorTarget)); err == nil {
				t.Fatal("old target release remained authorized")
			}
			s.finishHerb(op)
			f.mu.Lock()
			packets := len(f.packets)
			f.mu.Unlock()
			if packets != 0 || !op.receipt.CleanupUnconfirmed || !op.receipt.LeaseReleased || op.receipt.Status != wantStatus {
				t.Fatal("ambiguous window produced writes", packets, op.receipt)
			}
		})
	}
}
