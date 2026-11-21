package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func practiceApproachRig(t *testing.T, class uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	if class != 5 {
		return practiceRig(t, class)
	}
	s, f, _, _, advance := practiceCasterRig(t, 5)
	learnSpells(s, smite, lesserHeal)
	return s, f, advance
}

func TestPracticeApproachLuaDefaultsKeepActualClassStandoffBoundaries(t *testing.T) {
	for _, test := range []struct {
		class uint32
		yards float32
		move  bool
	}{{4, 3.5, false}, {4, 3.75, true}, {8, 25, false}, {8, 25.25, true}, {5, 25, false}, {5, 25.25, true}} {
		t.Run(fmt.Sprintf("class%d/%g", test.class, test.yards), func(t *testing.T) {
			s, f, _ := practiceApproachRig(t, test.class)
			f.world.NearbyCreatures[0].X = f.world.Self.X + test.yards
			plans := 0
			s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
				plans++
				return []position{a, b}, nil
			}
			s.Tick(context.Background())
			if plans != 1 || !s.state.NavAvailable || s.state.Moving != test.move ||
				countPackets(f, opcode.MSGMoveStartForward) != boolCount(test.move) ||
				countPackets(f, opcode.CMSGCastSpell) != boolCount(!test.move) ||
				s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 {
				t.Fatal("default shared standoff changed class boundary or skipped route validation", s.stateLocked(), plans, f.packets)
			}
		})
	}
}

func TestPracticeApproachLuaPublicCASChangesActualRouteBeforeCastInSameBody(t *testing.T) {
	s, f, advance := practiceApproachRig(t, 8)
	target := f.world.NearbyCreatures[0]
	target.X = f.world.Self.X + 20
	accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource})
	if err != nil {
		t.Fatal(err)
	}
	authored, sid, self, until := s.behavior, s.sessionID, s.selfGUID, s.practiceUntil
	observed := proto.Clone(f.world)
	s.Tick(context.Background())
	if lastCast(f) != fireball || countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.MSGMoveStartForward) != 0 {
		t.Fatal("default25-yard policy did not cast from the observed20-yard direct route", s.stateLocked(), f.packets)
	}
	practiceCasterConfirmObservedCast(t, s, target.Guid)
	request := &actionsv1.BehaviorReloadRequest{SessionId: sid, SelfGuid: self, ExpectedRevision: accepted.Revision,
		Source: editPracticeApproachTuning(t, behaviors.StarterSpellSource, "caster_yards=25", "caster_yards=15")}
	changed, err := reloadStarterSemanticBehavior(t, s, context.Background(), request)
	if err != nil || changed.Revision == accepted.Revision || s.behavior != authored {
		t.Fatal("same-body public CAS did not retain accepted runtime", changed, err)
	}
	request.Source = behaviors.StarterSpellSource
	if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), request); err == nil {
		t.Fatal("stale CAS replaced the accepted tactical edit")
	}
	advance(2 * time.Second) // The actual first cast's shared GCD expires normally.
	s.Tick(context.Background())
	if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 || !s.state.Moving {
		t.Fatal("accepted15-yard tuning did not change real movement at unchanged observations", s.stateLocked(), f.packets)
	}
	for step := 0; step < 12 && countPackets(f, opcode.CMSGCastSpell) == 1; step++ {
		advance(200 * time.Millisecond)
		s.Tick(context.Background())
	}
	start, stop, cast := -1, -1, -1
	for index, packet := range f.packets {
		switch packet.Opcode {
		case opcode.MSGMoveStartForward:
			start = index
		case opcode.MSGMoveStop:
			stop = index
		case opcode.CMSGCastSpell:
			cast = index
		}
	}
	if start < 0 || stop <= start || cast <= stop || countPackets(f, opcode.MSGMoveHeartbeat) == 0 ||
		countPackets(f, opcode.CMSGCastSpell) != 2 || lastCast(f) != fireball || s.state.Moving ||
		distance(s.pos, playerPosition(f.world.Self)) < 5 || distance(s.pos, position{x: target.X, y: target.Y, z: target.Z}) > 15 ||
		s.behavior != authored || s.starterSpells.revision != changed.Revision || s.sessionID != sid || s.selfGUID != self ||
		s.practiceUntil != until || s.state.PracticePulls != 1 || !proto.Equal(observed, f.world) || s.state.ServerAssistGo != 1 || s.state.AssistDamageEvents != 1 {
		t.Fatal("tactical edit skipped verified movement/STOP, changed body, or invented second-cast effects", s.stateLocked(), f.packets)
	}
}

