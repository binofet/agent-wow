package buddyinventory

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type enchantFixture struct {
	name                                                                             string
	supply, spell, enchant, enchantSlot, target, equipSlot, class, subclass, invType uint32
	charges                                                                          int32
}

func TestTypedEnhancementResolvesALateOilEffectWithoutAnotherUse(t *testing.T) {
	tc := partyEnchants[0]
	s, f, q := enhancementFixture(t, tc)
	// First application: permanent enchanted, temporary known zero and absent
	// from the sparse item fields. The source oil has multiple charges.
	f.w.SelfEquipment[q.EquipmentSlot].EnhancementDetails.Enchantments = []*worldv1.ItemEnchantmentSlot{{Id: 2669}}
	q.ExpectedEnchantmentId = proto.Uint32(0)
	// The carried source of a previous drink was already consumed. A missed
	// historical drink aura cannot keep custody of a new equipment operation.
	s.lastConsumable = &consumableAttempt{view: &inventoryv1.ConsumableUse{Source: &inventoryv1.ItemRef{ItemGuid: 0x4000000000000055, Entry: 1645, StackCount: 1},
		TransportAttempted: true, InventoryConfirmed: true, Phase: "transport_written_awaiting_observation"}}
	f.sendError = true
	written := make(chan struct{})
	f.afterSend = func(packet *modv1.SendPacketRequest) {
		if packet.Opcode == opcode.CMSGUseItem {
			close(written)
		}
	}
	listener := bufconn.Listen(1 << 16)
	server := grpc.NewServer()
	inventoryv1.RegisterBuddyInventoryServer(server, s)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	conn, err := grpc.NewClient("passthrough:///inventory", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := inventoryv1.NewBuddyInventoryClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	st, err := c.GetState(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	q.ExpectedRevision = st.Revision
	if _, err = c.ApplyItemEnhancement(ctx, q); err != nil {
		t.Fatal(err)
	}
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	s.mu.Lock()
	s.active.cancel()
	s.mu.Unlock()
	st = waitInventory(t, s)
	if st.Operation.Status != "OUTCOME_UNCONFIRMED" || st.Operation.TransportWritten {
		t.Fatal(st.Operation)
	}
	f.mu.Lock()
	target := f.w.SelfEquipment[q.EquipmentSlot]
	target.EnhancementDetails.Enchantments = append(target.EnhancementDetails.Enchantments, &worldv1.ItemEnchantmentSlot{Slot: 1, Id: 2627, DurationRaw: 3600000})
	f.w.SelfEquipmentRevision++
	f.mu.Unlock()
	st, err = c.GetState(ctx, &emptypb.Empty{})
	if err != nil || st.Operation.Status != "CONFIRMED" || !st.Operation.Enhancement.EffectConfirmed || st.Operation.InventoryConfirmed || st.Operation.TransportWritten {
		t.Fatalf("late target evidence %v %v", st.GetOperation(), err)
	}
	if _, err = c.ApplyItemEnhancement(ctx, q); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	uses := 0
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGUseItem {
			uses++
		}
	}
	if uses != 1 {
		t.Fatalf("%d item uses", uses)
	}
}

var partyEnchants = []enchantFixture{
	{"Wizard Oil on staff", 20750, 25121, 2627, 1, 873, 15, 2, 10, 17, -5},
	{"Instant Poison IV on main hand", 8926, 11338, 623, 1, 2164, 15, 2, 15, 13, -1},
	{"Deadly Poison II on off hand", 2893, 2824, 8, 1, 44217, 16, 2, 15, 13, -1},
	{"Dexterity on boots", 37603, 27951, 2657, 0, 14549, 7, 4, 4, 8, -1},
	{"Major Stamina on shield", 38945, 34009, 1071, 0, 1169, 16, 4, 6, 14, -1},
}

func enhancementFixture(t *testing.T, tc enchantFixture) (*Service, *fakeInventory, *inventoryv1.ApplyItemEnhancementRequest) {
	t.Helper()
	s, f := fixtureGear(t, false)
	f.w.VisitRevision = 7
	f.a.SessionId, f.a.SelfGuid = "body", 6
	f.w.Self.Level = 44
	// The raw realm item query owns the item-use spell and signed charges.
	raw := metadataFixture(tc.supply, 0, 1, 0, 0)
	prefix := 16 + len("Fixture") + 4
	binary.LittleEndian.PutUint32(raw[prefix+7*4:], ^uint32(0))
	binary.LittleEndian.PutUint32(raw[prefix+8*4:], ^uint32(0))
	if tc.supply == 8926 || tc.supply == 2893 {
		binary.LittleEndian.PutUint32(raw[prefix+7*4:], 8)
	}
	spellAt := prefix + 88 + 8 + 24 + 28 + 12
	binary.LittleEndian.PutUint32(raw[spellAt:], tc.spell)
	binary.LittleEndian.PutUint32(raw[spellAt+8:], uint32(tc.charges))
	m, err := parseItemMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	s.metadata[tc.supply], s.queried[tc.supply] = m, true
	s.metadata[tc.target], s.queried[tc.target] = itemMetadata{known: true, entry: tc.target, class: tc.class, subclass: tc.subclass, inventoryType: tc.invType}, true
	source := f.w.InventoryBags[0].Slots[0]
	source.Entry = tc.supply
	source.StackCount = 3
	if tc.charges < -1 {
		source.StackCount = 1
	}
	target := &worldv1.EquipmentSlotState{SlotKnown: true, Slot: tc.equipSlot, ItemGuid: 0x4000000000001122, Entry: tc.target, MetadataKnown: true, StackCount: 1, EnhancementsKnown: true, HasEnhancements: true,
		EnhancementDetails: &worldv1.ItemEnhancementState{IdKnownMask: 4095, DurationKnownMask: 4095, ChargesKnownMask: 4095,
			Enchantments: []*worldv1.ItemEnchantmentSlot{{Slot: 0, Id: 2669}, {Slot: 1, Id: 22, DurationRaw: 3000}}}}
	f.w.SelfEquipment[tc.equipSlot] = target
	st := stateInventory(t, s)
	var use *inventoryv1.ItemEnhancementUse
	for _, item := range st.Items {
		if item.Item.ItemGuid == source.ItemGuid {
			use = item.EnhancementUse
		}
	}
	if st.WorldVisitRevision != 7 || use.GetSpellId() != tc.spell || use.GetEnchantmentId() != tc.enchant || use.GetEnchantmentSlot() != tc.enchantSlot {
		t.Fatalf("actual source enhancement discovery: %v, visit %d", use, st.WorldVisitRevision)
	}
	old, _ := enchantmentAt(target.EnhancementDetails, tc.enchantSlot)
	return s, f, &inventoryv1.ApplyItemEnhancementRequest{SessionId: "body", SelfGuid: 6, RequestId: "test_enhance", VisitRevision: 7, ExpectedRevision: st.Revision,
		ExpectedInventoryRevision: st.InventoryRevision, Source: ref(source), SpellId: tc.spell, ExpectedEquipmentRevision: st.EquipmentRevision, EquipmentSlot: tc.equipSlot,
		EquippedItemGuid: target.ItemGuid, EquippedEntry: tc.target, ExpectedEnchantmentId: proto.Uint32(old.Id)}
}

func TestApplyCarriedEnhancementToExactEquippedItem(t *testing.T) {
	for _, tc := range partyEnchants {
		t.Run(tc.name, func(t *testing.T) {
			s, f, q := enhancementFixture(t, tc)
			f.afterSend = func(p *modv1.SendPacketRequest) {
				if p.Opcode != opcode.CMSGUseItem {
					return
				}
				wire := p.Payload
				if len(wire) != 28 || wire[0] != 255 || wire[1] != 23 || wire[2] == 0 || binary.LittleEndian.Uint32(wire[3:]) != tc.spell ||
					binary.LittleEndian.Uint64(wire[7:]) != q.Source.ItemGuid || !bytes.Equal(wire[15:24], []byte{0, 0, 0, 0, 0, 16, 0, 0, 0}) || !bytes.Equal(wire[24:], []byte{0x83, 0x22, 0x11, 0x40}) {
					t.Errorf("unexpected ordinary item-target packet: %x", wire)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				f.w.SelfEquipment[tc.equipSlot].EnhancementDetails.Enchantments[tc.enchantSlot].Id = tc.enchant
				f.w.SelfEquipmentRevision++
				if tc.charges == -1 {
					f.w.InventoryBags[0].Slots[0].StackCount--
					f.w.InventoryRevision++
				}
			}
			_, e := s.ApplyItemEnhancement(context.Background(), q)
			if e != nil {
				t.Fatal(e)
			}
			st := waitInventory(t, s)
			op := st.Operation
			if op.Status != "CONFIRMED" || !op.Enhancement.EffectConfirmed || !op.EquipmentConfirmed || op.GetEnhancement().GetObservedEnchantmentId() != tc.enchant || op.InventoryConfirmed != (tc.charges == -1) || !op.TransportWritten || !op.LeaseReleased || st.TransportWrites != 1 {
				t.Fatal(op)
			}
			repeat, e := s.ApplyItemEnhancement(context.Background(), q)
			if e != nil || repeat.TransportWrites != 1 || repeat.Operation.Status != "CONFIRMED" {
				t.Fatalf("replayed exact operation: %v %v", repeat, e)
			}
			modified := proto.Clone(q).(*inventoryv1.ApplyItemEnhancementRequest)
			modified.EquippedItemGuid++
			if _, e = s.ApplyItemEnhancement(context.Background(), modified); e == nil {
				t.Fatal("reused request ID with changed target")
			}
		})
	}
}

func TestEnhancementScopeAndUnknownEvidence(t *testing.T) {
	for _, mode := range []string{"wrong visit", "target replaced at renewal", "source missing at renewal", "incompatible target", "unexpected old enchant", "unknown other enchant", "wrong source spell", "wrong body class", "unresolved prior consumable", "uncertain callback", "wrong enchant observed", "unrelated permanent changed", "wrong target after write", "no decrement for single-use", "different visit after write", "same temp not refreshed", "same temp refreshed", "matching realm failure"} {
		t.Run(mode, func(t *testing.T) {
			tc := partyEnchants[1]
			s, f, q := enhancementFixture(t, tc)
			switch mode {
			case "wrong visit":
				q.VisitRevision++
			case "target replaced at renewal":
				f.afterRenew = func() { f.mu.Lock(); f.w.SelfEquipment[tc.equipSlot].ItemGuid++; f.mu.Unlock() }
			case "source missing at renewal":
				f.afterRenew = func() { f.mu.Lock(); f.w.InventoryBags[0].Slots[0].StackCount--; f.mu.Unlock() }
			case "incompatible target":
				m := s.metadata[tc.target]
				m.class = 4
				s.metadata[tc.target] = m
			case "unexpected old enchant":
				q.ExpectedEnchantmentId = proto.Uint32(0)
			case "unknown other enchant":
				f.w.SelfEquipment[tc.equipSlot].EnhancementDetails.IdKnownMask = 2
			case "wrong source spell":
				q.SpellId = 25121
			case "wrong body class":
				f.w.Self.ClassId = 8
			case "unresolved prior consumable":
				s.lastConsumable = &consumableAttempt{view: &inventoryv1.ConsumableUse{Source: &inventoryv1.ItemRef{ItemGuid: 0x4000000000000055, Entry: 1645, StackCount: 2},
					TransportAttempted: true, Phase: "transport_unconfirmed_no_retry"}}
			case "uncertain callback":
				f.sendError = true
			case "same temp not refreshed", "same temp refreshed":
				f.w.SelfEquipment[tc.equipSlot].EnhancementDetails.Enchantments[1].Id = tc.enchant
				q.ExpectedEnchantmentId = proto.Uint32(tc.enchant)
			}
			q.ExpectedRevision = stateInventory(t, s).Revision
			wrote := make(chan struct{})
			f.afterSend = func(p *modv1.SendPacketRequest) {
				if p.Opcode != opcode.CMSGUseItem {
					return
				}
				defer close(wrote)
				if mode == "matching realm failure" {
					raw := binary.LittleEndian.AppendUint32([]byte{p.Payload[2] - 1}, tc.spell)
					raw = append(raw, 51)
					s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: raw})
					if s.snapshot().Operation.ServerErrorObserved {
						t.Error("foreign cast count accepted")
					}
					raw[0] = p.Payload[2]
					s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: raw})
					return
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if mode == "uncertain callback" {
					return
				}
				target := f.w.SelfEquipment[tc.equipSlot]
				target.EnhancementDetails.Enchantments[1].Id = tc.enchant
				f.w.SelfEquipmentRevision++
				if mode != "no decrement for single-use" {
					f.w.InventoryBags[0].Slots[0].StackCount--
					f.w.InventoryRevision++
				}
				switch mode {
				case "unrelated permanent changed":
					target.EnhancementDetails.Enchantments[0].Id++
				case "wrong enchant observed":
					target.EnhancementDetails.Enchantments[1].Id++
				case "different visit after write":
					f.w.VisitRevision++
				case "wrong target after write":
					target.ItemGuid++
				case "same temp refreshed":
					target.EnhancementDetails.Enchantments[1].DurationRaw += 1000
				}
			}
			_, err := s.ApplyItemEnhancement(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "wrong visit", "target replaced at renewal", "source missing at renewal", "incompatible target", "unexpected old enchant", "unknown other enchant", "wrong source spell", "wrong body class", "unresolved prior consumable":
				st := waitInventory(t, s)
				if st.Operation.Status != "BLOCKED" || st.Operation.TransportAttempted {
					t.Fatal(st.Operation)
				}
				return
			}
			select {
			case <-wrote:
			case <-time.After(3 * time.Second):
				t.Fatal("enhancement was never written", s.snapshot().Operation)
			}
			if mode == "matching realm failure" || mode == "same temp refreshed" {
				st := waitInventory(t, s)
				if mode == "matching realm failure" && (st.Operation.Status != "SERVER_REJECTED" || st.Operation.ServerError != 51) || mode == "same temp refreshed" && (st.Operation.Status != "CONFIRMED" || !st.Operation.Enhancement.EffectConfirmed) {
					t.Fatal(st.Operation)
				}
				return
			}
			s.GetState(context.Background(), nil)
			s.mu.Lock()
			if s.active != nil {
				s.active.cancel()
			}
			s.mu.Unlock()
			st := waitInventory(t, s)
			if st.Operation.Status != "OUTCOME_UNCONFIRMED" || st.Operation.Enhancement.EffectConfirmed {
				t.Fatal(st.Operation)
			}
			repeat, err := s.ApplyItemEnhancement(context.Background(), q)
			if err != nil || repeat.Operation.Status != "OUTCOME_UNCONFIRMED" || repeat.TransportWrites != st.TransportWrites {
				t.Fatalf("uncertain request retried %v %v", repeat, err)
			}
			q.RequestId = "another"
			q.ExpectedRevision = st.Revision
			if _, err = s.ApplyItemEnhancement(context.Background(), q); err == nil {
				t.Fatal("unresolved write allowed replacement")
			}
		})
	}
}

