package buddyloot

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const fargodeepCorpseGUID uint64 = 0xf130000000000000 | uint64(475)<<24 | 0x41

type fargodeepLootSession struct {
	*fakeSession
	party *partyv1.StateResponse
}

func (f *fargodeepLootSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-party" && r.Method == "state" {
		f.calls = append(f.calls, "buddy-party.state")
		packed, err := anypb.New(proto.Clone(f.party))
		return &modv1.InvokeModuleResponse{Result: packed}, err
	}
	if r.Module == "buddy-actions" && r.Method == "approach_loot" {
		request := &actionsv1.ApproachLootRequest{}
		if r.Request.UnmarshalTo(request) != nil || request.CorpseGuid != fargodeepCorpseGUID || request.GameObjectGuid != 0 || request.Token != f.leaseToken || f.leaseOwner != "loot" || f.failApproach {
			return nil, errors.New("wrong Fargodeep approach scope")
		}
		f.approachCalls++
		f.actions.Position.X = f.world.LootableCorpses[0].X - 3
		f.actions.Moving = false
		f.actions.Reason = "loot_approach_within_distance"
		if f.afterApproach != nil {
			f.afterApproach()
		}
		a := proto.Clone(f.actions).(*actionsv1.State)
		a.InteractionOwner, a.InteractionLeaseRemainingMs = "loot", 15000
		packed, err := anypb.New(a)
		return &modv1.InvokeModuleResponse{Result: packed}, err
	}
	return f.fakeSession.InvokeModule(ctx, r, opts...)
}

func fargodeepLootRig(t *testing.T) (*Service, *fargodeepLootSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := wolfRig(t)
	s.state.SessionId = "fargodeep-loot-body"
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -9800, 124, 10
	f.actions.Position.X, f.actions.Position.Y, f.actions.Position.Z = -9800, 124, 10
	f.world.SelfQuestLog[0].QuestId = 47
	c := f.world.LootableCorpses[0]
	c.Guid, c.Entry = fargodeepCorpseGUID, 475
	c.X, c.Y, c.Z = -9798, 124, 10
	c.LevelKnown, c.Level, c.DynamicFlagsKnown, c.DynamicFlags = true, 5, true, 9
	c.UnitFlagsKnown = true
	f.world.NearbyCreatures = []*worldv1.CreatureState{c}
	for _, guid := range []uint64{4, 5} {
		p := proto.Clone(f.world.Self).(*worldv1.PlayerState)
		p.Guid = guid
		f.world.NearbyPlayers = append(f.world.NearbyPlayers, p)
	}
	f.actions.Fargodeep = &actionsv1.FargodeepEncounterState{TargetGuid: c.Guid, EngagerGuid: 5, Phase: "target_death_observed"}
	p := &partyv1.StateResponse{SessionId: s.state.SessionId, Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 8}}
	for _, g := range []uint64{1, 4, 5, 3} {
		p.Group.Members = append(p.Group.Members, &partyv1.Member{Guid: g, Self: g == 1, Online: g != 3})
	}
	wrapper := &fargodeepLootSession{fakeSession: f, party: p}
	s.session = wrapper
	return s, wrapper, advance
}

func TestFargodeepLootRequiresExplicitIntentAndActualBoundOwnQuest(t *testing.T) {
	s, f, _ := fargodeepLootRig(t)
	s.Tick(context.Background())
	if len(f.packets) != 0 {
		t.Fatal("quest implicitly enabled loot")
	}
	for _, qid := range []uint32{47, 60} {
		for _, complete := range []bool{false, true} {
			s, f, _ := fargodeepLootRig(t)
			f.world.SelfQuestLog[0].QuestId = qid
			f.world.SelfQuestLog[0].Complete = complete
			if complete {
				f.world.SelfQuestLog[0].State = 1
			}
			enable(t, s)
			s.Tick(context.Background())
			if !s.questFargodeep || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || binary.LittleEndian.Uint64(f.packets[0].Payload) != fargodeepCorpseGUID {
				t.Fatal("actual same-party corpse blocked with Joel offline", current(t, s))
			}
		}
	}
}

