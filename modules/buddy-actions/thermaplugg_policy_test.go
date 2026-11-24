package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func thermapluggTestOutput(kind string, f *thermapluggTestSession, pair int) map[string]any {
	out := map[string]any{"kind": kind, "faceGuid": "0", "buttonGuid": "0", "faceEntry": uint32(0), "buttonEntry": uint32(0), "memory": map[string]any{}}
	if pair >= 0 {
		p := thermapluggFaceButtons[pair]
		out["faceGuid"], out["buttonGuid"] = strconv.FormatUint(f.objects[p.face].Guid, 10), strconv.FormatUint(f.objects[p.button].Guid, 10)
		out["faceEntry"], out["buttonEntry"] = p.face, p.button
	}
	return out
}

func TestThermapluggSharedSelectorUsesActualDefaultFiveMillisecondRuntime(t *testing.T) {
	s, f, _ := thermapluggRig(t)
	// Override only this semantic fixture: actual production constructor,
	// source initialization and both pure reads retain the default watchdog.
	s.starterSpells.runtime = nil // Exercise the production lazy default constructor and source reload.
	f.objects[142211].State = 0
	if behavior.EvaluationLimit != 5*time.Millisecond {
		t.Fatal("production evaluation limit changed")
	}
	if !s.thermapluggButtonsTickLocked(context.Background(), f.world) || len(f.packets) != 1 || s.thermaplugg.attempt == nil ||
		!s.thermaplugg.attempt.written || s.starterSpells.lastError != "" {
		t.Fatal("actual shared selector failed its default evaluator contract", s.starterSpells.lastError, s.thermaplugg.attempt, f.packets)
	}
}

func TestThermapluggPolicySameBodyCASOwnsDutyAndPairChoice(t *testing.T) {
	s, f, _ := thermapluggRig(t)
	f.world.Self.ClassId = 5
	f.objects[142211].State = 0
	for _, entry := range []uint32{142210, 142215} {
		row := proto.Clone(f.objects[142214]).(*worldv1.GameObjectState)
		row.Entry, row.Guid, row.X = entry, uint64(0xf110)<<48|uint64(entry)<<24|2, 4
		f.objects[entry] = row
	}
	f.objects[142210].State = 0
	ctx := context.Background()
	if s.thermapluggButtonsTickLocked(ctx, f.world) || len(f.packets) != 0 || f.objectReads != 0 {
		t.Fatal("default Lua duty must refuse the other class without scanning", f.packets, f.objectReads)
	}
	sid, self := s.sessionID, s.selfGUID
	source := strings.Replace(behaviors.StarterSpellSource, "role_classes={[4]=true},prefer_nearest=true", "role_classes={[5]=true},prefer_nearest=false", 1)
	if source == behaviors.StarterSpellSource {
		t.Fatal("source tuning anchor missing")
	}
	accepted, err := reloadStarterSemanticBehavior(t, s, ctx, &actionsv1.BehaviorReloadRequest{SessionId: sid, SelfGuid: self, Source: source})
	if err != nil || accepted.Revision == "" {
		t.Fatal(accepted, err)
	}
	if !s.thermapluggButtonsTickLocked(ctx, f.world) || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGGameobjUse ||
		binary.LittleEndian.Uint64(f.packets[0].Payload) != f.objects[142215].Guid || s.sessionID != sid || s.selfGUID != self ||
		s.starterSpells.revision != accepted.Revision {
		t.Fatal("same-session Lua edit did not replace native class/nearest choice", s.starterSpells.lastError, s.stateLocked(), f.packets)
	}
}

