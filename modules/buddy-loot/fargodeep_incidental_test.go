package buddyloot

import (
	"context"
	"encoding/binary"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func incidentalLootRig(t *testing.T) (*Service, *fargodeepLootSession) {
	t.Helper()
	s, f, _ := fargodeepLootRig(t)
	f.actions.Fargodeep = nil // No client encounter or historical death receipt.
	return s, f
}

func TestIncidentalFargodeepLootUsesActualRightsWithoutInventedEncounter(t *testing.T) {
	for _, qid := range []uint32{47, 60} {
		for _, entry := range []uint32{40, 475} {
			s, f := incidentalLootRig(t)
			f.world.SelfQuestLog[0].QuestId = qid
			c := f.world.LootableCorpses[0]
			c.Entry = entry
			c.Guid = 0xf130000000000000 | uint64(entry)<<24 | 0x41
			enable(t, s)
			s.Tick(context.Background())
			got := current(t, s)
			if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || binary.LittleEndian.Uint64(f.packets[0].Payload) != c.Guid || !s.questFargodeep || got.LastCorpseGuid != 0 || got.ItemsStoredConfirmed != 0 || got.ReleasesObserved != 0 || f.actions.Fargodeep != nil {
				t.Fatal("normal rights did not open exact corpse, or fabricated kill/progress", got)
			}
		}
	}
}

func TestIncidentalFargodeepLootRequiresIncompleteGoalAndSafeCurrentParty(t *testing.T) {
	changes := map[string]func(*Service, *fargodeepLootSession){
		"complete": func(s *Service, f *fargodeepLootSession) {
			f.world.SelfQuestLog[0].Complete = true
			f.world.SelfQuestLog[0].State = 1
		},
		"failed":      func(s *Service, f *fargodeepLootSession) { f.world.SelfQuestLog[0].Failed = true },
		"unknown_log": func(s *Service, f *fargodeepLootSession) { f.world.SelfQuestLogKnown = false },
		"tap":         func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].DynamicFlags = 1 },
		"lootable":    func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].DynamicFlags = 8 },
		"living": func(s *Service, f *fargodeepLootSession) {
			f.world.LootableCorpses[0].Health = 10
			f.world.LootableCorpses[0].Alive = true
		},
		"other_entry":  func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].Entry = 476 },
		"body":         func(s *Service, f *fargodeepLootSession) { f.party.SessionId = "different-body" },
		"offline_peer": func(s *Service, f *fargodeepLootSession) { f.party.Group.Members[1].Online = false },
		"peer_combat":  func(s *Service, f *fargodeepLootSession) { f.world.NearbyPlayers[0].UnitFlags |= 0x80000 },
		"peer_injured": func(s *Service, f *fargodeepLootSession) {
			f.world.NearbyPlayers[0].Health = 84
			f.world.NearbyPlayers[0].MaxHealth = 100
		},
		"self_injured":      func(s *Service, f *fargodeepLootSession) { f.world.Self.Health = 84; f.world.Self.MaxHealth = 100 },
		"peer_far":          func(s *Service, f *fargodeepLootSession) { f.world.NearbyPlayers[0].X = f.actions.Position.X + 35.01 },
		"peer_pose_unknown": func(s *Service, f *fargodeepLootSession) { f.world.NearbyPlayers[0].PositionKnown = false },
		"peer_moving":       func(s *Service, f *fargodeepLootSession) { f.world.NearbyPlayers[0].MovementFlags = 1 },
		"recovery":          func(s *Service, f *fargodeepLootSession) { f.actions.Recovery = &actionsv1.RecoveryState{Active: true} },
		"active_mission": func(s *Service, f *fargodeepLootSession) {
			f.actions.Fargodeep = &actionsv1.FargodeepEncounterState{Active: true}
		},
		"cast":             func(s *Service, f *fargodeepLootSession) { f.actions.CastPending = true },
		"auto_attack":      func(s *Service, f *fargodeepLootSession) { f.actions.AutoAttacking = true },
		"uncertain_attack": func(s *Service, f *fargodeepLootSession) { f.actions.AutoAttackUnconfirmed = true },
		"motion":           func(s *Service, f *fargodeepLootSession) { f.actions.MotionUnconfirmed = true },
		"other_lease":      func(s *Service, f *fargodeepLootSession) { f.leaseOwner = "trade" },
		"capacity":         func(s *Service, f *fargodeepLootSession) { f.world.InventoryKnown = false },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f := incidentalLootRig(t)
			change(s, f)
			enable(t, s)
			s.Tick(context.Background())
			expectedOwner := ""
			if name == "other_lease" {
				expectedOwner = "trade"
			}
			if len(f.packets) != 0 || f.leaseOwner != expectedOwner {
				t.Fatal("unsafe incidental open", current(t, s))
			}
		})
	}
}

