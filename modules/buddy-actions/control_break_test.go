package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type refusedControlSession struct{ *fakeSession }

func (f refusedControlSession) SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	return nil, modcb.PacketNotDispatchedError("exact core admission refused before transport")
}

func TestControlBreakNativeTickNeverSelectsLearnedRankOrTrinket(t *testing.T) {
	s, f, _ := buffRig(t, 1, humanBreakSpell, 7744, 18499)
	f.world.Self.UnitFlags |= fleeingUnitFlag
	s.clientControlLost = true
	applyTestFear(s, 7399)
	s.Tick(context.Background())
	if len(f.packets) != 0 {
		t.Fatal("native Tick still chooses fear break", f.packets)
	}
	obs := s.controlBreakObservationLocked(f.world)
	if len(obs.Options) != 3 || len(obs.FearAuras) != 1 || !obs.FleeingKnown || !obs.Fleeing {
		t.Fatal(obs)
	}
	for _, option := range obs.Options {
		if !option.Ready {
			t.Fatal("server control made instant self break unavailable", option)
		}
	}
}

func TestControlBreakExactAuraBodyChannelAndCustodyGuards(t *testing.T) {
	for _, mode := range []string{"body", "session", "aura_revision", "aura_spell", "aura_slot", "unlearned", "not_fleeing", "not_alive", "channel", "partial_channel", "cast_pending", "body_motion", "interaction", "expired_aura"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := buffRig(t, 1, humanBreakSpell)
			applyTestFear(s, 7399)
			f.world.Self.UnitFlags |= fleeingUnitFlag
			r := exactFearRequest(s, humanBreakSpell)
			switch mode {
			case "body":
				r.SelfGuid++
			case "session":
				r.SessionId = "old-body"
			case "aura_revision":
				r.AuraRevision++
			case "aura_spell":
				r.AuraSpellId++
			case "aura_slot":
				r.AuraSlot++
			case "unlearned":
				r.SpellId = 7744
			case "not_fleeing":
				f.world.Self.UnitFlags = 0
			case "not_alive":
				f.world.Self.Alive = false
			case "channel":
				f.world.Self.ChannelSpellId = 15407
			case "partial_channel":
				f.world.Self.ChannelObjectKnown = false
			case "cast_pending":
				s.state.CastPending = true
			case "body_motion":
				s.bodyMoving = true
			case "interaction":
				s.state.InteractionOwner = "trade"
				s.interactionUntil = s.now().Add(time.Minute)
			case "expired_aura":
				advance(8 * time.Second)
			}
			if _, err := s.BreakControl(context.Background(), r); err == nil || len(f.packets) != 0 {
				t.Fatal("unsafe exact control break", mode, err, f.packets)
			}
		})
	}
}

