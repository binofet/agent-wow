package buddyactions

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func holdCombatInteraction(t *testing.T, x *combatExecutionFixture) *actionsv1.InteractionLease {
	t.Helper()
	lease, err := x.s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "trade", LeaseMs: 30000})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}
func releaseLuaInteraction(t *testing.T, x *combatExecutionFixture, d behavior.Decision) {
	t.Helper()
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "release_interaction" {
		t.Fatal("Lua did not choose exact access handoff", d.Output)
	}
	id, err := x.vm.ReserveAction(d)
	if err != nil {
		t.Fatal(err)
	}
	visit, _ := strconv.ParseUint(a["visitId"].(string), 10, 64)
	_, err = x.s.ReleaseInteraction(x.ctx, &actionsv1.InteractionRelease{Owner: a["owner"].(string), Token: a["token"].(string), SessionId: a["sessionId"].(string), SelfGuid: x.s.selfGUID, VisitId: visit})
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: x.s.sessionID, ActionID: id, Outcome: behavior.ActionSettledUnverified, Payload: map[string]any{"capability": "release_interaction"}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSharedLuaInteractionDefenseOwnsPreemptionThenExactOffense(t *testing.T) {
	x := newCombatExecutionFixture(t, combatReplayCatalog, 585)
	x.f.world.Owner.Health, x.f.world.Owner.Alive = 0, false
	lease := holdCombatInteraction(t, x)
	// An observed attacker cannot make native Tick seize custody, even with no
	// legacy assist/follow control and an absent/dead owner.
	before := len(x.f.packets)
	x.s.Tick(context.Background())
	if x.s.state.InteractionOwner != "trade" || x.s.interactionToken != lease.Token || len(x.f.packets) != before {
		t.Fatal("native defense still chooses interaction preemption", x.s.stateLocked(), x.f.packets)
	}
	x.s.state.Moving, x.s.state.Traveling = true, true
	releaseLuaInteraction(t, x, x.decide())
	if x.s.state.InteractionOwner != "" || x.s.state.Traveling || x.s.state.Moving || countPackets(x.f, opcode.MSGMoveStop) != 1 {
		t.Fatal("selected handoff failed to settle its exact movement", x.s.stateLocked(), x.f.packets)
	}
	x.cast()
	x.confirmDamage()
}

func TestSharedLuaInteractionKeepsFuturePackAndRetunesSameSession(t *testing.T) {
	x := newCombatExecutionFixture(t, combatReplayCatalog, 585)
	lease := holdCombatInteraction(t, x)
	c := x.f.world.RequestedCreature
	c.AttackVictimKnown, c.TargetKnown = false, false
	c.UnitFlags = 0
	x.f.world.Self.UnitFlags = 0
	d := x.decide()
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] == "release_interaction" || x.s.interactionToken != lease.Token {
		t.Fatal("untouched future target stole interaction", d.Output)
	}
	// An accepted same-body policy edit, not a native switch, owns whether a
	// witnessed encounter should preempt this lease.
	source, err := exec.Command("python3", "../../dev/encounter-bundle").Output()
	if err != nil {
		t.Fatal(err)
	}
	tuned := strings.Replace(string(source), "yield_for_engagement=true", "yield_for_engagement=false", 1)
	if tuned == string(source) {
		t.Fatal("missing authored tuning")
	}
	if _, err = x.vm.Reload(context.Background(), tuned, x.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	c.AttackVictimKnown, c.AttackVictimGuid, c.TargetKnown, c.TargetGuid = true, x.s.selfGUID, true, x.s.selfGUID
	c.UnitFlags = 0x80000
	x.f.world.Self.UnitFlags = 0x80000
	d = x.decide()
	a, _ = d.Output["action"].(map[string]any)
	if a["capability"] == "release_interaction" || x.s.interactionToken != lease.Token {
		t.Fatal("native policy overrode accepted tuning", d.Output)
	}
	if _, err = x.vm.Reload(context.Background(), string(source), x.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	releaseLuaInteraction(t, x, x.decide())
}

func TestSharedLuaInteractionYieldsForActualBusyHealingFacts(t *testing.T) {
	x := newCombatExecutionFixture(t, combatReplayCatalog, 585)
	x.s.spellData = nil // Use actual learned heal catalog metadata.
	x.s.applySpellbookLocked(map[uint32]bool{2050: true})
	c := x.f.world.RequestedCreature
	c.AttackVictimKnown, c.TargetKnown = false, false
	c.UnitFlags = 0
	x.f.world.Self.UnitFlags, x.f.world.Owner.UnitFlags = 0, 0
	x.f.world.Owner.Health = 20
	holdCombatInteraction(t, x)
	d := x.decide()
	releaseLuaInteraction(t, x, d)
	// A terminal release receipt only frees the ledger. Fresh native facts are
	// required before the same maintained policy may choose its patient/rank.
	d = x.decide()
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] == "approach_target" { // Ordinary measured LoS/range custody.
		target, _ := strconv.ParseUint(a["targetGuid"].(string), 10, 64)
		_, err := x.s.ApproachTarget(x.ctx, &actionsv1.ApproachTargetRequest{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, TargetGuid: target, VisitId: 1, RangeYards: 40, RequireLos: true})
		if err != nil {
			t.Fatal(err)
		}
		d = x.decide()
		a, _ = d.Output["action"].(map[string]any)
	}
	if a["capability"] != "heal" || a["targetGuid"] != strconv.FormatUint(ownerGUID, 10) {
		t.Fatal("quiet injured ally did not regain actual heal access", d.Output)
	}
	target, _ := strconv.ParseUint(a["targetGuid"].(string), 10, 64)
	_, err := x.s.CastHeal(x.ctx, &actionsv1.CastHealRequest{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, TargetGuid: target, SpellId: uint32(a["spellId"].(float64))})
	if err != nil || healingCastTarget(x.f) != ownerGUID || countPackets(x.f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("Lua-selected healing did not dispatch", err, x.f.packets)
	}
}

func TestExactInteractionReleaseFencesIdentityLeaseAndUnknownStop(t *testing.T) {
	for _, mode := range []string{"old_session", "foreign_body", "old_visit", "foreign_token", "clock_refusal", "unknown_stop"} {
		t.Run(mode, func(t *testing.T) {
			x := newCombatExecutionFixture(t, combatReplayCatalog, 585)
			lease := holdCombatInteraction(t, x)
			r := &actionsv1.InteractionRelease{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, VisitId: 1, Owner: "trade", Token: lease.Token}
			switch mode {
			case "old_session":
				r.SessionId += "old"
			case "foreign_body":
				r.SelfGuid++
			case "old_visit":
				r.VisitId++
			case "foreign_token":
				r.Token += "old"
			case "clock_refusal":
				x.f.failClock = true
			case "unknown_stop":
				x.f.failSend = true
			}
			x.s.state.Moving, x.s.state.Traveling = true, true
			before := len(x.f.packets)
			_, err := x.s.ReleaseInteraction(x.ctx, r)
			if status.Code(err) != codes.FailedPrecondition || x.s.interactionToken != lease.Token || x.s.state.InteractionOwner != "trade" || !x.s.state.Traveling {
				t.Fatal("failed release retired custody", mode, err, x.s.stateLocked())
			}
			if mode != "unknown_stop" && (len(x.f.packets) != before || x.s.state.MotionUnconfirmed) {
				t.Fatal("known no-write was sent or made unknown", mode, x.f.packets, x.s.stateLocked())
			}
			if mode == "unknown_stop" {
				if !x.s.state.MotionUnconfirmed {
					t.Fatal("ambiguous STOP lost physical fence")
				}
				x.f.failSend = false
				x.advance(time.Second)
				_, _ = x.s.ReleaseInteraction(x.ctx, r)
				if len(x.f.packets) != before+1 {
					t.Fatal("unconfirmed STOP replayed", x.f.packets)
				}
			}
		})
	}
}
