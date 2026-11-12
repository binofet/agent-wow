package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

func resRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	s, f, advance := rig(t)
	s.setKnownSpell(2006, true)
	s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
	f.world.Owner.Alive = false
	f.world.Owner.Health = 0
	f.world.Owner.UnitFlags &^= 0x80000
	f.world.Self.Power = 1000
	f.world.Self.MaxPower = 1000
	f.world.Self.BaseManaKnown = true
	f.world.Self.BaseMana = 100
	return s, f, advance
}
func TestResurrectionUsesLearnedEffectCurrentDeadPartyAndNoReceiptAsLife(t *testing.T) {
	s, f, advance := resRig(t)
	s.Tick(context.Background())
	if lastCast(f) != 2006 || s.supportOp == nil || s.supportOp.owner != 3 || s.supportOp.view.EffectObserved {
		t.Fatal("missing genuine resurrection", &s.state, s.supportOp)
	}
	p := smiteEvent(true, s.castCount, 3)
	_, _, _, off, _ := castEvent(p)
	binary.LittleEndian.PutUint32(p[off-4:], 2006)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: p})
	if s.state.CastPending || !s.supportOp.view.ServerCastGo || s.supportOp.view.EffectObserved || f.world.Owner.Alive {
		t.Fatal("GO claimed revival", s.supportOp)
	}
	advance(20 * time.Second)
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("same death replayed")
	}
	f.world.Owner.Alive = true
	f.world.Owner.Health = 100
	s.Tick(context.Background())
	if !s.supportOp.view.EffectObserved || s.supportOp.view.Phase != "confirmed_alive" {
		t.Fatal(s.supportOp)
	}
}
func TestResurrectionRefusesCombatNonmembersUnlearnedAndOutOfRange(t *testing.T) {
	for _, mode := range []string{"combat", "stranger", "unlearned", "range", "mana"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := resRig(t)
			switch mode {
			case "combat":
				f.world.Self.UnitFlags |= 0x80000
			case "stranger":
				f.party = nil
			case "unlearned":
				s.setKnownSpell(2006, false)
			case "range":
				f.world.Owner.X = 200
			case "mana":
				f.world.Self.Power = 1
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("unsafe res", &s.state)
			}
		})
	}
}

func TestDefiniteResurrectionRejectionAllowsFreshSelectionAfterCooldown(t *testing.T) {
	s, f, advance := resRig(t)
	s.Tick(context.Background())
	first := s.castCount
	p := []byte{first}
	p = binary.LittleEndian.AppendUint32(p, 2006)
	p = append(p, 47)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: p})
	if s.resurrectionBindings[3] || s.state.CastPending {
		t.Fatal("definite rejection quarantined the death")
	}
	advance(9 * time.Second)
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("rejection cooldown ignored")
	}
	advance(2 * time.Second)
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 2 || s.castCount == first {
		t.Fatal("fresh eligible resurrection could not start")
	}
	s, f, _ = resRig(t)
	f.failSend = true
	s.Tick(context.Background())
	if !s.resurrectionBindings[3] || s.supportOp.view.Phase != "transport_unconfirmed_no_retry" {
		t.Fatal("ambiguous transport was rearmed")
	}
}
func TestActualPartyResurrectionOfferAcceptedOnceNotForgedOrUnsolicited(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caster uint64
		tail   []byte
		want   int
	}{
		{name: "legacy", caster: 3, tail: []byte{0, 0}, want: 1},
		{name: "legacy timer", caster: 3, tail: []byte{0, 0, 0, 0, 0, 0}, want: 1},
		{name: "azerothcore", caster: 3, tail: []byte{0, 0, 0}, want: 1},
		{name: "azerothcore timer", caster: 3, tail: []byte{0, 0, 0, 0, 0, 0, 0}, want: 1},
		{name: "stranger", caster: 99, tail: []byte{0, 0, 0}},
		{name: "creature", caster: 0xf130000000000003, tail: []byte{0, 0, 1}},
		{name: "invalid padding", caster: 3, tail: []byte{0, 1, 0}},
		{name: "invalid sickness", caster: 3, tail: []byte{0, 0, 2}},
		{name: "trailing bytes", caster: 3, tail: []byte{0, 0, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := rig(t)
			f.world.Self.Alive = false
			f.world.Self.Health = 0
			p := binary.LittleEndian.AppendUint64(nil, tc.caster)
			p = binary.LittleEndian.AppendUint32(p, 1)
			p = append(p, tc.tail...)
			for i := 0; i < 2; i++ {
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGResurrectRequest, Payload: p})
				s.Tick(context.Background())
			}
			if countPackets(f, opcode.CMSGResurrectResponse) != tc.want {
				t.Fatal("offer acceptance mismatch", f.packets)
			}
		})
	}
}
func TestLearnedUnfamiliarHealAdaptsWithoutIDOrClassBranch(t *testing.T) {
	s, f, _ := rig(t)
	c, err := spellcatalog.New([]byte("{\"id\":987654,\"family\":987654,\"rank\":1,\"name\":\"New support\",\"timingKnown\":true,\"rangeKnown\":true,\"itemClass\":-1,\"maxFriendly\":40,\"cost\":5,\"effects\":[{\"kind\":10,\"a\":21,\"points\":500}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	s.spellData = c
	s.setKnownSpell(987654, true)
	s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
	f.world.Owner.Health = 30
	s.Tick(context.Background())
	if lastCast(f) != 987654 {
		t.Fatal("new learned heal did not adapt", &s.state)
	}
	s.setKnownSpell(987654, false)
	if len(s.candidatesLocked(spellcatalog.Heal)) != 0 {
		t.Fatal("removed spell remained usable")
	}
}