func TestThermapluggUseAttemptReservedBeforeTransportAndNeverReplayedByPolicy(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(strconv.FormatBool(unknown), func(t *testing.T) {
			s, f, advance := thermapluggRig(t)
			f.objects[142211].State = 0
			f.failSend = unknown
			uses := 0
			f.onUse = func() {
				uses++
				a := s.thermaplugg.attempt
				if a == nil || !a.attempted || !a.pending || a.face != f.objects[142211].Guid || a.button != f.objects[142214].Guid || a.session != s.sessionID || a.self != s.selfGUID || a.visit != 1 {
					t.Fatal("use was not durably reserved before the transport callback", a)
				}
			}
			ctx := context.Background()
			if !s.thermapluggButtonsTickLocked(ctx, f.world) {
				t.Fatal("missing first use", s.starterSpells.lastError)
			}
			a := s.thermaplugg.attempt
			if len(f.packets) != 1 || a == nil || !a.pending || a.written == unknown || a.unknown != unknown {
				t.Fatal(a, f.packets)
			}
			// A valid but hostile/reset policy cannot replace physical custody.
			s.starterSpells.remote = func(_ context.Context, input map[string]any) (bool, string, map[string]any, error) {
				kind := "observe"
				if input["observationsKnown"] == true {
					kind = "choose"
				}
				return true, "edited-source", thermapluggTestOutput(kind, f, 0), nil
			}
			s.thermaplugg.memory = nil
			advance(time.Hour)
			for i := 0; i < 3; i++ {
				s.thermapluggButtonsTickLocked(ctx, f.world)
			}
			if uses != 1 || len(f.packets) != 1 || s.thermaplugg.attempt != a || !a.pending || a.unknown != unknown {
				t.Fatal("edit/timer replayed use", a, f.packets)
			}
			// Current-policy failure also retains native history without fallback.
			s.starterSpells.remote = func(context.Context, map[string]any) (bool, string, map[string]any, error) {
				return false, "new-broken-source", nil, errors.New("policy unavailable")
			}
			s.thermapluggButtonsTickLocked(ctx, f.world)
			if len(f.packets) != 1 || s.thermaplugg.attempt != a || !a.pending {
				t.Fatal("policy error lost use custody", a)
			}
			s.starterSpells.remote = nil
			f.failSend = false
			f.objects[142211].State = 1
			if !s.thermapluggButtonsTickLocked(ctx, f.world) || a.pending || a.unknown != unknown || a.written == unknown || len(f.packets) != 1 {
				t.Fatal("exact face closure must settle pending without rewriting delivery truth", a, f.packets, s.starterSpells.lastError)
			}
		})
	}
}

func TestThermapluggKnownZeroUseRetryIsLuaTimed(t *testing.T) {
	s, f, advance := thermapluggRig(t)
	f.objects[142211].State = 0
	f.useError = modcb.PacketNotDispatchedError("synthetic pre-transport refusal")
	uses := 0
	f.onUse = func() { uses++ }
	ctx := context.Background()
	s.thermapluggButtonsTickLocked(ctx, f.world)
	if uses != 1 || len(f.packets) != 0 || s.thermaplugg.attempt == nil || s.thermaplugg.attempt.pending || s.thermaplugg.feedback != "use_not_dispatched" {
		t.Fatal(uses, s.thermaplugg)
	}
	// Lua consumes feedback once, then its retry memory owns the interval.
	s.thermapluggButtonsTickLocked(ctx, f.world)
	if s.thermaplugg.feedback != "" {
		t.Fatal("feedback was not acknowledged by Lua clear")
	}
	f.useError = nil
	advance(999 * time.Millisecond)
	s.thermapluggButtonsTickLocked(ctx, f.world)
	if uses != 1 || len(f.packets) != 0 {
		t.Fatal("known-zero retry ignored Lua cooldown", uses, f.packets)
	}
	advance(time.Second)
	s.thermapluggButtonsTickLocked(ctx, f.world)
	if uses != 2 || len(f.packets) != 1 || !s.thermaplugg.attempt.written {
		t.Fatal("known zero failed to admit a later Lua choice", uses, f.packets, s.starterSpells.lastError)
	}
}