func TestControlBreakTypedNoWriteRearmsButUnknownWaitsForObservedApplication(t *testing.T) {
	s, f, advance := buffRig(t, 1, humanBreakSpell)
	applyTestFear(s, 7399)
	f.world.Self.UnitFlags |= fleeingUnitFlag
	r := exactFearRequest(s, humanBreakSpell)
	s.session = refusedControlSession{f}
	if _, err := s.BreakControl(context.Background(), r); !modcb.PacketWasNotDispatched(err) || len(f.packets) != 0 || s.state.CastPending || len(s.controlBreakBindings) != 0 {
		t.Fatal("known no-write consumed source", err, s.stateLocked())
	}
	s.session = f
	f.failSend = true
	if _, err := s.BreakControl(context.Background(), r); err == nil || len(f.packets) != 1 || !s.state.CastPending {
		t.Fatal("unknown write not reserved", err, s.stateLocked())
	}
	advance(4 * time.Second)
	s.supportSnapshotLocked(f.world)
	if s.state.CastPending || s.supportOp.view.Phase != "outcome_unconfirmed_no_retry" || s.controlBreakBindings[r.AuraSlot].spell != r.AuraSpellId {
		t.Fatal("unknown cast either global-pending forever or replayable", s.stateLocked(), s.supportOp)
	}
	if _, err := s.BreakControl(context.Background(), r); err == nil || len(f.packets) != 1 {
		t.Fatal("replayed same unknown application", err, f.packets)
	}
	// Losing an aura cache is not positive removal evidence. Retain native
	// no-replay even though there is currently no eligible aura to propose.
	saved := s.buffAuras
	s.buffAuras = nil
	s.controlBreakObservationLocked(f.world)
	if len(s.controlBreakBindings) != 1 || s.supportOp.view.Phase != "outcome_unconfirmed_no_retry" {
		t.Fatal("unavailable aura cache fabricated clearance", s.supportOp)
	}
	s.buffAuras = saved
	// A same-slot/caster refresh has a new raw revision but is not proof that
	// the uncertain prior application ended.
	applyTestFear(s, 7399)
	refreshed := exactFearRequest(s, humanBreakSpell)
	projected := s.controlBreakObservationLocked(f.world)
	if refreshed.AuraRevision == r.AuraRevision || len(projected.AttemptedAuraRevisions) != 1 || projected.AttemptedAuraRevisions[0] != refreshed.AuraRevision {
		t.Fatal("refreshed active aura lost consumed proof", projected)
	}
	if _, err := s.BreakControl(context.Background(), refreshed); err == nil || len(f.packets) != 1 {
		t.Fatal("refresh reopened unknown fear break", err, f.packets)
	}
	applyTestFear(s, 0)
	if len(s.controlBreakBindings) != 0 {
		t.Fatal("actual removal packet was not retained between policy samples")
	}
	s.controlBreakObservationLocked(f.world)
	if s.supportOp.view.EffectObserved || s.supportOp.view.ServerCastGo || s.supportOp.view.Phase != "observed_control_clear_effect_unverified" || len(s.controlBreakBindings) != 0 {
		t.Fatal("observed clearance claimed own success", s.supportOp)
	}
	advance(2 * time.Minute)
	applyTestFear(s, 7399)
	r = exactFearRequest(s, humanBreakSpell)
	f.failSend = false
	if _, err := s.BreakControl(context.Background(), r); err != nil || len(f.packets) != 2 {
		t.Fatal("new application not usable", err, f.packets)
	}
}

func TestControlBreakExactTrinketIdentityAndSharedCooldown(t *testing.T) {
	s, f, _ := buffRig(t, 9)
	applyTestFear(s, 7399)
	f.world.Self.UnitFlags |= fleeingUnitFlag
	f.world.SelfEquipmentKnown = true
	f.world.SelfEquipment = []*worldv1.EquipmentSlotState{{Slot: 13, SlotKnown: true, MetadataKnown: true, ItemGuid: 0x4000000000012345, Entry: 18854, StackCount: 1}}
	r := exactFearRequest(s, pvpBreakSpell)
	r.ItemGuid = f.world.SelfEquipment[0].ItemGuid
	r.ItemSlot = 13
	for _, mutate := range []func(*actionsv1.BreakControlRequest){func(r *actionsv1.BreakControlRequest) { r.ItemGuid++ }, func(r *actionsv1.BreakControlRequest) { r.ItemSlot = 14 }, func(r *actionsv1.BreakControlRequest) { r.SpellId = humanBreakSpell }} {
		bad := proto.Clone(r).(*actionsv1.BreakControlRequest)
		mutate(bad)
		if _, err := s.BreakControl(context.Background(), bad); err == nil || len(f.packets) != 0 {
			t.Fatal("foreign item admitted", bad, err)
		}
	}
	if _, err := s.BreakControl(context.Background(), r); err != nil || len(f.packets) != 1 {
		t.Fatal(err, f.packets)
	}
	if !s.fearBreakReady.After(s.now()) {
		t.Fatal("server shared trinket cooldown lost")
	}
}