func TestFargodeepLootRejectsUnboundForeignUnsafeAndUnknownCorpses(t *testing.T) {
	changes := map[string]func(*Service, *fargodeepLootSession){
		"wrong_self":      func(s *Service, f *fargodeepLootSession) { s.selfGUID, f.world.Self.Guid = 8, 8 },
		"quest":           func(s *Service, f *fargodeepLootSession) { f.world.SelfQuestLog[0].QuestId = 7 },
		"failed":          func(s *Service, f *fargodeepLootSession) { f.world.SelfQuestLog[0].State = 2 },
		"partial_log":     func(s *Service, f *fargodeepLootSession) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] },
		"duplicate_quest": func(s *Service, f *fargodeepLootSession) { f.world.SelfQuestLog[1].QuestId = 47 },
		"wrong_target":    func(s *Service, f *fargodeepLootSession) { f.actions.Fargodeep.TargetGuid++ },
		"wrong_phase":     func(s *Service, f *fargodeepLootSession) { f.actions.Fargodeep.Phase = "disabled" },
		"active":          func(s *Service, f *fargodeepLootSession) { f.actions.Fargodeep.Active = true },
		"wrong_engager":   func(s *Service, f *fargodeepLootSession) { f.actions.Fargodeep.EngagerGuid = 99 },
		"wrong_entry":     func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].Entry = 476 },
		"unknown_level":   func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].LevelKnown = false },
		"high_level":      func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].Level = 8 },
		"living":          func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].Alive = true },
		"unknown_tap":     func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].DynamicFlagsKnown = false },
		"unknown_flags":   func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].UnitFlagsKnown = false },
		"not_ours":        func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].DynamicFlags = 5 },
		"not_lootable":    func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].DynamicFlags = 8 },
		"far":             func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].X += 12 },
		"height":          func(s *Service, f *fargodeepLootSession) { f.world.LootableCorpses[0].Z += 2 },
		"map":             func(s *Service, f *fargodeepLootSession) { f.world.Self.MapId = 1 },
		"outside":         func(s *Service, f *fargodeepLootSession) { f.actions.Position.X = -9600 },
		"own_combat":      func(s *Service, f *fargodeepLootSession) { f.world.Self.UnitFlags |= 0x80000 },
		"owner_combat": func(s *Service, f *fargodeepLootSession) {
			f.world.Owner = &worldv1.PlayerState{Guid: 3, Present: true, UnitFlagsKnown: true, UnitFlags: 0x80000}
		},
		"party_changed": func(s *Service, f *fargodeepLootSession) { f.party.Group.InGroup = false },
		"peer_offline":  func(s *Service, f *fargodeepLootSession) { f.party.Group.Members[1].Online = false },
		"party_body":    func(s *Service, f *fargodeepLootSession) { f.party.SessionId = "replacement" },
		"peer_combat":   func(s *Service, f *fargodeepLootSession) { f.world.NearbyPlayers[0].UnitFlags |= 0x80000 },
		"peer_unknown":  func(s *Service, f *fargodeepLootSession) { f.world.NearbyPlayers[0].UnitFlagsKnown = false },
		"peer_absent":   func(s *Service, f *fargodeepLootSession) { f.world.NearbyPlayers = nil },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := fargodeepLootRig(t)
			if name == "wrong_target" || name == "wrong_phase" || name == "wrong_engager" {
				f.world.SelfQuestLog[0].Complete, f.world.SelfQuestLog[0].State = true, 1
			}
			change(s, f)
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal("unsafe independent loot", current(t, s))
			}
		})
	}
}

func TestFargodeepLootPostStopAndFinalWorldCannotReuseOldAuthority(t *testing.T) {
	for _, moment := range []string{"after_stop", "final_world"} {
		for _, mode := range []string{"quest", "target", "tap", "party", "combat"} {
			t.Run(moment+"_"+mode, func(t *testing.T) {
				s, f, _ := fargodeepLootRig(t)
				change := func(w *worldv1.SnapshotResponse) {
					switch mode {
					case "quest":
						w.SelfQuestLog[0].QuestId = 0
					case "target":
						w.LootableCorpses[0].Guid++
					case "tap":
						w.LootableCorpses[0].DynamicFlags = 1
					case "party":
						f.party.Group.InGroup = false
					case "combat":
						w.Self.UnitFlags |= 0x80000
					}
				}
				if moment == "after_stop" {
					f.afterAcquire = func() { change(f.world) }
				} else {
					f.onSnapshot = func(n int, w *worldv1.SnapshotResponse) {
						if n == 4 {
							change(w)
						}
					}
					// Party is checked before the final world read; a change in
					// that last response cannot be queried retroactively. Test the
					// actual earlier exact-token callback for that separate source.
					if mode == "party" {
						f.afterRenew = func() { f.party.Group.InGroup = false }
					}
				}
				enable(t, s)
				s.Tick(context.Background())
				if len(f.packets) != 0 || f.leaseOwner != "" {
					t.Fatal("stale final authority opened loot", f.snapshotReads, current(t, s))
				}
			})
		}
	}
}

func TestFargodeepOwnItemsNeedPushSlotRemovalAndReleaseEvenWhenLootableClears(t *testing.T) {
	s, f, _ := fargodeepLootRig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(fargodeepCorpseGUID, 0, lootItem{slot: 2, id: 773, count: 1}))
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("write claimed Gold Dust")
	}
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	emit(s, opcode.SMSGItemPushResult, pushPacket(4, 773, 1))
	f.world.LootableCorpses[0].DynamicFlags = 8
	f.world.LootableCorpses = nil // It remains a real observed corpse in NearbyCreatures.
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("peer dust claimed own receipt")
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 773, 1))
	s.Tick(context.Background())
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 1 || f.packets[len(f.packets)-1].Opcode != opcode.CMSGLootRelease {
		t.Fatal("last-slot LOOTABLE clearing aborted actual receipt", current(t, s))
	}
	r := guidPacket(fargodeepCorpseGUID)
	r = append(r, 1)
	emit(s, opcode.SMSGLootReleaseResponse, r)
	s.Tick(context.Background())
	if f.leaseOwner != "" || current(t, s).ReleasesObserved != 1 || s.questFargodeep || current(t, s).Phase != "idle" {
		t.Fatal("bound release not confirmed", current(t, s))
	}
}

func TestFargodeepApproachRechecksBeforeTheLootOpen(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s, f, advance := fargodeepLootRig(t)
		f.world.LootableCorpses[0].X = -9790
		if fail {
			f.afterApproach = func() { f.world.SelfQuestLog[0].QuestId = 7 }
		}
		enable(t, s)
		s.Tick(context.Background())
		if len(f.packets) != 0 || current(t, s).Phase != "approaching" {
			t.Fatal(current(t, s))
		}
		advance(200 * time.Millisecond)
		s.Tick(context.Background())
		if fail {
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal(current(t, s))
			}
		} else if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot {
			t.Fatal(current(t, s))
		}
	}
}
