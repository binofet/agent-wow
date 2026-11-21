package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
	"time"
)

// This adapter forwards an exact test input, without patient, target or rank
// selection. Gameplay selection is tested through the maintained Lua bundle.
func castSelectedForTest(s *Service, spell uint32, target uint64) (*actionsv1.State, error) {
	return s.CastOffense(context.Background(), &actionsv1.CastOffenseRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: target, SpellId: spell})
}

func TestSelectedOffenseDoesNotApplyNativeOwnerHealthReserveOrLevelPolicy(t *testing.T) {
	for _, mode := range []string{"owner_dead", "owner_injured", "owner_absent", "level_gap", "exact_mana_cost"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r, _ := exactOffenseFixture(t)
			switch mode {
			case "owner_dead":
				f.world.Owner.Health, f.world.Owner.Alive = 0, false
			case "owner_injured":
				f.world.Owner.Health = 1
			case "owner_absent":
				f.world.Owner = nil
				f.party = nil
			case "level_gap":
				f.world.Self.LevelKnown, f.world.Self.Level = true, 5
				f.world.RequestedCreature.LevelKnown, f.world.RequestedCreature.Level = true, 80
			case "exact_mana_cost":
				f.world.Self.BaseManaKnown, f.world.Self.BaseMana = true, 100
				f.world.Self.Power = uint32(spellCost(s.learnedContractsLocked()[r.SpellId], f.world.Self))
			}
			if _, err := s.CastOffense(context.Background(), r); err != nil || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("harness applied a gameplay preference to exact action", err, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestSelectedOffenseDefiniteRejectionLeavesOnlyRealGCDAndNeverAutoreplays(t *testing.T) {
	for _, op := range []uint32{opcode.SMSGCastFailed, opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther} {
		t.Run(encounterGUID(uint64(op)), func(t *testing.T) {
			s, f, r, advance := exactOffenseFixture(t)
			if _, err := s.CastOffense(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			first, ready := s.castCount, s.nextAssist
			s.OnPacket(context.Background(), healingRejectionPacket(op, s.selfGUID, first, r.SpellId))
			if s.state.CastPending || s.nextAssist != ready || s.failedAssistTarget != 0 {
				t.Fatal("definite rejection installed native retry policy", s.stateLocked())
			}
			if _, err := s.CastOffense(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("real GCD was bypassed", err)
			}
			advance(ready.Sub(s.now()))
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("native Tick replayed a selected action", f.packets)
			}
			if _, err := s.CastOffense(context.Background(), r); err != nil || s.castCount != first+1 || countPackets(f, opcode.CMSGCastSpell) != 2 {
				t.Fatal("fresh selected action rejected after real GCD", err, s.stateLocked())
			}
		})
	}
}

func TestSelectedOffenseAmbiguousWriteNeverReplayedByMaintenance(t *testing.T) {
	s, f, r, advance := exactOffenseFixture(t)
	f.failSend = true
	out, err := s.CastOffense(context.Background(), r)
	if err != nil || out.GetLastSpellOperation().GetTransportWritten() || !out.GetCastPending() {
		t.Fatal(out, err)
	}
	f.failSend = false
	for range 5 {
		advance(time.Second)
		s.Tick(context.Background())
	}
	if countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.GetLastSpellOperation().GetEffectObserved() {
		t.Fatal("uncertain write was replayed or invented success", s.stateLocked(), f.packets)
	}
}
