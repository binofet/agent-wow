package buddyactions

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func supportPolicyContext() context.Context {
	ctx := modcb.WithPolicySpell(context.Background(), strings.Repeat("a", 64))
	md, _ := metadata.FromOutgoingContext(ctx)
	return metadata.NewIncomingContext(context.Background(), md)
}

func supportPolicyInterruptRig(t *testing.T) (*Service, *fakeSession, *worldv1.CreatureState, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	learnSpells(s, 1766)
	f.world.Self.ClassId, f.world.Self.PowerType, f.world.Self.Power = 4, 3, 100
	f.world.Self.UnitFlags = 0x80000
	f.world.Owner.Alive, f.world.Owner.Health = false, 0
	enemy := encounterTestCreature(0xf130000000000001, 0)
	enemy.UnitFlags = 0x80000
	enemy.ActiveCast = &worldv1.CreatureCast{SpellId: 12039, RemainingMs: 1000, Revision: 7}
	f.world.NearbyCreatures = []*worldv1.CreatureState{enemy}
	f.lookupCreatures = map[uint64]*worldv1.CreatureState{enemy.Guid: enemy}
	return s, f, enemy, advance
}

func TestPolicyInterruptExactCastDoesNotDependOnLivingOwnerOrToggle(t *testing.T) {
	s, f, enemy, advance := supportPolicyInterruptRig(t)
	s.UseHostStandingHealing()
	activateEncounterWriter(t, s, f)
	w, err := s.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	view := s.encounterSupportLocked(context.Background(), w, s.onlineHealingGroup(f.party.Group))
	if len(view.Interrupts) != 1 || !view.Interrupts[0].Ready || !view.Interrupts[0].Healing || view.Interrupts[0].CastRevision != 7 {
		t.Fatal("learned interrupt was hidden by dead owner/default toggle", view)
	}
	req := &actionsv1.InterruptCastRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: enemy.Guid, SpellId: 1766, CastRevision: 7}
	ctx := modcb.WithPolicySpell(context.Background(), strings.Repeat("a", 64))
	ctx = modcb.WithPolicyEncounterAccess(ctx, testEncounterKey, 1)
	md, _ := metadata.FromOutgoingContext(ctx)
	ctx = metadata.NewIncomingContext(context.Background(), md)
	st, err := s.InterruptCast(ctx, req)
	if err != nil || !st.GetCastPending() || lastCast(f) != 1766 || countPackets(f, opcode.CMSGCastSpell) != 1 || s.supportOp.policySpellToken == "" {
		t.Fatal("exact Lua-selected interrupt did not dispatch", st, err, f.packets)
	}
	advance(3 * time.Second)
	s.expireExplicitCastLocked()
	if _, err := s.InterruptCast(supportPolicyContext(), req); status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("same cast revision replayed", err)
	}
}

func TestPolicySupportRecoversAfterFearAndChannelWithoutRearmingModes(t *testing.T) {
	for _, mode := range []string{"fear", "channel"} {
		t.Run(mode, func(t *testing.T) {
			s, f, enemy, _ := supportPolicyInterruptRig(t)
			if mode == "fear" {
				f.world.Self.UnitFlags |= 0x800000
			} else {
				f.world.Self.ChannelSpellId = 12051
			}
			w, _ := s.snapshot(context.Background())
			if view := s.encounterSupportLocked(context.Background(), w, nil); len(view.Interrupts) != 0 {
				t.Fatal("cast while physically controlled", view)
			}
			f.world.Self.UnitFlags &^= 0x800000
			f.world.Self.ChannelSpellId = 0
			w, _ = s.snapshot(context.Background())
			if view := s.encounterSupportLocked(context.Background(), w, nil); len(view.Interrupts) != 1 || !view.Interrupts[0].Ready {
				t.Fatal("support did not resume after control ended", view)
			}
			_, err := s.InterruptCast(supportPolicyContext(), &actionsv1.InterruptCastRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: enemy.Guid, SpellId: 1766, CastRevision: 7})
			if err != nil || lastCast(f) != 1766 {
				t.Fatal("recovered body did not interrupt", err)
			}
		})
	}
}

func TestPolicyInterruptRejectsChangedCastWithoutSubstitution(t *testing.T) {
	s, f, enemy, _ := supportPolicyInterruptRig(t)
	_, err := s.InterruptCast(supportPolicyContext(), &actionsv1.InterruptCastRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: enemy.Guid, SpellId: 1766, CastRevision: 6})
	if status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
		t.Fatal("stale cast was substituted", err, f.packets)
	}
}

