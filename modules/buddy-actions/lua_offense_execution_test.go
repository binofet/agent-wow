package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

type combatExecutionFixture struct {
	t       *testing.T
	s       *Service
	f       *fakeSession
	vm      *behavior.Session
	ctx     context.Context
	advance func(time.Duration)
	pending uint64
}

func newCombatExecutionFixture(t *testing.T, catalog string, learned ...uint32) *combatExecutionFixture {
	t.Helper()
	s, f, _, advance := exactOffenseFixture(t)
	data, err := spellcatalog.New([]byte(catalog))
	if err != nil {
		t.Fatal(err)
	}
	s.spellData = data
	learnSpells(s, learned...)
	npc := f.world.RequestedCreature
	npc.X = 10
	npc.AttackVictimGuid = s.selfGUID
	npc.TargetKnown, npc.TargetGuid = true, s.selfGUID
	npc.UnitFlags = 0x80000
	npc.DynamicFlagsKnown = true
	f.world.NearbyCreatures = []*worldv1.CreatureState{npc}
	f.world.Self.UnitFlags = 0x80000
	s.navDataDir = "/fixture/clear-flat-ground"
	// This fixture's declared geometry is a clear flat casting lane. The real
	// approach primitive still checks its visit, actor pose, range and facing.
	s.crowdControlSight = func(context.Context, string, uint32, position, position) (bool, error) { return true, nil }
	activateEncounterWriter(t, s, f)
	source, err := exec.Command("python3", "../../dev/encounter-bundle").Output()
	if err != nil {
		t.Fatal(err)
	}
	vm, err := behavior.NewSessionWithOptions(context.Background(), s.sessionID, string(source), behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	ctx := modcb.WithPolicySpell(context.Background(), testEncounterKey)
	ctx = modcb.WithPolicyEncounterAccess(ctx, testEncounterKey, 1)
	md, _ := metadata.FromOutgoingContext(ctx)
	return &combatExecutionFixture{t: t, s: s, f: f, vm: vm, ctx: metadata.NewIncomingContext(context.Background(), md), advance: advance}
}

func (x *combatExecutionFixture) decide() behavior.Decision {
	x.t.Helper()
	if _, err := x.s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1)); err != nil {
		x.t.Fatal(err)
	}
	observed, err := x.s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if err != nil {
		x.t.Fatal(err)
	}
	raw, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(observed)
	if err != nil {
		x.t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		x.t.Fatal(err)
	}
	seq, err := x.vm.Enqueue(behavior.Event{Kind: "encounter_observation", Value: value})
	if err != nil {
		x.t.Fatal(err)
	}
	decision, err := x.vm.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": strconv.FormatUint(seq, 10)})
	if err != nil {
		x.t.Fatal(err)
	}
	return decision
}

// The shared script chooses every target/rank/approach. This test adapter only
// invokes its exact public primitive and feeds the resulting native evidence.
func (x *combatExecutionFixture) cast() behavior.Decision {
	x.t.Helper()
	for range 4 {
		decision := x.decide()
		a, _ := decision.Output["action"].(map[string]any)
		if a == nil {
			x.t.Fatal("shared policy produced no action", decision.Output)
		}
		target, _ := strconv.ParseUint(a["targetGuid"].(string), 10, 64)
		id, err := x.vm.ReserveAction(decision)
		if err != nil {
			x.t.Fatal(err)
		}
		switch a["capability"] {
		case "approach_target":
			r := &actionsv1.ApproachTargetRequest{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, TargetGuid: target, VisitId: 1, RangeYards: float32(a["rangeYards"].(float64))}
			r.RequireLos, _ = a["requireLos"].(bool)
			r.Behind, _ = a["behind"].(bool)
			if _, err = x.s.ApproachTarget(x.ctx, r); err != nil {
				x.t.Fatal("Lua-selected approach refused", a, err)
			}
			_, err = x.vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: x.s.sessionID, ActionID: id, Outcome: behavior.ActionSettledUnverified, Payload: map[string]any{"capability": "approach_target", "target_guid": a["targetGuid"]}}})
			if err != nil {
				x.t.Fatal(err)
			}
		case "offense":
			r := &actionsv1.CastOffenseRequest{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, TargetGuid: target, SpellId: uint32(a["spellId"].(float64))}
			before := countPackets(x.f, opcode.CMSGCastSpell)
			out, err := x.s.CastOffense(x.ctx, r)
			if err != nil || !out.GetLastSpellOperation().GetTransportWritten() || countPackets(x.f, opcode.CMSGCastSpell) != before+1 || lastCast(x.f) != r.SpellId {
				x.t.Fatal("exact selected Lua cast failed", decision.Output, out, err)
			}
			_, err = x.vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: x.s.sessionID, ActionID: id, Outcome: behavior.ActionTransportAccepted, Payload: map[string]any{"capability": "offense", "target_guid": a["targetGuid"], "spell_id": a["spellId"]}}})
			if err != nil {
				x.t.Fatal(err)
			}
			x.pending = id
			return decision
		default:
			x.t.Fatal("expected shared Lua movement or offense", decision.Output)
		}
	}
	x.t.Fatal("shared Lua never progressed from approach to exact cast")
	return behavior.Decision{}
}

