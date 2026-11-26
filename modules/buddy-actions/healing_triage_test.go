package buddyactions

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func triagePlayer(f *fakeSession, guid uint64, health uint32) *worldv1.PlayerState {
	p := proto.Clone(f.world.Owner).(*worldv1.PlayerState)
	p.Guid, p.Health, p.X = guid, health, 15
	return p
}
func triageMember(f *fakeSession, guid uint64) {
	f.party.Group.Members = append(f.party.Group.Members, &partyv1.Member{Guid: guid, Online: true})
}
func healingCastTarget(f *fakeSession) uint64 {
	for i := len(f.packets) - 1; i >= 0; i-- {
		p := f.packets[i]
		if p.Opcode == opcode.CMSGCastSpell && len(p.Payload) > 10 {
			guid, _, _ := readGUID(p.Payload[10:])
			return guid
		}
	}
	return 0
}

// Existing packet fixtures explicitly select intent. Refusals still yield the
// observed State for packet/correlation assertions, not a replacement cast.
func testExactHeal(s *Service, spell uint32, target uint64) (*actionsv1.State, error) {
	if s.sessionID == "" {
		s.sessionID = "actions-test-body"
	}
	out, err := s.CastHeal(context.Background(), &actionsv1.CastHealRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: target, SpellId: spell})
	if err != nil {
		return s.Status(context.Background(), &emptypb.Empty{})
	}
	return out, nil
}

// Exercise the shipped editable selector, never a Go translation of triage.
func healingVM(t *testing.T, s *Service, source string) *behavior.Session {
	t.Helper()
	if s.sessionID == "" {
		s.sessionID = "actions-test-body"
		if fake, ok := s.session.(*fakeSession); ok && fake.party.GetSessionId() != "" {
			s.sessionID = fake.party.SessionId
		}
	}
	if source == "" {
		b, e := os.ReadFile("../../behaviors/healing.lua")
		if e != nil {
			t.Fatal(e)
		}
		source = string(b)
	}
	vm, e := behavior.NewSessionWithOptions(context.Background(), s.sessionID, source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(vm.Close)
	return vm
}
func luaHealingAction(t *testing.T, s *Service, vm *behavior.Session, explicit bool) map[string]any {
	t.Helper()
	if explicit {
		if _, e := vm.Enqueue(behavior.Event{Kind: "healing_request"}); e != nil {
			t.Fatal(e)
		}
	}
	view, e := s.HealingOptions(context.Background(), &emptypb.Empty{})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := protojson.Marshal(view)
	if e != nil {
		t.Fatal(e)
	}
	var value map[string]any
	if e = json.Unmarshal(raw, &value); e != nil {
		t.Fatal(e)
	}
	seq, e := vm.Enqueue(behavior.Event{Kind: "healing_observation", Value: value})
	if e != nil {
		t.Fatal(e)
	}
	out, e := vm.Decide(context.Background(), map[string]any{"kind": "healing", "observationSequence": strconv.FormatUint(seq, 10)})
	if e != nil {
		t.Fatal(e)
	}
	action, _ := out.Output["action"].(map[string]any)
	return action
}
func runLuaHealing(t *testing.T, s *Service, explicit bool) map[string]any {
	t.Helper()
	action := luaHealingAction(t, s, healingVM(t, s, ""), explicit)
	if action == nil {
		return nil
	}
	target, e := strconv.ParseUint(action["targetGuid"].(string), 10, 64)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CastHeal(context.Background(), &actionsv1.CastHealRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: target, SpellId: uint32(action["spellId"].(float64))}); e != nil {
		t.Fatal(e)
	}
	return action
}

func TestLuaHealingOwnsSelfAllyUrgencyAndRank(t *testing.T) {
	for _, mode := range []string{"solo", "independent", "ally", "urgent_self", "ratio", "tie", "manual99"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			learn(s)
			f.world.Self.Health, f.world.Owner.Health = 60, 20
			p := triagePlayer(f, 4, 15)
			f.world.NearbyPlayers = []*worldv1.PlayerState{p}
			triageMember(f, 4)
			want := uint64(4)
			switch mode {
			case "solo":
				f.world.Owner, f.world.NearbyPlayers, f.party = nil, nil, nil
				want = 1
			case "independent":
				s.independent = true
				f.world.Self.Health = 30
				want = 1
			case "ally":
				f.party = nil
				p.RaceKnown, p.RaceId = true, 1
				f.world.Self.RaceKnown, f.world.Self.RaceId = true, 1
				f.world.Owner.Health = 100
			case "urgent_self":
				f.world.Self.Health = 20
				want = 1
			case "ratio":
				f.world.Owner.Health, f.world.Owner.MaxHealth = 25, 200
				want = 3
			case "tie":
				f.world.Owner.Health, p.Health = 50, 50
				want = 3
			case "manual99":
				f.world.Self.Health, f.world.Owner.Health = 100, 99
				f.world.NearbyPlayers = nil
				want = 3
			}
			action := runLuaHealing(t, s, mode == "manual99")
			if action == nil || healingCastTarget(f) != want || state(t, s).HealAttempts != 1 {
				t.Fatalf("Lua intent not executed: %+v %s", action, state(t, s))
			}
		})
	}
}

