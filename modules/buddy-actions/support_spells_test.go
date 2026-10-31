package buddyactions

import (
	"context"
	"encoding/binary"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"testing"
	"time"
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
func TestActualPartyResurrectionOfferAcceptedOnceNotForgedOrUnsolicited(t *testing.T) {
	for _, caster := range []uint64{3, 99} {
		t.Run(string(rune(caster)), func(t *testing.T) {
			s, f, _ := rig(t)
			f.world.Self.Alive = false
			f.world.Self.Health = 0
			p := binary.LittleEndian.AppendUint64(nil, caster)
			p = binary.LittleEndian.AppendUint32(p, 1)
			p = append(p, 0, 0)
			for i := 0; i < 2; i++ {
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGResurrectRequest, Payload: p})
				s.acceptResurrectionLocked(context.Background())
			}
			want := 0
			if caster == 3 {
				want = 1
			}
			if countPackets(f, opcode.CMSGResurrectResponse) != want {
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