func (x *combatExecutionFixture) confirmDamage() {
	x.t.Helper()
	spell, target, count := x.s.pendingSpell, x.s.pendingTarget, x.s.castCount
	packet := smiteEvent(true, count, target)
	_, _, _, at, _ := castEvent(packet)
	binary.LittleEndian.PutUint32(packet[at-4:], spell)
	emitPolymorph(x.s, opcode.SMSGSpellGo, packet)
	emitPolymorph(x.s, opcode.SMSGSpellnonmeleedamagelog, exactOffenseDamageLog(x.s.selfGUID, target, spell, 10))
	view := state(x.t, x.s).GetLastSpellOperation()
	if !view.GetServerCastGo() || !view.GetEffectObserved() || view.GetPhase() != "confirmed_effect" {
		x.t.Fatal("native cast/effect was not actually correlated", view)
	}
	_, err := x.vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: x.s.sessionID, ActionID: x.pending, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "offense", "target_guid": strconv.FormatUint(target, 10), "spell_id": float64(spell), "effect_observed": true}}})
	if err != nil {
		x.t.Fatal(err)
	}
	x.pending = 0
	x.advance(4 * time.Second)
}

const combatReplayCatalog = `{"id":585,"family":585,"rank":1,"level":1,"name":"Filler","timingKnown":true,"rangeKnown":true,"itemClass":-1,"maxHostile":30,"cost":10,"castMs":3000,"gcdMs":1500,"effects":[{"kind":2,"a":6,"points":40}]}
{"id":591,"family":585,"rank":2,"level":6,"name":"Filler rank2","timingKnown":true,"rangeKnown":true,"itemClass":-1,"maxHostile":30,"cost":20,"castMs":3000,"gcdMs":1500,"effects":[{"kind":2,"a":6,"points":80}]}
{"id":424242,"family":424242,"rank":1,"level":1,"name":"Burst","timingKnown":true,"rangeKnown":true,"itemClass":-1,"maxHostile":30,"cost":20,"castMs":0,"gcdMs":1500,"cooldownMs":30000,"effects":[{"kind":2,"a":6,"points":100}]}
`

func TestSharedLuaOffenseExecutesAfterOwnerDeathControlRecoveryAndUrgentFinish(t *testing.T) {
	x := newCombatExecutionFixture(t, combatReplayCatalog, 585, 424242)
	x.f.world.Owner.Health, x.f.world.Owner.Alive = 0, false
	x.f.world.RequestedCreature.Health, x.f.world.RequestedCreature.MaxHealth = 20, 1000
	x.f.world.Self.UnitFlags |= 0x00800000
	if a, _ := x.decide().Output["action"].(map[string]any); a["capability"] == "offense" {
		x.t.Fatal("feared body claimed a cast", a)
	}
	x.f.world.Self.UnitFlags &^= 0x00800000
	decision := x.cast()
	a := decision.Output["action"].(map[string]any)
	if a["spellId"] != float64(424242) || decision.Output["burstIntent"] == nil {
		x.t.Fatal("same queue did not drive urgent burst", decision.Output)
	}
	x.confirmDamage()
}