func TestPracticeApproachLuaCannotBypassVerifiedRouteOrBoundedExecutor(t *testing.T) {
	for _, mode := range []string{"assets", "partial", "bent", "elapsed"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := practiceApproachRig(t, 8)
			switch mode {
			case "assets":
				s.navDataDir = ""
			case "partial":
				s.planPath = func(context.Context, string, position, position) ([]position, error) {
					return nil, errors.New("incomplete verified route")
				}
			case "bent", "elapsed":
				s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
					return []position{a, {x: a.x, y: a.y + 6, z: a.z}, {x: b.x, y: a.y + 6, z: a.z}, b}, nil
				}
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.PracticePulls != 0 {
				t.Fatal("arrived intent bypassed actual planner or bent ground route", mode, s.stateLocked(), f.packets)
			}
			if mode == "assets" || mode == "partial" {
				if s.state.Practicing || s.state.Moving || len(f.packets) != 0 {
					t.Fatal("missing complete route admitted movement", s.stateLocked(), f.packets)
				}
				return
			}
			if !s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 {
				t.Fatal("nearby bent corridor did not require actual route movement", s.stateLocked(), f.packets)
			}
			if mode == "elapsed" {
				advance(601 * time.Millisecond)
				s.Tick(context.Background())
				if s.state.Practicing || s.state.Moving || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.MSGMoveStop) != 1 {
					t.Fatal("tactical intent bypassed bounded elapsed-time movement", s.stateLocked(), f.packets)
				}
				return
			}
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			packet := f.packets[len(f.packets)-1]
			_, offset, err := readGUID(packet.Payload)
			if err != nil || packet.Opcode != opcode.MSGMoveHeartbeat {
				t.Fatal("route step did not write an actual heartbeat", packet, err)
			}
			x := math.Float32frombits(binary.LittleEndian.Uint32(packet.Payload[offset+10:]))
			y := math.Float32frombits(binary.LittleEndian.Uint32(packet.Payload[offset+14:]))
			if x != f.world.Self.X || math.Abs(float64(y-f.world.Self.Y)-1.4) > .002 || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("route step cut the verified corner or exceeded actual speed7", x, y, s.stateLocked())
			}
		})
	}
	t.Run("stop_callback_changes_corridor", func(t *testing.T) {
		s, f, advance := practiceApproachRig(t, 8)
		target := f.world.NearbyCreatures[0]
		target.X = f.world.Self.X + 20
		accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
			SessionId: s.sessionID, SelfGuid: s.selfGUID,
			Source: editPracticeApproachTuning(t, behaviors.StarterSpellSource, "caster_yards=25", "caster_yards=15")})
		if err != nil {
			t.Fatal(err)
		}
		s.Tick(context.Background())
		if !s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 || s.practiceRoute == nil {
			t.Fatal("corridor fixture never began its real verified route", s.stateLocked(), f.packets)
		}
		// Only the editable spatial assertion is adversarial. If native route
		// validation is missing, the real unchanged damage selector can still cast.
		source := "local policy=(function()\n" + behaviors.StarterSpellSource + "\nend)()\n" +
			`return {decide=function(input)
if input.selection=="practice_approach" then return {kind="arrived",standYards=25} end
return policy.decide(input)
end}`
		if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
			SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: accepted.Revision, Source: source}); err != nil {
			t.Fatal(err)
		}
		shifted := false
		f.onClock = func() {
			f.onClock = nil
			if !s.state.Moving {
				// stopLocked consumes the old moving flag immediately before this
				// actual STOP clock callback; its written route still belongs here.
				target.Y++
				shifted = true
			}
		}
		advance(200 * time.Millisecond)
		s.Tick(context.Background())
		if !shifted || countPackets(f, opcode.MSGMoveStop) != 1 || s.state.Moving || s.state.MotionUnconfirmed ||
			countPackets(f, opcode.CMSGCastSpell) != 0 || s.practiceRoute == nil || s.state.PracticePulls != 0 ||
			distance(s.pos, position{x: target.X, y: target.Y, z: target.Z}) >= 25 ||
			directRemainingRoute(s.pos, position{x: target.X, y: target.Y, z: target.Z}, s.practiceRoute, s.practiceRouteIndex) {
			t.Fatal("editable arrived assertion bypassed the changed final native corridor", shifted, s.stateLocked(), f.packets)
		}
	})
}

