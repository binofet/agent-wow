package buddyactions

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestCorpseApproachConstructorUsesOrdinaryExactTargetWithoutNamedPolicy(t *testing.T) {
	for _, entry := range []uint32{40, 475, 299, 327, 476, 999999} {
		t.Run(fmt.Sprint(entry), func(t *testing.T) {
			s, f, _, request := navLootRig(t)
			f.world.Owner, f.party = nil, nil
			f.world.SelfQuestLogKnown, f.world.SelfQuestLog = false, nil
			corpse := f.world.LootableCorpses[0]
			corpse.Entry, corpse.Guid = entry, uint64(0xf130)<<48|uint64(entry)<<24|1
			corpse.LevelKnown, corpse.Level = true, 99
			request.CorpseGuid = corpse.Guid
			s.starterSpells.remote = func(context.Context, map[string]any) (bool, string, map[string]any, error) {
				t.Fatal("exact native approach reentered gameplay selection")
				return false, "", nil, nil
			}
			s.planMapPath = func(_ context.Context, _ string, mapID uint32, from, to position) ([]position, error) {
				if mapID != f.world.Self.MapId || to != (position{corpse.X, corpse.Y, corpse.Z, corpse.Orientation}) {
					t.Fatal("route did not use the exact offered corpse", mapID, to)
				}
				return []position{from, to}, nil
			}
			out, err := s.ApproachLoot(context.Background(), request)
			if err != nil || !out.Moving || !out.NavAvailable || countPackets(f, opcode.MSGMoveStartForward) != 1 ||
				countPackets(f, opcode.CMSGLoot) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 || s.lootApproachGuard != nil {
				t.Fatal("ordinary constructor retained a compiled quest/party/profile selector", out, err, f.packets)
			}
		})
	}
}

func TestCorpseApproachFinalClockRechecksPhysicalAuthorityAndOnlyOwnedCleanup(t *testing.T) {
	for _, moving := range []bool{false, true} {
		for _, mode := range []string{"combat", "dead", "foreign self", "new session", "new visit", "self correction", "corpse revived", "corpse not lootable", "corpse changed", "corpse revision", "corpse absent", "duplicate corpse", "lease expired", "cast", "autoattack", "same owner new token", "different owner new token"} {
			t.Run(fmt.Sprint(moving)+"/"+mode, func(t *testing.T) {
				s, f, advance, request := navLootRig(t)
				corpse := f.world.LootableCorpses[0]
				if moving {
					out, err := s.ApproachLoot(context.Background(), request)
					if err != nil || !out.Moving {
						t.Fatal("initial ordinary movement failed", out, err)
					}
					advance(200 * time.Millisecond)
				}
				starts, beats := countPackets(f, opcode.MSGMoveStartForward), countPackets(f, opcode.MSGMoveHeartbeat)
				f.onClock = func() {
					f.onClock = nil
					switch mode {
					case "combat":
						f.world.Self.UnitFlags |= 0x80000
					case "dead":
						f.world.Self.Alive, f.world.Self.Health = false, 0
					case "foreign self":
						f.world.Self.Guid++
					case "new session":
						s.sessionID += "-replacement"
					case "new visit":
						f.world.VisitRevision++
					case "self correction":
						f.world.Self.PositionRevision++
					case "corpse revived":
						corpse.Health, corpse.Alive = 1, true
					case "corpse not lootable":
						corpse.DynamicFlags = 0
					case "corpse changed":
						corpse.X += .1
					case "corpse revision":
						corpse.PositionRevision++
					case "corpse absent":
						f.world.LootableCorpses = nil
					case "duplicate corpse":
						f.world.LootableCorpses = append(f.world.LootableCorpses, proto.Clone(corpse).(*worldv1.CreatureState))
					case "lease expired":
						advance(30 * time.Second)
					case "cast":
						s.state.CastPending = true
					case "autoattack":
						s.state.AutoAttacking = true
					case "same owner new token":
						s.interactionToken = "replacement"
					case "different owner new token":
						s.interactionToken, s.state.InteractionOwner = "replacement", "professions"
					}
				}
				out, err := s.ApproachLoot(context.Background(), request)
				if err != nil || countPackets(f, opcode.MSGMoveStartForward) != starts || countPackets(f, opcode.MSGMoveHeartbeat) != beats || s.lootApproachGuard != nil {
					t.Fatal("final physical/lease change emitted movement or leaked its guard", out, err, f.packets)
				}
				cleanupForbidden := mode == "foreign self" || mode == "new session" || mode == "new visit" || mode == "same owner new token" || mode == "different owner new token"
				stops := countPackets(f, opcode.MSGMoveStop)
				if moving && !cleanupForbidden && (stops != 1 || out.Moving || out.MotionUnconfirmed) {
					t.Fatal("unsafe owned movement did not receive its bounded STOP", out, f.packets)
				}
				if cleanupForbidden && stops != 0 {
					t.Fatal("old approach stopped a replaced body or lease", f.packets)
				}
			})
		}
	}
}