func TestHostResurrectionKeepsNativeTickPassiveAfterLuaLeaseExpiry(t *testing.T) {
	s, f, advance := resRig(t)
	learnSpells(s, 2006)
	s.sessionID = f.party.SessionId
	f.world.Self.RaceKnown, f.world.Self.RaceId = true, 1
	f.world.Owner.RaceKnown, f.world.Owner.RaceId = true, 1
	activateEncounterWriter(t, s, f)
	ownerCtx := modcb.WithPolicySupportOwnership(context.Background(), true, true)
	ownerCtx = modcb.WithPolicyEncounterAccess(ownerCtx, testEncounterKey, 1)
	md, _ := metadata.FromOutgoingContext(ownerCtx)
	ownerCtx = metadata.NewIncomingContext(context.Background(), md)
	if _, err := s.RenewPolicyEncounterLease(ownerCtx, wrapperspb.UInt64(1)); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("native resurrection competed with Lua", f.packets)
	}
	w, _ := s.snapshot(context.Background())
	view := s.encounterSupportLocked(context.Background(), w, s.onlineHealingGroup(f.party.Group))
	if len(view.Resurrections) != 1 || !view.Resurrections[0].Ready || view.Resurrections[0].PatientGuid != 3 {
		t.Fatal("dead leader not visible to Lua", view)
	}
	f.party.Group.Members[1].Online = false
	if view := s.encounterSupportLocked(context.Background(), w, s.onlineHealingGroup(f.party.Group)); len(view.Resurrections) != 0 {
		t.Fatal("offline member stayed eligible", view)
	}
	f.party.Group.Members[1].Online = true
	advance(encounterLeaseDuration)
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("expired host lease restarted native patient selection", s.stateLocked())
	}
	if _, err := requestResurrection(s); err != nil || lastCast(f) != 2006 {
		t.Fatal("exact learned resurrection unavailable after lease expiry", err)
	}
}

func TestLuaResurrectionCarriesExactCorpseAndNativeEffectReceipt(t *testing.T) {
	s, f, _ := resRig(t)
	learnSpells(s, 2006)
	s.sessionID = f.party.SessionId
	f.world.Self.RaceKnown, f.world.Self.RaceId = true, 1
	f.world.Owner.RaceKnown, f.world.Owner.RaceId = true, 1
	f.world.Owner.PlayerFlags = ghostFlag
	const corpse = uint64(0xf10100000000003a)
	f.world.PlayerCorpses = []*worldv1.CorpseState{{Guid: corpse, OwnerGuid: 3, Present: true, MapKnown: true, PositionKnown: true, X: 10, FlagsKnown: true, RaceKnown: true, RaceId: 1}}
	w, _ := s.snapshot(context.Background())
	view := s.encounterSupportLocked(context.Background(), w, s.onlineHealingGroup(f.party.Group))
	if len(view.Resurrections) != 1 || view.Resurrections[0].CastTargetGuid != corpse {
		t.Fatal("owned corpse unavailable", view)
	}
	st, err := s.ResurrectOnce(supportPolicyContext(), &actionsv1.ResurrectOnceRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: 3, SpellId: 2006})
	if err != nil || st.LastSpellOperation.TargetGuid != corpse || s.supportOp.policySpellToken == "" || binary.LittleEndian.Uint32(f.packets[0].Payload[6:]) != 0x8000 {
		t.Fatal("corpse cast lost binding", st, err)
	}
	// The native parser, rather than Lua or transport acceptance, retires it.
	p := []byte{s.castCount}
	p = binary.LittleEndian.AppendUint32(p, 2006)
	p = append(p, 47)
	_, err = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: p})
	if err != nil || s.state.CastPending || s.supportOp.view.Phase != "server_rejected_no_replay" {
		t.Fatal("native rejection did not retire support custody", err, s.supportOp)
	}
}

func TestEncounterLeaseWithoutLuaSupportPreservesExactResurrectionCapability(t *testing.T) {
	s, f, _ := resRig(t)
	s.sessionID = f.party.SessionId
	activateEncounterWriter(t, s, f)
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("native Tick selected an unrequested patient")
	}
	if _, err := requestResurrection(s); err != nil || lastCast(f) != 2006 {
		t.Fatal("an unrelated Lua policy suppressed the exact resurrection capability", err, s.stateLocked())
	}
}
