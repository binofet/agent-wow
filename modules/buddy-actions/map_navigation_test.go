package buddyactions

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// These distant zones/maps deliberately share only the controller contract.
// The fake planner proves map routing; real asset acceptance belongs to pkg/buddynav.
func TestProductionFollowUsesActualMapWithoutRegionalFence(t *testing.T) {
	for _, mapID := range []uint32{0, 1, 530, 571, 33} {
		t.Run(string(rune('A'+mapID%26)), func(t *testing.T) {
			s, f, advance := formationRig(t, 4, 8)
			f.world.Self.MapId, f.world.Owner.MapId = mapID, mapID
			f.world.Self.X, f.world.Self.Y, f.world.Self.Z = 1500, 1500, 80
			f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = 1515, 1500, 80
			calls := 0
			s.planMapPath = func(_ context.Context, _ string, got uint32, a, b position) ([]position, error) {
				if got != mapID {
					t.Fatalf("planner map=%d want=%d", got, mapID)
				}
				calls++
				return []position{a, b}, nil
			}
			s.planPath = func(context.Context, string, position, position) ([]position, error) {
				t.Fatal("used regional planner")
				return nil, nil
			}
			toggle(t, s, true)
			for range 6 {
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
			}
			if calls == 0 || !s.state.Moving || s.posMap != mapID || s.pos.x <= 1500 {
				t.Fatal(mapID, s.stateLocked(), s.pos)
			}
		})
	}
}

func TestMapPlannerRejectsMalformedRoutesAndNeverUsesLegacyFallback(t *testing.T) {
	s, _, _, target := followNavRig(t)
	s.pos = position{x: 1500, y: 1500, z: 80}
	target = s.pos
	target.x += 10
	for _, mode := range []string{"missing", "error", "nan", "long", "points", "endpoint"} {
		t.Run(mode, func(t *testing.T) {
			s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
				switch mode {
				case "missing", "error":
					return nil, errors.New("installed map data unavailable")
				case "nan":
					return []position{s.pos, {x: float32(math.NaN())}, target}, nil
				case "long":
					return []position{s.pos, {x: s.pos.x + 130, y: s.pos.y + 100, z: s.pos.z}, target}, nil
				case "points":
					p := make([]position, 257)
					for i := range p {
						p[i] = s.pos
					}
					p[256] = target
					return p, nil
				default:
					return []position{s.pos, {x: target.x - 3, y: target.y, z: target.z}}, nil
				}
			}
			if mode == "missing" {
				s.planMapPath = nil
			}
			s.planPath = func(context.Context, string, position, position) ([]position, error) {
				t.Fatal("regional fallback")
				return nil, nil
			}
			if _, err := s.mapNavigationPath(context.Background(), "/client", 571, s.pos, target); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
	if !validMapNavigationPath([]position{s.pos, target}, s.pos, target) {
		t.Fatal("valid distant map route rejected")
	}
}

func TestPerWriteMapBindingRejectsTransferDuringClockAndRebasesStop(t *testing.T) {
	for _, mode := range []string{"map", "revision", "owner_map"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, target := followNavRig(t)
			s.state.Following = true
			f.onClock = func() {
				f.onClock = nil
				switch mode {
				case "map":
					f.world.Self.MapId = 1
					f.world.Self.X = 2000
					f.world.Self.PositionRevision++
				case "revision":
					f.world.Self.X += 30
					f.world.Self.PositionRevision++
				case "owner_map":
					f.world.Owner.MapId = 530
				}
			}
			if s.followFormationStepLocked(context.Background(), target, .8) || len(f.packets) != 0 || s.state.Moving {
				t.Fatal("stale START", mode, s.stateLocked(), f.packets)
			}
			if mode == "map" && (s.posMap != 1 || s.pos.x != 2000) {
				t.Fatal("transfer not rebased", s.pos, s.posMap)
			}
		})
	}
	s, f, _, target := followNavRig(t)
	if !s.followFormationStepLocked(context.Background(), target, .8) {
		t.Fatal("start")
	}
	f.onClock = func() {
		f.onClock = nil
		f.world.Self.MapId = 571
		f.world.Self.X = 2300
		f.world.Self.PositionRevision++
	}
	if !s.stopLocked(context.Background()) || s.posMap != 571 || s.pos.x != 2300 || s.state.Moving || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("STOP used old map", s.stateLocked(), f.packets)
	}
	_, n, _ := readGUID(f.packets[len(f.packets)-1].Payload)
	data := f.packets[len(f.packets)-1].Payload[n:]
	if read := math.Float32frombits(uint32(data[10]) | uint32(data[11])<<8 | uint32(data[12])<<16 | uint32(data[13])<<24); read != 2300 {
		t.Fatal("STOP payload old coordinates", read)
	}
}