func TestCorpseApproachPlanCannotChangeBodyVisitOrExactTarget(t *testing.T) {
	for _, mode := range []string{"session", "visit", "corpse revived", "corpse no longer lootable", "same owner new token"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, request := navLootRig(t)
			s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
				switch mode {
				case "session":
					s.sessionID += "-replacement"
				case "visit":
					f.world.VisitRevision++
				case "corpse revived":
					f.world.LootableCorpses[0].Alive = true
				case "corpse no longer lootable":
					f.world.LootableCorpses[0].DynamicFlags = 0
				case "same owner new token":
					s.interactionToken = "replacement"
				}
				return []position{from, to}, nil
			}
			out, err := s.ApproachLoot(context.Background(), request)
			if err == nil && out.Moving || len(f.packets) != 0 {
				t.Fatal("stale planned route emitted a packet", out, err, f.packets)
			}
		})
	}
}

func TestCorpseApproachCleanupClockCannotStopReplacementBodyOrLease(t *testing.T) {
	for _, mode := range []string{"unchanged", "new session", "new visit", "same owner new token", "different owner new token"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, request := navLootRig(t)
			out, err := s.ApproachLoot(context.Background(), request)
			if err != nil || !out.Moving {
				t.Fatal(out, err)
			}
			advance(200 * time.Millisecond)
			clocks := 0
			f.onClock = func() {
				clocks++
				if clocks == 1 {
					f.world.LootableCorpses[0].Alive = true
					return
				}
				f.onClock = nil
				switch mode {
				case "new session":
					s.sessionID += "-replacement"
				case "new visit":
					f.world.VisitRevision++
				case "same owner new token":
					s.interactionToken = "replacement"
				case "different owner new token":
					s.interactionToken, s.state.InteractionOwner = "replacement", "professions"
				}
			}
			out, err = s.ApproachLoot(context.Background(), request)
			wantStops := 0
			if mode == "unchanged" {
				wantStops = 1
			}
			if err != nil || clocks != 2 || countPackets(f, opcode.MSGMoveStop) != wantStops ||
				countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || s.lootApproachGuard != nil {
				t.Fatal("cleanup clock reused old body/lease authority or lost the owned STOP", out, err, clocks, f.packets)
			}
		})
	}
}

func TestCorpseApproachEnteredMovementLossRetainsUnknownWithoutReplay(t *testing.T) {
	for _, heartbeat := range []bool{false, true} {
		t.Run(fmt.Sprint(heartbeat), func(t *testing.T) {
			s, f, advance, request := navLootRig(t)
			op := uint32(opcode.MSGMoveStartForward)
			if heartbeat {
				out, err := s.ApproachLoot(context.Background(), request)
				if err != nil || !out.Moving {
					t.Fatal(out, err)
				}
				advance(200 * time.Millisecond)
				op = opcode.MSGMoveHeartbeat
			}
			f.failSend, f.failSendOpcode = true, op
			out, err := s.ApproachLoot(context.Background(), request)
			if err != nil || !out.MotionUnconfirmed || out.LastTransportFailure == nil || !out.LastTransportFailure.OutcomeUnknown || countPackets(f, op) != 1 {
				t.Fatal("entered loss was not retained", out, err, f.packets)
			}
			f.failSend = false
			for range 2 {
				advance(200 * time.Millisecond)
				out, err = s.ApproachLoot(context.Background(), request)
				if err != nil || !out.MotionUnconfirmed || countPackets(f, op) != 1 {
					t.Fatal("unknown movement was implicitly retried", out, err, f.packets)
				}
			}
		})
	}
}

func TestLootApproachPlannerAndCancellationCleanupRetainOriginalLease(t *testing.T) {
	for _, harvest := range []bool{false, true} {
		for _, cancelled := range []bool{false, true} {
			for _, replacement := range []string{"unchanged", "session", "visit", "same owner token", "different owner token"} {
				t.Run(fmt.Sprint(harvest, "/", cancelled, "/", replacement), func(t *testing.T) {
					rig := navLootRig
					if harvest {
						rig = harvestApproachRig
					}
					s, f, advance, request := rig(t)
					out, err := s.ApproachLoot(context.Background(), request)
					if err != nil || !out.Moving {
						t.Fatal(out, err)
					}
					advance(200 * time.Millisecond)
					replace := func() {
						switch replacement {
						case "session":
							s.sessionID += "-replacement"
						case "visit":
							f.world.VisitRevision++
						case "same owner token":
							s.interactionToken = "replacement"
						case "different owner token":
							s.interactionToken, s.state.InteractionOwner = "replacement", "professions"
						}
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if cancelled {
						cancel()
						f.onClock = func() { f.onClock = nil; replace() }
					} else {
						s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
							replace()
							return nil, errors.New("observed route unavailable")
						}
					}
					_, err = s.ApproachLoot(ctx, request)
					wantStops := 0
					if replacement == "unchanged" {
						wantStops = 1
					}
					if cancelled != (err != nil) || countPackets(f, opcode.MSGMoveStop) != wantStops ||
						countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || s.lootApproachGuard != nil {
						t.Fatal("cleanup rebound to a replacement or lost its original owned STOP", err, f.packets)
					}
				})
			}
		}
	}
}