func TestThermapluggPolicyFinalCallbacksCannotWriteStaleBodyOrObjects(t *testing.T) {
	for _, seam := range []string{"policy session", "policy visit", "policy body", "policy controller", "policy pose", "object", "clock visit", "clock control"} {
		t.Run(seam, func(t *testing.T) {
			s, f, _ := thermapluggRig(t)
			f.objects[142211].State = 0
			if strings.HasPrefix(seam, "clock") {
				f.objects[142214].X = 9
			}
			runtime := s.starterSpells.runtime
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				out, err := runtime.EvaluateContext(ctx, input)
				if input["observationsKnown"] == true {
					switch seam {
					case "policy session":
						s.sessionID = "replacement"
					case "policy visit":
						s.encounterVisit.revision++
						f.world.VisitRevision++
					case "policy body":
						s.selfGUID = 8
					case "policy controller":
						s.state.InteractionOwner = "trade"
					case "policy pose":
						s.pos.x = 50
						s.posRevision++
					case "object":
						f.objects[142214].Flags = 2
					}
				}
				return true, runtime.Status().Revision, out, err
			}
			if strings.HasPrefix(seam, "clock") {
				f.onClock = func() {
					if seam == "clock visit" {
						s.encounterVisit.revision++
						f.world.VisitRevision++
					} else {
						s.clientControlLost = true
					}
				}
			}
			s.thermapluggButtonsTickLocked(context.Background(), f.world)
			if len(f.packets) != 0 || s.thermaplugg.attempt != nil {
				t.Fatal("late callback admitted a stale movement/use", seam, f.packets, s.thermaplugg.attempt)
			}
		})
	}
}

func TestThermapluggMalformedPolicyAndForeignVisitCannotConfirmAttempt(t *testing.T) {
	s, f, advance := thermapluggRig(t)
	f.objects[142211].State = 0
	s.thermapluggButtonsTickLocked(context.Background(), f.world)
	a := s.thermaplugg.attempt
	if a == nil || !a.pending {
		t.Fatal("first use missing", s.starterSpells.lastError)
	}
	advance(time.Second)
	s.starterSpells.remote = func(context.Context, map[string]any) (bool, string, map[string]any, error) {
		out := thermapluggTestOutput("choose", f, 0)
		out["extra"] = true
		return true, "malformed", out, nil
	}
	s.thermapluggButtonsTickLocked(context.Background(), f.world)
	if !a.pending || len(f.packets) != 1 {
		t.Fatal("malformed policy released pending", a)
	}
	s.starterSpells.remote = nil
	f.objects[142211].State = 1
	s.encounterVisit.revision, f.world.VisitRevision = 2, 2
	s.thermapluggButtonsTickLocked(context.Background(), f.world)
	if !a.pending || len(f.packets) != 1 {
		t.Fatal("foreign visit closed the old attempt", a, f.packets)
	}
}

func TestThermapluggStopCannotRefreshAwayChangedChoiceAuthority(t *testing.T) {
	for _, approach := range []bool{false, true} {
		for _, seam := range []string{"fresh raw", "fresh controller", "fresh source", "stop controller"} {
			t.Run(strconv.FormatBool(approach)+"/"+seam, func(t *testing.T) {
				s, f, _ := thermapluggRig(t)
				f.objects[142211].State = 0
				if approach {
					f.objects[142214].X = 9
				}
				runtime := s.starterSpells.runtime
				s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
					out, err := runtime.EvaluateContext(ctx, input)
					if input["observationsKnown"] == true {
						f.onSnapshot = func() {
							f.onSnapshot = nil
							switch seam {
							case "fresh raw":
								f.world.Self.X++
								f.world.Self.PositionRevision++
							case "fresh controller":
								s.pos.x++
								s.posRevision++
							case "fresh source":
								s.starterSpells.revision = "replacement"
							}
						}
						if seam == "stop controller" {
							s.state.Moving = true
							f.onClock = func() { s.posRevision++ }
						}
					}
					return true, runtime.Status().Revision, out, err
				}
				s.thermapluggButtonsTickLocked(context.Background(), f.world)
				for _, packet := range f.packets {
					if packet.Opcode != opcode.MSGMoveStop {
						t.Fatal("changed choice authority admitted approach/use", packet)
					}
				}
				if s.thermaplugg.attempt != nil {
					t.Fatal("changed choice authority reserved a use", s.thermaplugg.attempt)
				}
			})
		}
	}
}
