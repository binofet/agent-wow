package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func TestExpiredLearnedPriorKeepsComposedFriendlyActions(t *testing.T) {
	for _, action := range []string{"heal", "buff"} {
		for _, guard := range []string{"expired", "active", "unexpired", "unknown", "operation", "ground", "awaiting", "visit", "moving", "motion", "explicit"} {
			t.Run(action+"/"+guard, func(t *testing.T) {
				setup := `local s=encounter_commands.export_state().data
 s.rangedHold={owner="ranged:maraudon_sludge_backline",sessionId=frame.session_id,selfGuid="4",mapId=349,visitId="1",token="original-token",untilUnixMs=1}
 `
				switch guard {
				case "unexpired":
					setup += `s.rangedHold.untilUnixMs=9999999999999`
				case "unknown":
					setup += `s.rangedHold.uncertain=true`
				case "operation":
					setup += `s.rangedHold.operation={mode="release"}`
				case "ground":
					setup += `s.rangedGround={uncertain=true}`
				case "awaiting":
					setup += `s.rangedGround={awaiting=true}`
				case "visit":
					setup += `s.rangedHold.visitId="2"`
				case "explicit":
					setup += `s.rangedOnly={id="explicit",sessionId=frame.session_id,selfGuid="4",mapId=349,visitId="1",creatureEntry=12222,minRangeYards=14,maxRangeYards=36,clearanceYards=2,durationMs=600000,expiresAt=9999999999999,expiresUnixMs=9999999999999}`
				}
				source := strings.Replace(readEncounterBundle(t), "local function decide(frame)", "local function decide(frame)\n"+setup+"\n", 1)
				source = strings.Replace(source, "commands.ranged_filter(current,frame,state.sequence,out,ranged_config,ranged_status,out.rangedOffenseEligible and damage_spell or nil,task_view)", "commands.ranged_filter(current,frame,state.sequence,out,ranged_config,ranged_status,out.rangedOffenseEligible and damage_spell or nil,task_view)\nout.fixtureHoldRetained=commands.export_state().data.rangedHold~=nil", 1)
				h, e := behavior.NewSession(context.Background(), fallbackSession, source)
				if e != nil {
					t.Fatal(e)
				}
				defer h.Close()
				p := &fallbackPolicyTest{t: t, host: h}
				o := positionedHealObservation()
				o.MapId = proto.Uint32(349)
				for _, v := range o.Players {
					v.MapId = proto.Uint32(349)
					v.CombatKnown = true
					v.InCombat = false
				}
				self := o.Players["4"]
				self.PositionEstimated = true
				self.MovementKnown = true
				self.Moving = false
				o.Navigation = &actionsv1.NavigationObservation{MovementOwner: &actionsv1.MovementOwnerState{}}
				n := proto.Clone(sludgePriorObservation().Targets[fallbackGUID(fallbackNPC)]).(*actionsv1.EncounterCreature)
				n.X = self.X + 170
				n.Y = self.Y
				n.Z = self.Z
				n.Engaged = false
				n.AttackObservation = &actionsv1.EncounterAttackObservation{}
				o.Targets[fallbackGUID(fallbackNPC)] = n
				if action == "buff" {
					o.Healing.Patients = nil
					o.Healing.Spells = nil
					b := &actionsv1.EncounterSpell{Known: true, Ready: true, Available: true, MetadataKnown: true, Capability: "buff", TargetRule: "ally", FamilyId: 5697, Rank: 1, Cost: 10, MaxRange: 30, AuraEffects: []*actionsv1.EncounterAuraEffect{{Kind: 6, Aura: 82}}}
					o.Spells = map[string]*actionsv1.EncounterSpell{"5697": b}
					o.Support = &actionsv1.EncounterSupport{AuraSpells: map[uint32]*actionsv1.EncounterSpell{5697: b}}
					for _, v := range o.Players {
						v.HealthKnown = true
						v.Health = 100
						v.MaxHealth = 100
						v.PowerKnown = true
						v.PowerType = 0
						v.Power = 100
						v.MaxPower = 100
					}
				}
				switch guard {
				case "active":
					o.Navigation.MovementOwner.Active = true
					o.Navigation.MovementOwner.Owner = "foreign"
				case "moving":
					self.Moving = true
				case "motion":
					o.Navigation.MotionUnconfirmed = true
				}
				out := p.decide(observeRanged(t, p, o, false)).Output
				a, _ := out["action"].(map[string]any)
				if guard == "expired" {
					if a["capability"] != action {
						t.Fatal("expired bookkeeping suppressed current selector", out)
					}
				} else if guard == "explicit" {
					status, _ := out["rangedStance"].(map[string]any)
					if status["active"] != true || status["origin"] == "learned_prior" {
						t.Fatal("explicit directive changed", out)
					}
				} else if a["capability"] == action {
					t.Fatal("protected custody unexpectedly retired", out)
				}
			})
		}
	}
}