func TestGenericTravelUsesObservedNPCMapAndRejectsTransferDuringPlanning(t *testing.T) {
	for _, trainer := range []bool{false, true} {
		for _, change := range []bool{false, true} {
			s, f, advance, r := travelRig(t)
			if trainer {
				s, f, advance, r = trainerTravelRig(t)
			}
			_ = advance
			f.world.Self.MapId = 1
			f.world.Self.X, f.world.Self.Y, f.world.Self.Z = 1500, 1500, 80
			var npc *worldv1.CreatureState
			if trainer {
				npc = f.world.NearbyCreatures[0]
			} else {
				npc = f.world.QuestGivers[0]
			}
			npc.MapId = 1
			npc.X, npc.Y, npc.Z = 1510, 1500, 80
			called := 0
			s.planMapPath = func(_ context.Context, _ string, mapID uint32, a, b position) ([]position, error) {
				if mapID != 1 {
					t.Fatal("wrong map", mapID)
				}
				called++
				if change {
					f.world.Self.MapId = 530
					f.world.Self.X = 2000
					f.world.Self.PositionRevision++
				}
				return []position{a, b}, nil
			}
			var got *actionsv1.State
			var err error
			if trainer {
				got, err = s.TravelTrainer(context.Background(), r)
			} else {
				got, err = s.TravelQuest(context.Background(), r)
			}
			if err != nil || called != 1 || got.Moving {
				t.Fatal(trainer, change, got, err, called)
			}
			if change && len(f.packets) != 0 {
				t.Fatal("moved after transfer", f.packets)
			}
			if !change && countPackets(f, opcode.MSGMoveStartForward) == 0 {
				t.Fatal("generic travel did not move", got)
			}
		}
	}
}

func TestGenericNearbyQuestAndLootApproachUseActualMap(t *testing.T) {
	s, f, _, r := navLootRig(t)
	f.world.Self.MapId, f.world.Owner.MapId = 530, 530
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = 1500, 1500, 80
	f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = 1505, 1500, 80
	corpse := f.world.LootableCorpses[0]
	corpse.MapId = 530
	corpse.X, corpse.Y, corpse.Z = 1507, 1500, 80
	s.planMapPath = func(_ context.Context, _ string, mapID uint32, a, b position) ([]position, error) {
		if mapID != 530 {
			t.Fatal(mapID)
		}
		return []position{a, b}, nil
	}
	got, err := s.ApproachLoot(context.Background(), r)
	if err != nil || !got.Moving || !got.NavAvailable {
		t.Fatal(got, err)
	}

	s, f, _, qr := navQuestApproachRig(t)
	f.world.Self.MapId = 571
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = 1500, 1500, 80
	npc := f.world.QuestGivers[0]
	npc.MapId = 571
	npc.X, npc.Y, npc.Z = 1507, 1500, 80
	s.planMapPath = func(_ context.Context, _ string, mapID uint32, a, b position) ([]position, error) {
		if mapID != 571 {
			t.Fatal(mapID)
		}
		return []position{a, b}, nil
	}
	got, err = s.ApproachQuest(context.Background(), qr)
	if err != nil || got.Moving || !got.NavAvailable || countPackets(f, opcode.MSGMoveStartForward) == 0 {
		t.Fatal(got, err, f.packets)
	}
}

func TestProductionMissingDataNeverFallsBackToDirectMovement(t *testing.T) {
	s, f, _ := rig(t)
	s.planMapPath = planMapNavigation
	toggle(t, s, true)
	s.Tick(context.Background())
	if s.state.Moving || s.state.NavAvailable || len(f.packets) != 0 || s.state.Reason != "follow_navigation_unavailable" {
		t.Fatal(s.stateLocked(), f.packets)
	}
	s, f, _, r := questApproachRig(t)
	s.planMapPath = planMapNavigation
	got, err := s.ApproachQuest(context.Background(), r)
	if err != nil || got.Moving || len(f.packets) != 0 || got.NavAvailable {
		t.Fatal(got, err, f.packets)
	}
}

func TestTransferPendingNeverWritesOldMapCoordinates(t *testing.T) {
	s, f, advance, target := followNavRig(t)
	if !s.followFormationStepLocked(context.Background(), target, .8) {
		t.Fatal("start")
	}
	advance(200 * time.Millisecond)
	f.onClock = func() {
		f.onClock = nil
		f.world.Self.Present = false
		f.world.Self.MapKnown = false
		f.world.Self.PositionKnown = false
	}
	if s.followFormationStepLocked(context.Background(), target, .8) || len(f.packets) != 1 || !s.state.MotionUnconfirmed || s.state.Moving {
		t.Fatal("wrote into transfer", s.stateLocked(), f.packets)
	}
}

func TestRegionalPracticeStillRejectsSameCoordinatesOnOtherMaps(t *testing.T) {
	s, f, _ := coldridgeRig(t)
	f.world.Self.MapId = 1
	f.world.NearbyCreatures[0].MapId = 1
	s.Tick(context.Background())
	if s.state.Practicing || len(f.packets) != 0 {
		t.Fatal("regional practice escaped map zero", s.stateLocked(), f.packets)
	}
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil {
		t.Fatal("enabled regional practice on another map")
	}
}