func TestPracticeApproachLuaCurrentPolicyRefusalNeverUsesNativeStandoffFallback(t *testing.T) {
	for name, source := range map[string]string{
		"old_handler": `return {decide=function() return {kind="cast",spellId=133} end}`,
		"error":       `return {decide=function() error("current approach unavailable") end}`,
		"wait":        `return {decide=function() return {kind="wait",standYards=0} end}`,
		"missing":     `return {decide=function() return {kind="arrived"} end}`,
		"extra":       `return {decide=function() return {kind="arrived",standYards=25,spellId=133} end}`,
		"text_range":  `return {decide=function() return {kind="arrived",standYards="25"} end}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, f, _ := practiceApproachRig(t, 8)
			if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
				SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: source}); err != nil {
				t.Fatal(err)
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 ||
				countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 ||
				s.state.PracticePulls != 0 || s.state.AssistAttempts != 0 {
				t.Fatal("invalid/current negative approach policy used hidden native defaults", s.stateLocked(), f.packets)
			}
		})
	}
	t.Run("wait_stops_actual_route", func(t *testing.T) {
		s, f, advance := practiceApproachRig(t, 8)
		f.world.NearbyCreatures[0].X = f.world.Self.X + 20
		accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
			SessionId: s.sessionID, SelfGuid: s.selfGUID,
			Source: editPracticeApproachTuning(t, behaviors.StarterSpellSource, "caster_yards=25", "caster_yards=15")})
		if err != nil {
			t.Fatal(err)
		}
		s.Tick(context.Background())
		if !s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 {
			t.Fatal("wait fixture never entered its actual route", s.stateLocked(), f.packets)
		}
		if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
			SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: accepted.Revision,
			Source: `return {decide=function() return {kind="wait",standYards=0} end}`}); err != nil {
			t.Fatal(err)
		}
		advance(200 * time.Millisecond)
		s.Tick(context.Background())
		if s.state.Moving || s.state.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStop) != 1 ||
			countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.PracticePulls != 0 {
			t.Fatal("current wait continued movement or admitted hostile fallback", s.stateLocked(), f.packets)
		}
	})
}

func TestPracticeApproachLuaCallbacksCannotExtendMovementAuthority(t *testing.T) {
	for _, stage := range []string{"planner", "policy", "world", "clock"} {
		for _, change := range []string{"quest", "body", "target", "lease", "context", "replacement"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				s, f, advance := practiceApproachRig(t, 5)
				f.world.NearbyCreatures[0].X = f.world.Self.X + 30
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				changed := false
				var replacementUntil time.Time
				mutate := func() {
					if changed {
						return
					}
					changed = true
					switch change {
					case "quest":
						f.world.SelfQuestLog[0].Complete = true
					case "body":
						s.sessionID = "replacement-Practice-session"
					case "target":
						f.world.NearbyCreatures[0].AttackVictimKnown = true
						f.world.NearbyCreatures[0].AttackVictimGuid = 3
					case "lease":
						advance(11 * time.Minute)
					case "context":
						cancel()
					case "replacement":
						// Tick owns the mutex. Inject a distinct native intent binding;
						// this does not claim public rearm while an action is pending.
						replacementUntil = coldridgeCasterReplaceBinding(s)
					}
				}
				policyRead := false
				runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
				s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
					output, err := runtime.EvaluateContext(ctx, input)
					if input["selection"] == "practice_approach" {
						policyRead = true
						if stage == "policy" {
							mutate()
						}
					}
					return true, runtime.Status().Revision, output, err
				}
				if stage == "planner" {
					s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
						mutate()
						return []position{a, b}, nil
					}
				} else if stage == "world" {
					s.session = &coldridgePullWorldSession{fakeSession: f, policyRead: &policyRead, after: mutate}
				} else if stage == "clock" {
					f.onClock = mutate
				}
				s.Tick(ctx)
				if !changed || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 ||
					countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.PracticePulls != 0 || s.state.AssistAttempts != 0 {
					t.Fatal("callback changed mission authority before a stale movement write", stage, change, changed, s.stateLocked(), f.packets)
				}
				if change == "replacement" && (!s.state.Practicing || s.practiceUntil != replacementUntil ||
					s.state.PracticePhase != "replacement_waiting_for_new_pull") {
					t.Fatal("older approach clobbered replacement intent binding", s.stateLocked())
				}
			})
		}
	}
}

type practiceApproachAmbiguousSession struct {
	*fakeSession
	opcode uint32
}

func (f *practiceApproachAmbiguousSession) SendPacket(ctx context.Context, request *modv1.SendPacketRequest, options ...grpc.CallOption) (*emptypb.Empty, error) {
	response, err := f.fakeSession.SendPacket(ctx, request, options...)
	if err == nil && request.Opcode == f.opcode {
		return nil, errors.New("actual movement write has an ambiguous transport outcome")
	}
	return response, err
}

func TestPracticeApproachLuaEditCannotReplayUnknownMovement(t *testing.T) {
	for _, movement := range []uint32{opcode.MSGMoveStartForward, opcode.MSGMoveStop} {
		t.Run(fmt.Sprint(movement), func(t *testing.T) {
			s, f, advance := practiceApproachRig(t, 8)
			f.world.NearbyCreatures[0].X = f.world.Self.X + 20
			source := editPracticeApproachTuning(t, behaviors.StarterSpellSource, "caster_yards=25", "caster_yards=15")
			accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
				SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: source})
			if err != nil {
				t.Fatal(err)
			}
			s.session = &practiceApproachAmbiguousSession{fakeSession: f, opcode: movement}
			s.Tick(context.Background())
			if movement == opcode.MSGMoveStop {
				if !s.state.Moving || s.state.MotionUnconfirmed {
					t.Fatal("STOP fixture never wrote the preceding actual START", s.stateLocked(), f.packets)
				}
				accepted, err = reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
					SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: accepted.Revision, Source: behaviors.StarterSpellSource})
				if err != nil {
					t.Fatal(err)
				}
				source = behaviors.StarterSpellSource
				advance(200 * time.Millisecond)
				s.Tick(context.Background())
			}
			if !s.state.MotionUnconfirmed || s.state.Moving || s.state.LastTransportFailure.GetOpcode() != movement ||
				!s.state.LastTransportFailure.GetOutcomeUnknown() || countPackets(f, movement) != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("actual ambiguous movement lost uncertainty or invented arrival/cast", s.stateLocked(), f.packets)
			}
			failure := proto.Clone(s.state.LastTransportFailure)
			if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
				SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: accepted.Revision, Source: source + "\n-- accepted after unknown movement"}); err != nil {
				t.Fatal(err)
			}
			before := len(f.packets)
			for range 3 {
				advance(200 * time.Millisecond)
				s.Tick(context.Background())
			}
			if len(f.packets) != before || !s.state.MotionUnconfirmed || !proto.Equal(failure, s.state.LastTransportFailure) ||
				s.state.AssistAttempts != 0 || s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 {
				t.Fatal("accepted source edit replayed uncertain movement or invented effects", s.stateLocked(), f.packets)
			}
			if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil {
				t.Fatal("public rearm bypassed unresolved physical movement")
			}
		})
	}
}
