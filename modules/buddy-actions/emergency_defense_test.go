package buddyactions

import (
	"context"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestEmergencyDefensePreemptsTaskCustodyWithAssistDisabled(t *testing.T) {
	for _, owner := range []string{"exploration", "quests", "loot", "professions", "trade", "training", "mail"} {
		t.Run(owner, func(t *testing.T) {
			s, f, target, _ := selfDefenseRig(t, 8)
			if _, err := s.Assist(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
				t.Fatal(err)
			}
			f.world.Self.UnitFlags = 0
			lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: owner, LeaseMs: 30000})
			if err != nil {
				t.Fatal(err)
			}
			f.world.Self.UnitFlags = 0x80000
			s.state.Moving, s.state.Traveling = true, true
			s.Tick(context.Background())
			got := state(t, s)
			if got.Assisting || got.InteractionOwner != "" || got.Traveling || got.Moving || lastCast(f) != fireball || got.LastCastTargetGuid != target.Guid || got.ServerAssistGo != 0 {
				t.Fatal(got, f.packets)
			}
			stop, cast := -1, -1
			for i, packet := range f.packets {
				if packet.Opcode == opcode.MSGMoveStop {
					stop = i
				}
				if packet.Opcode == opcode.CMSGCastSpell {
					cast = i
				}
			}
			if stop < 0 || cast <= stop {
				t.Fatal("defense did not stop the task before casting", f.packets)
			}
			if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: owner, Token: lease.Token}); err == nil {
				t.Fatal("retired task token was renewed")
			}
		})
	}
}

func TestEmergencyPartyDefenseDoesNotDependOnOwnerAssist(t *testing.T) {
	for _, class := range []uint32{5, 8, 4} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			s, f, target, _ := partyDefenseRig(t, class)
			s.state.Assisting, s.state.Following, s.state.Healing, s.state.Buffing = false, false, false, false
			s.Tick(context.Background())
			if s.state.Assisting || s.state.AssistAttempts != 1 || s.state.LastCastTargetGuid != target.Guid || lastCast(f) == 0 {
				t.Fatal(s.stateLocked(), f.packets)
			}
		})
	}
}

func TestEmergencyDefenseKeepsWitnessAndUncertaintyGuards(t *testing.T) {
	for _, mode := range []string{"no_attacker", "foreign_victim", "dead_target", "wrong_body", "motion_unknown", "aa_unknown", "pending_cast", "stop_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, f, target, _ := selfDefenseRig(t, 8)
			s.state.Assisting = false
			s.state.InteractionOwner, s.interactionToken = "exploration", "old-task"
			s.interactionUntil = s.now().Add(30 * time.Second)
			switch mode {
			case "no_attacker":
				target.AttackVictimKnown = false
			case "foreign_victim":
				target.AttackVictimGuid = 99
			case "dead_target":
				target.Alive, target.Health = false, 0
			case "wrong_body":
				f.world.Self.Guid = 99
			case "motion_unknown":
				s.state.MotionUnconfirmed = true
			case "aa_unknown":
				s.state.AutoAttackUnconfirmed = true
			case "pending_cast":
				s.state.CastPending, s.castUntil = true, s.now().Add(time.Second)
			case "stop_failure":
				s.state.Moving, f.failSend = true, true
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 {
				t.Fatal("unproven defense emitted offense", s.stateLocked(), f.packets)
			}
			if mode == "stop_failure" {
				if !s.state.MotionUnconfirmed || s.state.InteractionOwner != "" {
					t.Fatal("ambiguous STOP lost its uncertainty or retained writer", s.stateLocked())
				}
			} else if s.state.InteractionOwner != "exploration" {
				t.Fatal("no fresh attack proof must not retire custody", s.stateLocked())
			}
		})
	}
}

func TestExpiredCastKeepsIntentAndRequiresFreshDefenseSelection(t *testing.T) {
	s, f, target, advance := selfDefenseRig(t, 8)
	s.state.Following, s.state.Healing = true, true
	s.followUntil, s.healingUntil = s.now().Add(time.Hour), s.now().Add(time.Hour)
	s.Tick(context.Background())
	if !s.state.CastPending || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal(s.stateLocked())
	}
	advance(10 * time.Second)
	s.Tick(context.Background())
	if s.state.CastPending || !s.state.Following || !s.state.Healing || !s.state.Assisting || s.state.ServerAssistGo != 0 || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("timeout revoked intent or retried old cast", s.stateLocked(), f.packets)
	}
	// New offense still needs a current actual attacker after the cooldown.
	target.AttackVictimKnown = false
	advance(5 * time.Second)
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("missing fresh witness retried a cast")
	}
	target.AttackVictimKnown, target.AttackAgeMs = true, 0
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 2 || !s.state.CastPending || s.state.ServerAssistGo != 0 {
		t.Fatal("fresh same attacker did not permit a new cast after backoff", s.stateLocked(), f.packets)
	}
}

func TestEmergencySelfDefenseRechecksWitnessAndResourcesAfterFacing(t *testing.T) {
	for _, class := range []uint32{4, 8} {
		for _, mode := range []string{"victim", "dead", "absent", "own_body", "map", "revision", "range", "immune", "resources"} {
			t.Run(fmt.Sprintf("%d/%s", class, mode), func(t *testing.T) {
				s, f, c, _ := selfDefenseRig(t, class)
				s.state.Assisting = false
				f.onClock = func() {
					switch mode {
					case "victim":
						c.AttackVictimGuid = 99
					case "dead":
						c.Alive, c.Health = false, 0
					case "absent":
						c.Present = false
					case "own_body":
						f.world.Self.Guid = 99
					case "map":
						f.world.Self.MapId++
					case "revision":
						f.world.Self.PositionRevision++
					case "range":
						c.X += 100
					case "immune":
						c.UnitFlags |= 0x100
					case "resources":
						if class == 4 {
							f.world.Self.Mainhand = nil
						} else {
							f.world.Self.Power = 0
						}
					}
				}
				s.Tick(context.Background())
				if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || s.state.CastPending || s.state.AssistAttempts != 0 {
					t.Fatal("changed witness authorized offense", s.stateLocked(), f.packets)
				}
			})
		}
	}
}

func TestEmergencyDefenseDoesNotExtendExpiredHealingIntent(t *testing.T) {
	s, f, _, _ := selfDefenseRig(t, 5)
	s.state.Assisting, s.state.Healing = false, true
	s.healingUntil = s.now().Add(-time.Second)
	f.world.Self.Health = 10
	s.Tick(context.Background())
	if lastCast(f) == lesserHeal || s.state.HealAttempts != 0 {
		t.Fatal("emergency defense extended expired heal authority", s.stateLocked(), f.packets)
	}
}
