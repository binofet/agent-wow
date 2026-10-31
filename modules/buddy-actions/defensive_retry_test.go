package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type ambiguousDefensiveCastSession struct{ *fakeSession }

func (f *ambiguousDefensiveCastSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	result, err := f.fakeSession.SendPacket(ctx, r, opts...)
	if err == nil && r.Opcode == opcode.CMSGCastSpell {
		return nil, errors.New("ambiguous defensive cast transport")
	}
	return result, err
}

func rejectDefensiveCast(t *testing.T, s *Service, op uint32) {
	t.Helper()
	if !s.state.CastPending || s.pendingTarget == 0 {
		t.Fatal("no actual defensive cast to reject", s.stateLocked())
	}
	target := s.pendingTarget
	s.OnPacket(context.Background(), healingRejectionPacket(op, s.selfGUID, s.castCount, s.pendingSpell))
	if s.state.CastPending || !s.state.Assisting || s.failedAssistTarget != target || s.failedAssistCastTarget != target {
		t.Fatal("matching rejection did not settle only its defensive cast", s.stateLocked())
	}
}

func acknowledgeDefensiveAttackStop(s *Service, target uint64) {
	p := append(packedGUID(s.selfGUID), packedGUID(target)...)
	p = binary.LittleEndian.AppendUint32(p, 0)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAttackstop, Payload: p})
}

func TestDefensiveRetryUsesNewCastAfterCorrelatedFailureCooldownAndCurrentWitness(t *testing.T) {
	for _, party := range []bool{false, true} {
		for _, class := range []uint32{5, 8, 4} {
			t.Run(fmt.Sprintf("party=%v/class=%d", party, class), func(t *testing.T) {
				rig := selfDefenseRig
				if party {
					rig = partyDefenseRig
				}
				s, f, target, advance := rig(t, class)
				s.Tick(context.Background())
				oldCount := s.castCount
				rejectDefensiveCast(t, s, opcode.SMSGCastFailed)
				advance(4 * time.Second)
				s.Tick(context.Background())
				if countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.AssistAttempts != 1 {
					t.Fatal("retry ignored the five-second failure cooldown", s.stateLocked())
				}
				if class == 4 {
					if !s.state.AutoAttackUnconfirmed {
						t.Fatal("lost existing attack-stop cleanup", s.stateLocked())
					}
					advance(2 * time.Second)
					s.Tick(context.Background())
					if countPackets(f, opcode.CMSGCastSpell) != 1 {
						t.Fatal("retried before the exact melee stop receipt")
					}
					acknowledgeDefensiveAttackStop(s, target.Guid)
				} else {
					advance(2 * time.Second)
				}
				s.Tick(context.Background())
				if countPackets(f, opcode.CMSGCastSpell) != 2 || s.state.AssistAttempts != 2 || !s.state.CastPending ||
					s.castCount == oldCount || s.pendingTarget != target.Guid || s.failedAssistTarget != target.Guid ||
					s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 {
					t.Fatal("fresh defense did not make a new, unconfirmed cast", s.stateLocked(), f.packets)
				}
			})
		}
	}
}

func TestDefensiveRetryRejectsMissingCurrentAttackOrPartyAuthority(t *testing.T) {
	for _, party := range []bool{false, true} {
		for _, mode := range []string{"attack_stopped", "selected_only", "foreign_victim", "dead", "missing", "stale", "offline_victim"} {
			if !party && mode == "offline_victim" {
				continue
			}
			t.Run(fmt.Sprintf("party=%v/%s", party, mode), func(t *testing.T) {
				rig := selfDefenseRig
				if party {
					rig = partyDefenseRig
				}
				s, f, c, advance := rig(t, 8)
				s.Tick(context.Background())
				rejectDefensiveCast(t, s, opcode.SMSGCastFailed)
				switch mode {
				case "attack_stopped", "selected_only":
					c.AttackVictimKnown = false
					c.TargetKnown, c.TargetGuid = true, s.selfGUID
				case "foreign_victim":
					c.AttackVictimGuid, c.TargetGuid = 99, 99
				case "dead":
					c.Alive, c.Health = false, 0
				case "missing":
					c.Present = false
				case "stale":
					c.AttackAgeMs, c.TargetKnown = 90000, false
				case "offline_victim":
					for _, m := range f.party.Group.Members {
						if m.Guid == c.AttackVictimGuid {
							m.Online = false
						}
					}
				}
				advance(6 * time.Second)
				s.Tick(context.Background())
				if countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.CastPending || s.failedAssistTarget != c.Guid {
					t.Fatal("failure receipt became new attack authority", s.stateLocked())
				}
			})
		}
	}
}

