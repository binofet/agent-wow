package buddyactions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

func starterStealthRig(t *testing.T) (*Service, *fakeSession, *worldv1.CreatureState, string, func(time.Duration)) {
	t.Helper()
	s, f, advance := practiceRig(t, 4)
	target := f.world.NearbyCreatures[0]
	target.X, target.DynamicFlagsKnown = -8925, true
	learnSpells(s, autoAttack, sinisterStrike, 1784, 1785)
	source := strings.Replace(behaviors.StarterSpellSource, "melee_yards=3.5", "melee_yards=12", 1)
	if source == behaviors.StarterSpellSource {
		t.Fatal("missing accepted approach tuning")
	}
	accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: source})
	if err != nil || accepted.Revision == "" {
		t.Fatal(accepted, err)
	}
	return s, f, target, source, advance
}

func TestStarterStealthAcceptedSameSessionChoiceAndRankReachActualSelfPacket(t *testing.T) {
	for _, mode := range []string{"disable", "lower rank"} {
		t.Run(mode, func(t *testing.T) {
			s, f, target, source, _ := starterStealthRig(t)
			if !s.refreshPositionLocked(f.world) {
				t.Fatal("missing current pose")
			}
			// The active mission has selected this observed wolf. The same native
			// selector can inspect all learned ranks without writing a packet.
			s.practiceTarget, s.practicePullStarted = target.Guid, true
			point, _, _ := creaturePosition(target)
			before := s.starterWeaponDecisionLocked(context.Background(), f.world, target, point)
			if !before.valid || before.kind != "cast_self" || before.spell.ID != 1785 || len(f.packets) != 0 {
				t.Fatal("shared Lua did not select the highest learned Stealth", before, s.starterSpells.failure())
			}
			changed := strings.Replace(source, "enabled=true, rank_order=", "enabled=false, rank_order=", 1)
			if mode == "lower rank" {
				changed = strings.Replace(source, `if id then return {kind="cast_self",spellId=id} end`,
					`if id then return {kind="cast_self",spellId=1784} end`, 1)
			}
			previous := s.behavior.Status().Revision
			accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
				SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: previous, Source: changed})
			if err != nil || changed == source || accepted.Revision == previous {
				t.Fatal("same-session source CAS failed", accepted, err)
			}
			s.Tick(context.Background())
			want := 0
			if mode == "lower rank" {
				want = 1
			}
			if countPackets(f, opcode.CMSGCastSpell) != want || countPackets(f, opcode.CMSGAttackswing) != 0 ||
				want == 1 && (lastCast(f) != 1784 || s.supportOp.view.TargetGuid != s.selfGUID ||
					!s.supportOp.view.TransportWritten || s.supportOp.view.ServerCastGo || s.supportOp.view.EffectObserved) {
				t.Fatal("accepted source did not control the exact self-cast without inventing an effect", s.stateLocked(), f.packets)
			}
		})
	}
}

type starterStealthStopSession struct {
	*fakeSession
	afterStop func()
}

func (f starterStealthStopSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out, err := f.fakeSession.SendPacket(ctx, r, opts...)
	if r.Opcode == opcode.MSGMoveStop && f.afterStop != nil {
		f.afterStop()
	}
	return out, err
}

func TestStarterStealthRevalidatesAfterActualStop(t *testing.T) {
	for _, mode := range []string{"unchanged", "source", "body", "visit", "combat", "caster", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, f, target, source, _ := starterStealthRig(t)
			s.refreshPositionLocked(f.world)
			s.practiceTarget, s.practicePullStarted = target.Guid, true
			point, _, _ := creaturePosition(target)
			chosen := s.starterWeaponDecisionLocked(context.Background(), f.world, target, point)
			if !chosen.valid || chosen.kind != "cast_self" {
				t.Fatal(chosen, s.starterSpells.failure())
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stops := 0
			s.state.Moving = true
			s.session = starterStealthStopSession{f, func() {
				stops++
				switch mode {
				case "source":
					_, err := s.behavior.Reload(strings.Replace(source, "enabled=true, rank_order=", "enabled=false, rank_order=", 1), s.behavior.Status().Revision)
					if err != nil {
						t.Fatal(err)
					}
				case "body":
					f.world.Self.Guid++
				case "visit":
					f.world.VisitRevision++
				case "combat":
					f.world.Self.UnitFlags |= 0x80000
				case "caster":
					f.world.Self.AuraState = 1 << 11
				case "cancel":
					cancel()
				}
			}}
			s.castStarterSelfLocked(ctx, f.world, target.Guid, chosen)
			want := 0
			if mode == "unchanged" {
				want = 1
			}
			if stops != 1 || countPackets(f, opcode.CMSGCastSpell) != want || countPackets(f, opcode.CMSGAttackswing) != 0 {
				t.Fatal("old self-cast escaped the post-STOP boundary", stops, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestStarterStealthPendingAndUnknownCannotReplayAfterEditOrReceiptReplacement(t *testing.T) {
	for _, mode := range []string{"pending", "lost reply", "timeout", "GO without effect"} {
		t.Run(mode, func(t *testing.T) {
			s, f, target, source, advance := starterStealthRig(t)
			if mode == "lost reply" {
				s.session = starterWeaponAmbiguousSession{f}
			}
			s.Tick(context.Background())
			op := s.supportOp
			if countPackets(f, opcode.CMSGCastSpell) != 1 || lastCast(f) != 1785 || op == nil || op.owner != target.Guid {
				t.Fatal("fixture did not enter actual selected Stealth", s.stateLocked(), f.packets)
			}
			if mode != "pending" {
				if mode == "GO without effect" {
					emitPolymorph(s, opcode.SMSGSpellGo, buffGo(s, s.selfGUID))
					if !op.view.ServerCastGo || op.view.EffectObserved {
						t.Fatal("actual parser did not retain GO/effect distinction", op.view)
					}
				}
				advance(6 * time.Second)
				s.Tick(context.Background())
				if s.starterSpells.uncertainStrike != op {
					t.Fatal("entered uncertainty was not retained", op.view, s.stateLocked())
				}
				_, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
					SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: s.behavior.Status().Revision, Source: source + "\n-- after entered self cast"})
				if err != nil {
					t.Fatal(err)
				}
				for _, enabled := range []bool{false, true} {
					if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: enabled}); err != nil {
						t.Fatal(err)
					}
				}
				heal, _ := s.spellData.Lookup(lesserHeal)
				s.reserveHealReceiptLocked("", heal, s.selfGUID)
				target.Guid++ // Another enemy cannot grant a new self-Stealth attempt.
			}
			for range 2 {
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
			}
			if countPackets(f, opcode.CMSGCastSpell) != 1 || op.view.EffectObserved ||
				mode != "pending" && (s.starterSpells.uncertainStrike != op || s.supportOp == op) {
				t.Fatal("source/task/target/general receipt replacement replayed a self cast", op.view, s.stateLocked(), f.packets)
			}
		})
	}
}