func TestEnhancementWorldBoundaryRetainsOriginalAttempt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		boundary uint32
	}{{"transfer pending", opcode.SMSGTransferPending}, {"same-map entry", opcode.SMSGNewWorld}, {"login verification", opcode.SMSGLoginVerifyWorld}} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, q := enhancementFixture(t, partyEnchants[0])
			f.afterSend = func(p *modv1.SendPacketRequest) {
				if p.Opcode != opcode.CMSGUseItem {
					return
				}
				body := make([]byte, 20)
				if tc.boundary == opcode.SMSGTransferPending {
					body = body[:4]
				}
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: tc.boundary, Payload: body})
				// The ordered boundary has arrived, but the other module still
				// returns its old visit. Neither a matching error nor a newer
				// item field from this ambiguous interval proves the outcome.
				failure := make([]byte, 18)
				failure[0] = 4
				binary.LittleEndian.PutUint64(failure[1:], q.Source.ItemGuid)
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInventoryChangeFailure, Payload: failure})
				cast := append(binary.LittleEndian.AppendUint32([]byte{p.Payload[2]}, q.SpellId), 51)
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: cast})
				f.mu.Lock()
				f.w.SelfEquipment[q.EquipmentSlot].EnhancementDetails.Enchantments[1].Id = 2627
				f.w.SelfEquipmentRevision++
				f.mu.Unlock()
			}
			if _, err := s.ApplyItemEnhancement(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			observation := waitInventory(t, s)
			op := observation.Operation
			if !op.TransportAttempted || op.Status != "OUTCOME_UNCONFIRMED" || op.ServerErrorObserved || op.Enhancement.EffectConfirmed {
				t.Fatal(op)
			}
			q.RequestId = "replacement_after_boundary"
			q.ExpectedRevision = observation.Revision
			if _, err := s.ApplyItemEnhancement(context.Background(), q); err == nil {
				t.Fatal("new request bypassed retained attempt")
			}
		})
	}
}