func TestDefensiveRetryRechecksWitnessAndResourcesAfterFacing(t *testing.T) {
	for _, party := range []bool{false, true} {
		for _, mode := range []string{"attack_stopped", "foreign_victim", "range", "mana", "immune", "level", "offline_victim"} {
			if !party && mode == "offline_victim" {
				continue
			}
			t.Run(fmt.Sprintf("party=%v/%s", party, mode), func(t *testing.T) {
				rig := selfDefenseRig
				if party {
					rig = partyDefenseRig
				}
				s, f, c, advance := rig(t, 8)
				s.Tick(context.Background())
				rejectDefensiveCast(t, s, opcode.SMSGCastFailed)
				advance(6 * time.Second)
				f.onClock = func() {
					switch mode {
					case "attack_stopped":
						c.AttackVictimKnown = false
					case "foreign_victim":
						c.AttackVictimGuid, c.TargetGuid = 99, 99
					case "range":
						c.X += 40
					case "mana":
						f.world.Self.Power = 0
					case "immune":
						c.UnitFlags |= 0x100
					case "level":
						c.Level = 9
					case "offline_victim":
						for _, m := range f.party.Group.Members {
							if m.Guid == c.AttackVictimGuid {
								m.Online = false
							}
						}
					}
				}
				s.Tick(context.Background())
				if countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.CastPending || s.state.AssistAttempts != 1 {
					t.Fatal("post-facing proof loss allowed defensive retry", s.stateLocked())
				}
			})
		}
	}
}

func TestDefensiveRetryDoesNotRetireUncorrelatedPendingOrTransportAmbiguity(t *testing.T) {
	for _, mode := range []string{"count", "spell", "caster", "no_receipt", "transport"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, advance := selfDefenseRig(t, 8)
			if mode == "transport" {
				s.session = &ambiguousDefensiveCastSession{f}
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("fixture never issued its initial cast", s.stateLocked())
			}
			if mode != "transport" && mode != "no_receipt" {
				caster, count, spell := s.selfGUID, s.castCount, s.pendingSpell
				switch mode {
				case "count":
					count++
				case "spell":
					spell++
				case "caster":
					caster++
				}
				s.OnPacket(context.Background(), healingRejectionPacket(opcode.SMSGSpellFailure, caster, count, spell))
			}
			before := countPackets(f, opcode.CMSGCastSpell)
			advance(6 * time.Second)
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != before || s.failedAssistCastTarget != 0 || s.state.Assisting {
				t.Fatal("uncertain or unrelated result gained retry authority", s.stateLocked())
			}
		})
	}
}

func TestDefensiveRetryPreservesMissionAndNonCastAttackStopQuarantine(t *testing.T) {
	for _, mode := range []string{"practice", "mission", "no_cast_rejection", "server_attack_stop"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, advance := selfDefenseRig(t, 4)
			s.Tick(context.Background())
			rejectDefensiveCast(t, s, opcode.SMSGCastFailed)
			if mode == "server_attack_stop" {
				acknowledgeDefensiveAttackStop(s, c.Guid) // Unsolicited: not a spell-rejection retry grant.
			} else if mode == "no_cast_rejection" {
				s.failedAssistCastTarget = 0
			} else if mode == "practice" {
				s.state.Practicing = true
			} else {
				s.fargodeep.active = true
			}
			advance(6 * time.Second)
			before := countPackets(f, opcode.CMSGCastSpell)
			s.assistObservedTargetLocked(context.Background(), f.world, c, true)
			if countPackets(f, opcode.CMSGCastSpell) != before || s.failedAssistTarget != c.Guid {
				t.Fatal("bypassed a non-defensive-cast quarantine", s.stateLocked())
			}
		})
	}
}

func TestDefensiveRetryAcceptsOnlyCorrelatedNormalFailurePacketFamilies(t *testing.T) {
	for _, op := range []uint32{opcode.SMSGCastFailed, opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther} {
		t.Run(opcode.WorldName(op), func(t *testing.T) {
			s, f, c, advance := selfDefenseRig(t, 5)
			s.Tick(context.Background())
			rejectDefensiveCast(t, s, op)
			advance(6 * time.Second)
			s.Tick(context.Background())
			if s.state.AssistAttempts != 2 || s.state.LastCastTargetGuid != c.Guid || countPackets(f, opcode.CMSGCastSpell) != 2 {
				t.Fatal(s.stateLocked())
			}
		})
	}
}