func TestSharedLuaRetunesKnownOffenseRankInSameBodyAndNativeAddsNoReserve(t *testing.T) {
	x := newCombatExecutionFixture(t, combatReplayCatalog, 585, 591)
	x.f.world.RequestedCreature.Health, x.f.world.RequestedCreature.MaxHealth = 300, 500
	first := x.cast().Output["action"].(map[string]any)
	if first["spellId"] != float64(591) {
		x.t.Fatal("highest available learned rank missing", first)
	}
	x.confirmDamage()
	source, err := exec.Command("python3", "../../dev/encounter-bundle").Output()
	if err != nil {
		x.t.Fatal(err)
	}
	tuned := strings.Replace(string(source), "return ar > br", "return ar < br", 1)
	tuned = strings.Replace(tuned, "[5]={mana_reserve_fraction=0.15}", "[5]={mana_reserve_fraction=0}", 1)
	before := x.vm.Status()
	if _, err = x.vm.Reload(context.Background(), tuned, before.Revision); err != nil {
		x.t.Fatal(err)
	}
	x.f.world.Self.Power = 10 // Exact known rank cost, no hidden native healing reserve.
	second := x.cast().Output["action"].(map[string]any)
	if second["spellId"] != float64(585) || x.vm.Status().SessionID != before.SessionID || x.vm.Status().Revision == before.Revision {
		x.t.Fatal("same-session Lua rank tuning failed", second, x.vm.Status())
	}
	x.confirmDamage()
}

func TestSharedLuaDefenseClassifiesRawAttacksWithoutAnOwnerOrLegacyAssist(t *testing.T) {
	for _, mode := range []string{"self", "party", "owner_dead", "afk_owner", "stale", "foreign_victim", "selected_only", "protected", "dead_target", "overlevel"} {
		t.Run(mode, func(t *testing.T) {
			x := newCombatExecutionFixture(t, combatReplayCatalog, 585)
			c := x.f.world.RequestedCreature
			x.s.state.Assisting, x.s.state.Following = false, false
			x.f.party.Group.GroupGuid, x.f.party.Group.LeaderGuid = 77, ownerGUID
			x.f.world.Owner.Health = 100
			want := true
			switch mode {
			case "self":
				x.f.world.Owner = nil
				x.f.party = nil
			case "party":
				c.AttackVictimGuid, c.TargetGuid = ownerGUID, ownerGUID
				x.f.world.Owner.UnitFlags = 0x80000
			case "owner_dead":
				x.f.world.Owner.Health, x.f.world.Owner.Alive = 0, false
			case "afk_owner":
				x.f.world.Owner.PlayerFlags = 2
			case "stale":
				c.AttackAgeMs = 90000
				c.TargetKnown = false
				c.UnitFlags = 0
				x.f.world.Self.UnitFlags = 0
				want = false
			case "foreign_victim":
				c.AttackVictimGuid, c.TargetGuid = 99, 99
				want = false
			case "selected_only":
				c.AttackVictimKnown = false
				want = false
			case "protected":
				emitPolymorph(x.s, opcode.SMSGAuraUpdate, sheepAura(c.Guid, 9, 118, 20000))
				want = false
			case "dead_target":
				c.Alive, c.Health = false, 0
				want = false
			case "overlevel":
				x.f.world.Self.LevelKnown, x.f.world.Self.Level = true, 5
				c.LevelKnown, c.Level = true, 80
			}
			decision := x.decide()
			action, _ := decision.Output["action"].(map[string]any)
			selected := action["capability"] == "offense" || action["capability"] == "approach_target"
			if selected != want || selected && action["targetGuid"] != strconv.FormatUint(c.Guid, 10) {
				t.Fatal("shared queue used invalid attack evidence or lost defense", mode, decision.Output)
			}
			x.s.Tick(context.Background())
			if countPackets(x.f, opcode.CMSGCastSpell) != 0 || countPackets(x.f, opcode.CMSGAttackswing) != 0 {
				t.Fatal("native maintenance selected offense", mode, x.f.packets)
			}
		})
	}
}

func TestSharedLuaQueueFinishesThreatBeforeNearestEnemyAndPreservesProtection(t *testing.T) {
	x := newCombatExecutionFixture(t, combatReplayCatalog, 585)
	far := x.f.world.RequestedCreature
	far.Health, far.MaxHealth, far.X = 10, 1000, 15
	near := proto.Clone(far).(*worldv1.CreatureState)
	near.Guid++
	near.Health, near.X = 1000, 2
	x.f.world.NearbyCreatures = append(x.f.world.NearbyCreatures, near)
	decision := x.decide()
	action, _ := decision.Output["action"].(map[string]any)
	if action["targetGuid"] != strconv.FormatUint(far.Guid, 10) {
		t.Fatal("distance or GUID replaced urgent finishing policy", decision.Output)
	}
	emitPolymorph(x.s, opcode.SMSGAuraUpdate, sheepAura(far.Guid, 9, 118, 20000))
	action, _ = x.decide().Output["action"].(map[string]any)
	if action["targetGuid"] != strconv.FormatUint(near.Guid, 10) {
		t.Fatal("urgent finisher broke CC or abandoned other live threat", action)
	}
}
