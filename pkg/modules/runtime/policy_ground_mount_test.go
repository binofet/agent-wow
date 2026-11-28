package modrt

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	buddyactions "github.com/agent-wow/agent-wow/modules/buddy-actions"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func groundMountAura(native *buddyactions.Service, guid uint64, id uint32) {
	p := append(spellFixturePackedGUID(guid), 8)
	p = binary.LittleEndian.AppendUint32(p, id)
	if id != 0 {
		p = append(p, 0x19, 40, 1)
	}
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: p})
}

func mountEncounterStep(t *testing.T, m *Manager, p *SessionPolicy) (*behavior.ActionReceipt, map[string]any) {
	t.Helper()
	ctx, seq := observeFriendlyEncounter(t, m, p)
	out, receipt, err := p.Step(ctx, map[string]any{"kind": "encounter", "observationSequence": seq}, nil)
	if err != nil {
		t.Fatal(out, err)
	}
	return receipt, out
}

func TestCompanionFollowsOwnersGroundMountAndDismount(t *testing.T) {
	for _, ambiguous := range []string{"", "mount", "dismount", "default_formation", "swimming_owner"} {
		m, native, cb := friendlyEncounterManager(t)
		me := cb.world.Self
		me.RunSpeed = 7
		me.SpeedsKnown = true
		owner := proto.CloneOf(me)
		owner.Guid = 3
		owner.X = 4
		owner.RunSpeed = 14
		cb.world.Owner = owner
		runtimeControlBook(t, native, 458, 23238, 32235, 60424)
		if ambiguous != "default_formation" && ambiguous != "swimming_owner" {
			if _, err := native.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
				t.Fatal(err)
			}
		}
		p, err := m.StartPolicy(context.Background(), followFallbackBundle(t))
		if err != nil {
			t.Fatal(err)
		}
		defer p.close()
		for i := 0; i < 2; i++ {
			mountEncounterStep(t, m, p)
		}
		if len(cb.writes) != 0 {
			t.Fatal("owner on foot", cb.writes)
		}
		owner.UnitFlags = movementflags.Mounted
		groundMountAura(native, 3, 23240)
		if ambiguous == "mount" || ambiguous == "dismount" {
			send := m.session.SendPacket
			m.session.SendPacket = func(ctx context.Context, code uint32, b []byte) error {
				if err := send(ctx, code, b); err != nil {
					return err
				}
				if ambiguous == "mount" && code == opcode.CMSGCastSpell || ambiguous == "dismount" && code == opcode.CMSGCancelMountAura {
					return errors.New("mount input delivery unknown")
				}
				return nil
			}
		}
		var receipt *behavior.ActionReceipt
		var out map[string]any
		for i := 0; i < 3 && len(cb.writes) == 0; i++ {
			receipt, out = mountEncounterStep(t, m, p)
		}
		if len(cb.writes) != 1 || cb.writes[0].Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(cb.writes[0].Payload[1:]) != 23238 {
			obs, _ := native.EncounterObservation(context.Background(), &emptypb.Empty{})
			t.Fatalf("no exact learned swift ground cast: receipt=%v output=%v mount=%v spell=%v", receipt, out, obs.GetNavigation().GetGroundMount(), obs.GetSpells()["23238"])
		}
		if _, err = p.Reload(context.Background(), followFallbackBundle(t)+"\n-- retained mount", p.vm.Status().Revision); err != nil {
			t.Fatal(err)
		}
		mountEncounterStep(t, m, p)
		if len(cb.writes) != 1 {
			t.Fatal("mount replay", cb.writes)
		}
		if ambiguous == "mount" {
			if receipt == nil || receipt.Outcome != behavior.ActionUnknown {
				t.Fatal(receipt)
			}
			continue
		}
		runtimeGenericGo(native, cb.writes[0].Payload[0], 23238, false)
		for i := 0; i < 2; i++ {
			mountEncounterStep(t, m, p)
		}
		view, _ := native.EncounterObservation(context.Background(), &emptypb.Empty{})
		if view.GetNavigation().GetGroundMount().GetEffectObserved() {
			t.Fatal("GO alone proved mounted")
		}
		me.RunSpeed = 14
		me.UnitFlags = movementflags.Mounted
		groundMountAura(native, me.Guid, 23238)
		mountEncounterStep(t, m, p)
		view, _ = native.EncounterObservation(context.Background(), &emptypb.Empty{})
		if !view.GetNavigation().GetGroundMount().GetEffectObserved() {
			t.Fatal(view.Navigation.GroundMount)
		}
		owner.UnitFlags = 0
		owner.RunSpeed = 7
		if ambiguous == "swimming_owner" {
			owner.MovementFlags = 0x00200001
			mountEncounterStep(t, m, p)
			if len(cb.writes) != 1 {
				t.Fatal("owner's swimming flag and mounted-bit clear cannot prove aura removal", cb.writes)
			}
		}
		groundMountAura(native, 3, 0)
		for i := 0; i < 3 && len(cb.writes) == 1; i++ {
			receipt, out = mountEncounterStep(t, m, p)
		}
		if len(cb.writes) != 2 || cb.writes[1].Opcode != opcode.CMSGCancelMountAura || len(cb.writes[1].Payload) != 0 {
			t.Fatal("ordinary dismount", receipt, out, cb.writes)
		}
		if receipt == nil || ambiguous == "dismount" && receipt.Outcome != behavior.ActionUnknown || ambiguous != "dismount" && receipt.Outcome != behavior.ActionSettledUnverified {
			t.Fatal("dismount transport", ambiguous, receipt)
		}
		if _, err = p.Reload(context.Background(), followFallbackBundle(t)+"\n-- retained dismount", p.vm.Status().Revision); err != nil {
			t.Fatal(err)
		}
		mountEncounterStep(t, m, p)
		if len(cb.writes) != 2 {
			t.Fatal("dismount replay", cb.writes)
		}
		me.UnitFlags = 0
		me.RunSpeed = 7
		groundMountAura(native, me.Guid, 0)
		for i := 0; i < 2; i++ {
			mountEncounterStep(t, m, p)
		}
		view, _ = native.EncounterObservation(context.Background(), &emptypb.Empty{})
		if p.vm.Status().Pending != 0 || view.GetNavigation().GetGroundMount().GetEffectObserved() != (ambiguous != "dismount") || view.GetNavigation().GetGroundMount().LastPhase != "observed_ground_mount_state" {
			t.Fatal(p.vm.Status(), view.GetNavigation().GetGroundMount())
		}
	}
}
