package buddyinventory

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"testing"
)

func gearAuraFixture(t *testing.T) (*Service, *fakeInventory, *consumableTestSession) {
	t.Helper()
	s, f := fixtureGear(t, true)
	s.metadata[60001] = itemMetadata{onUseSpells: [5]uint32{430}}
	peer := &consumableTestSession{fakeInventory: f, auras: &actionsv1.PlayerAuras{TargetGuid: 6, Complete: true, Revision: 10}}
	for i := uint32(0); i < 5; i++ {
		peer.auras.Auras = append(peer.auras.Auras, &actionsv1.ObservedAura{Slot: i, SpellId: 430 + i, CasterGuid: 6, Positive: true, DurationKnown: true, RemainingMs: 60000, Revision: 10})
	}
	s.session = peer
	return s, f, peer
}

func TestInventoryCountdownReadThenEquipKeepsExactOutputAndCAS(t *testing.T) {
	s, f, peer := gearAuraFixture(t)
	r := gearRequest(t, s)
	for _, aura := range peer.auras.Auras {
		aura.RemainingMs -= 500
	}
	observed := stateInventory(t, s)
	if observed.Revision != r.ExpectedRevision {
		t.Fatal("elapsed countdown invalidated unchanged inventory CAS", r.ExpectedRevision, observed.Revision)
	}
	if observed.ConsumableContext.SelfAuras.Auras[0].RemainingMs != 59500 {
		t.Fatal("countdown output was frozen", observed.ConsumableContext)
	}
	for _, aura := range peer.auras.Auras {
		aura.RemainingMs -= 200
	}
	gearProof(s, f)
	if _, err := equipGearPolicyTest(t, s, r); err != nil {
		t.Fatal("real read/equip refused elapsed time", err)
	}
	confirmed := waitInventory(t, s)
	if confirmed.Operation.Status != "CONFIRMED" || !confirmed.Operation.EquipmentConfirmed || !confirmed.Operation.InventoryConfirmed || !confirmed.Operation.LeaseReleased {
		t.Fatal("equip lacked real receipt proof", confirmed)
	}
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGAutoequipItemSlot {
		t.Fatal("expected exactly one equip packet", f.packets)
	}
}

func TestInventoryAuraAndItemChangesStillInvalidateEquipCAS(t *testing.T) {
	changes := map[string]func(*Service, *fakeInventory, *consumableTestSession){
		"expiry":           func(_ *Service, _ *fakeInventory, p *consumableTestSession) { p.auras.Auras[0].RemainingMs = 0 },
		"removal":          func(_ *Service, _ *fakeInventory, p *consumableTestSession) { p.auras.Auras = p.auras.Auras[1:] },
		"refresh duration": func(_ *Service, _ *fakeInventory, p *consumableTestSession) { p.auras.Auras[0].RemainingMs++ },
		"refresh revision": func(_ *Service, _ *fakeInventory, p *consumableTestSession) {
			p.auras.Auras[0].Revision++
			p.auras.Auras[0].RemainingMs--
		},
		"set revision":     func(_ *Service, _ *fakeInventory, p *consumableTestSession) { p.auras.Revision++ },
		"spell":            func(_ *Service, _ *fakeInventory, p *consumableTestSession) { p.auras.Auras[0].SpellId++ },
		"caster":           func(_ *Service, _ *fakeInventory, p *consumableTestSession) { p.auras.Auras[0].CasterGuid++ },
		"unknown duration": func(_ *Service, _ *fakeInventory, p *consumableTestSession) { p.auras.Auras[0].DurationKnown = false },
		"unknown baseline": func(_ *Service, _ *fakeInventory, p *consumableTestSession) { p.auras.Complete = false },
		"new aura": func(_ *Service, _ *fakeInventory, p *consumableTestSession) {
			p.auras.Auras = append(p.auras.Auras, proto.Clone(p.auras.Auras[0]).(*actionsv1.ObservedAura))
			p.auras.Auras[5].Slot = 9
		},
		"duplicate slot": func(_ *Service, _ *fakeInventory, p *consumableTestSession) { p.auras.Auras[1].Slot = 0 },
		"inventory":      func(_ *Service, f *fakeInventory, _ *consumableTestSession) { f.w.InventoryRevision++ },
		"equipment":      func(_ *Service, f *fakeInventory, _ *consumableTestSession) { f.w.SelfEquipmentRevision++ },
		"actor":          func(_ *Service, f *fakeInventory, _ *consumableTestSession) { f.w.Self.Guid++ },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, peer := gearAuraFixture(t)
			r := gearRequest(t, s)
			change(s, f, peer)
			r.RequireConservativeUpgrade = false // Same native request after ordinary Lua comparison.
			if _, err := s.EquipGear(context.Background(), r); err == nil {
				t.Fatal("intervening change bypassed CAS", name)
			}
			if len(f.packets) != 0 || f.leases != 0 {
				t.Fatal("changed state wrote or reserved gear", f.packets, f.leases)
			}
		})
	}
}

func TestInventoryAuraOrderAndUnknownRevisionKeepTruthfulCAS(t *testing.T) {
	s, _, peer := gearAuraFixture(t)
	first := stateInventory(t, s)
	peer.auras.Auras[0], peer.auras.Auras[4] = peer.auras.Auras[4], peer.auras.Auras[0]
	next := stateInventory(t, s)
	if next.Revision != first.Revision || next.ConsumableContext.SelfAuras.Auras[0].Slot != 4 {
		t.Fatal("slot ordering changed facts or output was normalized", next)
	}
	for _, mode := range []string{"set", "row"} {
		t.Run(mode, func(t *testing.T) {
			s, _, peer := gearAuraFixture(t)
			if mode == "set" {
				peer.auras.Revision = 0
			} else {
				peer.auras.Auras[0].Revision = 0
			}
			first := stateInventory(t, s)
			peer.auras.Auras[0].RemainingMs--
			next := stateInventory(t, s)
			if next.Revision == first.Revision {
				t.Fatal("unknown aura revision treated as known elapsed time")
			}
		})
	}
}