func TestIncidentalFargodeepLastItemCompleteFinishesOnlyBoundReceipts(t *testing.T) {
	s, f := incidentalLootRig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(fargodeepCorpseGUID, 0, lootItem{slot: 2, id: 773, count: 1}))
	s.Tick(context.Background())
	// Actual own log can become COMPLETE before its slot-removal/release arrives.
	f.world.SelfQuestLog[0].Complete, f.world.SelfQuestLog[0].State = true, 1
	f.world.LootableCorpses[0].DynamicFlags = 8
	f.world.LootableCorpses = nil
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	emit(s, opcode.SMSGItemPushResult, pushPacket(4, 773, 1))
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("peer receipt became own progress")
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 773, 1))
	s.Tick(context.Background())
	s.Tick(context.Background())
	if f.packets[len(f.packets)-1].Opcode != opcode.CMSGLootRelease || current(t, s).ItemsStoredConfirmed != 1 {
		t.Fatal("complete log interrupted bound receipt", current(t, s))
	}
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(fargodeepCorpseGUID), 1))
	s.Tick(context.Background())
	if f.leaseOwner != "" || current(t, s).ReleasesObserved != 1 || s.questFargodeep {
		t.Fatal("bound operation did not finish", current(t, s))
	}
	other := proto.Clone(f.world.NearbyCreatures[0]).(*worldv1.CreatureState)
	other.Guid++
	other.DynamicFlags = 9
	f.world.LootableCorpses = []*worldv1.CreatureState{other}
	f.world.NearbyCreatures = []*worldv1.CreatureState{other}
	before := len(f.packets)
	s.Tick(context.Background())
	if len(f.packets) != before {
		t.Fatal("COMPLETE quest selected a new unbound corpse")
	}
}

func TestIncidentalFargodeepRechecksStopAndFinalWorldWithoutRetargeting(t *testing.T) {
	for _, moment := range []string{"stop", "final"} {
		for _, mode := range []string{"quest_lost", "quest_complete", "tap_lost", "party_lost", "peer_injured", "source_changed"} {
			t.Run(moment+mode, func(t *testing.T) {
				s, f := incidentalLootRig(t)
				change := func(w *worldv1.SnapshotResponse) {
					switch mode {
					case "quest_lost":
						w.SelfQuestLog[0].QuestId = 0
					case "quest_complete":
						w.SelfQuestLog[0].Complete, w.SelfQuestLog[0].State = true, 1
					case "tap_lost":
						w.LootableCorpses[0].DynamicFlags = 1
					case "party_lost":
						f.party.Group.Members[1].Online = false
					case "peer_injured":
						w.NearbyPlayers[0].Health = 1
					case "source_changed":
						w.LootableCorpses[0].Guid++
					}
				}
				if moment == "stop" {
					f.afterAcquire = func() { change(f.world) }
				} else {
					f.onSnapshot = func(n int, w *worldv1.SnapshotResponse) {
						if n == 4 && mode != "party_lost" {
							change(w)
						}
					}
					if mode == "party_lost" {
						f.afterRenew = func() { change(f.world) }
					}
				}
				enable(t, s)
				s.Tick(context.Background())
				if len(f.packets) != 0 || f.leaseOwner != "" {
					t.Fatal("stale incidental authority opened loot", current(t, s))
				}
			})
		}
	}
}