func TestNativeTickNeverChoosesHealingPatientOrRank(t *testing.T) {
	for _, independent := range []bool{false, true} {
		s, f, advance := rig(t)
		learn(s)
		s.independent = independent
		f.world.Self.Health = 10
		for _, elapsed := range []time.Duration{0, 5 * time.Hour} {
			advance(elapsed)
			s.Tick(context.Background())
		}
		if countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.HealAttempts != 0 {
			t.Fatal("native Tick chose healing intent", s.stateLocked())
		}
		if _, err := s.HealOnce(context.Background(), &emptypb.Empty{}); err == nil {
			t.Fatal("unbound module invented a Lua host")
		}
	}
}

func TestLuaHealingRetunesLearnedRanksInSameSession(t *testing.T) {
	s, f, _ := rig(t)
	learnSpells(s, 2050, 2052)
	f.world.Self.BaseManaKnown, f.world.Self.BaseMana = true, 100
	f.world.Self.Power = 100
	f.world.Owner.Health = 60
	source, e := os.ReadFile("../../behaviors/healing.lua")
	if e != nil {
		t.Fatal(e)
	}
	vm := healingVM(t, s, string(source))
	before := luaHealingAction(t, s, vm, false)
	if before["spellId"] != float64(2050) {
		t.Fatal("expected efficient lower rank", before)
	}
	// Keep a larger deficit for a meaningful larger-rank choice.
	f.world.Owner.MaxHealth, f.world.Owner.Health = 400, 220
	tuned := strings.Replace(string(source), `rank_mode="effective_per_resource"`, `rank_mode="largest"`, 1)
	sid := vm.Status().SessionID
	if _, e = vm.Reload(context.Background(), tuned, vm.Status().Revision); e != nil {
		t.Fatal(e)
	}
	after := luaHealingAction(t, s, vm, false)
	if after["spellId"] != float64(2052) || vm.Status().SessionID != sid {
		t.Fatal("current same-body Lua did not choose tuned rank", after)
	}
	f.world.Self.Power = 0
	if a := luaHealingAction(t, s, vm, false); a != nil {
		t.Fatal("script created mana", a)
	}
}

func TestExactHealRechecksWithoutRetargetingAndAllowsLivingRecovery(t *testing.T) {
	for _, mode := range []string{"range", "offline", "self_injured", "recovery"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			learn(s)
			s.sessionID = "actions-test-body"
			f.world.Owner.Health = 50
			if mode == "recovery" {
				s.recovery.state.Active = true
			} else {
				toggle(t, s, true)
				f.world.Owner.Health = 100
				s.Tick(context.Background())
				f.world.Owner.Health = 50
				f.onClock = func() {
					switch mode {
					case "range":
						f.world.Owner.X = 100
					case "offline":
						f.party.Group.Members[1].Online = false
					case "self_injured":
						f.world.Self.Health = 1
					}
				}
			}
			_, err := s.CastHeal(context.Background(), &actionsv1.CastHealRequest{SessionId: s.sessionID, SelfGuid: 1, TargetGuid: 3, SpellId: 2050})
			if mode == "range" || mode == "offline" {
				if err == nil || countPackets(f, opcode.CMSGCastSpell) != 0 {
					t.Fatal("stale intent was cast", err, s.stateLocked())
				}
			} else if err != nil || healingCastTarget(f) != 3 {
				t.Fatal("exact intent was substituted or living recovery blocked", err, s.stateLocked())
			}
		})
	}
}

// Existing interaction/movement packet fixtures now run the actual shared Lua
// healer around native mechanical maintenance. No patient/rank choice is
// recreated here, and the production Tick itself stays policy-free.
func tickLuaHealing(t *testing.T, s *Service) {
	t.Helper()
	before := s.state.HealAttempts
	runLuaHealing(t, s, false)
	s.Tick(context.Background())
	if s.state.HealAttempts == before && !s.state.CastPending {
		runLuaHealing(t, s, false)
	}
}
