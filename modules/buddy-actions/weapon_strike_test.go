package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

func instantStrikeFixture(t *testing.T, id uint32) (*Service, *fakeSession, *actionsv1.CastOffenseRequest) {
	t.Helper()
	s, f, r, _ := exactOffenseFixture(t)
	learnSpells(s, 53, 1760, 6762)
	r.SpellId = id
	self := f.world.Self
	self.ClassId, self.PowerType, self.Power, self.MaxPower = 4, 3, 100, 100
	self.Mainhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: 11, MetadataKnown: true, Entry: 2092, DurabilityKnown: true, Durability: 10, MaxDurability: 10}
	s.weapons[2092] = weaponProof{entry: 2092, class: 2, subclass: 15, inventory: 13, valid: true, damageKnown: true, damageMin: 20, damageMax: 30}
	f.world.RequestedCreature.X = 3
	s.state.AutoAttacking, s.state.AutoAttackTargetGuid, s.autoTarget = true, enemyGUID, enemyGUID
	s.state.Moving, self.MovementFlags = true, 1
	return s, f, r
}

func comboPacket(target uint64, points byte) []byte { return append(packedGUID(target), points) }

func TestExactWeaponStrikesCoexistWithMovingAutoswingAndUseCatalogGCD(t *testing.T) {
	for _, id := range []uint32{1760, 53, 6762} {
		t.Run(encounterGUID(uint64(id)), func(t *testing.T) {
			s, f, r := instantStrikeFixture(t, id)
			if id == 6762 {
				emitPolymorph(s, opcode.SMSGUpdateComboPoints, comboPacket(enemyGUID, 5))
			}
			out, err := s.CastOffense(context.Background(), r)
			if err != nil || !out.GetLastSpellOperation().GetTransportWritten() || !out.GetCastPending() ||
				!out.GetAutoAttacking() || !out.GetMoving() || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCastSpell ||
				binary.LittleEndian.Uint32(f.packets[0].Payload[1:]) != id || s.nextAssist.Sub(s.now()) != time.Second {
				t.Fatal("exact instant strike interrupted movement/swing or used a mage GCD", out, err, f.packets)
			}
			if id == 6762 && (!s.weaponCombo.known || s.weaponCombo.points != 5) {
				t.Fatal("cast inferred combo consumption")
			}
			if countPackets(f, opcode.CMSGAttackstop) != 0 || countPackets(f, opcode.MSGMoveStop) != 0 {
				t.Fatal("strike stopped combat", f.packets)
			}
		})
	}
}

func TestWeaponStrikeAfterRealAttackStartAcknowledgement(t *testing.T) {
	for _, mode := range []string{"acknowledged", "pending", "unconfirmed", "stale visit", "foreign swing"} {
		t.Run(mode, func(t *testing.T) {
			s, f, start, _ := exactMeleeFixture(t)
			learnSpells(s, autoAttack, 1760)
			self := f.world.Self
			self.ClassId, self.PowerType, self.Power, self.MaxPower = 4, 3, 100, 100
			s.weapons[2092] = weaponProof{entry: 2092, class: 2, subclass: 15, inventory: 13, valid: true}
			f.world.RequestedCreature.X = 3
			s.state.Moving, self.MovementFlags = true, 1
			if _, err := s.Melee(exactMeleeContext(), start); err != nil {
				t.Fatal(err)
			}
			if mode != "pending" {
				acknowledgeExactMeleeStart(t, s, enemyGUID)
				if !s.state.AutoAttacking || !s.state.AutoAttackRequested {
					t.Fatal("fixture did not retain the actual acknowledged requested state", s.stateLocked())
				}
			}
			switch mode {
			case "unconfirmed":
				s.state.AutoAttackUnconfirmed = true
			case "stale visit":
				s.meleeAcknowledgedVisit++
			case "foreign swing":
				s.state.AutoAttackTargetGuid++
			}
			obs := &actionsv1.EncounterObservationResponse{Spells: map[string]*actionsv1.EncounterSpell{}}
			s.encounterSpells(f.world, nil, obs)
			row := obs.Spells["1760"]
			wantReady := mode == "acknowledged"
			if row == nil || row.Ready != wantReady {
				t.Fatal("reflection disagrees with actual start receipt", mode, row)
			}
			out, err := s.CastOffense(context.Background(), &actionsv1.CastOffenseRequest{SessionId: start.SessionId, SelfGuid: start.SelfGuid, TargetGuid: start.TargetGuid, SpellId: 1760})
			if !wantReady {
				if status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
					t.Fatal("pending or unconfirmed start allowed a strike", mode, out, err, f.packets)
				}
				return
			}
			if err != nil || len(f.packets) != 2 || f.packets[0].Opcode != opcode.CMSGAttackswing || f.packets[1].Opcode != opcode.CMSGCastSpell ||
				!out.GetAutoAttacking() || !out.GetAutoAttackRequested() || !out.GetMoving() || !out.GetLastSpellOperation().GetTransportWritten() {
				t.Fatal("acknowledged moving autoswing blocked strike or was interrupted", out, err, f.packets)
			}
		})
	}
}

func TestExactWeaponStrikeRequiresMechanicalWeaponPositionAndComboProof(t *testing.T) {
	for _, mode := range []string{"front_backstab", "wrong_weapon", "broken_weapon", "range", "energy", "combo_unknown", "combo_foreign", "combo_zero", "pacified", "disarmed"} {
		t.Run(mode, func(t *testing.T) {
			id := uint32(53)
			if mode == "combo_unknown" || mode == "combo_foreign" || mode == "combo_zero" {
				id = 6762
			}
			s, f, r := instantStrikeFixture(t, id)
			switch mode {
			case "front_backstab":
				f.world.RequestedCreature.Orientation = float32(math.Pi)
			case "wrong_weapon":
				proof := s.weapons[2092]
				proof.subclass = 7
				s.weapons[2092] = proof
			case "broken_weapon":
				f.world.Self.Mainhand.Durability = 0
			case "range":
				f.world.RequestedCreature.X = 5.01
			case "energy":
				f.world.Self.Power = 59
			case "combo_foreign":
				emitPolymorph(s, opcode.SMSGUpdateComboPoints, comboPacket(enemyGUID+1, 5))
			case "combo_zero":
				emitPolymorph(s, opcode.SMSGUpdateComboPoints, comboPacket(enemyGUID, 0))
			case "pacified":
				f.world.Self.UnitFlags |= 0x20000
			case "disarmed":
				f.world.Self.UnitFlags |= 0x200000
			}
			if _, err := s.CastOffense(context.Background(), r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
				t.Fatal("unqualified strike was written", mode, err, f.packets)
			}
		})
	}
	t.Run("physical strike is not silenced", func(t *testing.T) {
		s, f, r := instantStrikeFixture(t, 1760)
		f.world.Self.UnitFlags |= 0x2000
		if _, err := s.CastOffense(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOwnComboObservationRejectsMalformedPacketsAndDoesNotInferGains(t *testing.T) {
	s, _, _ := instantStrikeFixture(t, 1760)
	emitPolymorph(s, opcode.SMSGUpdateComboPoints, comboPacket(enemyGUID, 4))
	before := s.weaponCombo
	for _, payload := range [][]byte{nil, {0}, {0, 1}, comboPacket(enemyGUID, 6), append(comboPacket(enemyGUID, 5), 0)} {
		emitPolymorph(s, opcode.SMSGUpdateComboPoints, payload)
		if s.weaponCombo != before {
			t.Fatal("malformed packet changed actual combo state", payload, s.weaponCombo)
		}
	}
	emitPolymorph(s, opcode.SMSGUpdateComboPoints, comboPacket(enemyGUID, 0))
	if !s.weaponCombo.known || s.weaponCombo.points != 0 {
		t.Fatal("actual zero-point observation missing")
	}
	emitPolymorph(s, opcode.SMSGNewWorld, make([]byte, 20))
	if s.weaponCombo.known {
		t.Fatal("new visit retained stale combo proof")
	}
}

func TestWeaponStrikeReflectionAndLuaEnergyComboPositionChoices(t *testing.T) {
	s, f, _ := instantStrikeFixture(t, 1760)
	obs := &actionsv1.EncounterObservationResponse{SelfGuid: s.selfGUID, Players: map[string]*actionsv1.EncounterPlayer{}, Spells: map[string]*actionsv1.EncounterSpell{}}
	refresh := func() { s.encounterPlayers(f.world, nil, obs); s.encounterSpells(f.world, nil, obs) }
	refresh()
	for _, id := range []string{"53", "1760", "6762"} {
		if row := obs.Spells[id]; row == nil || row.Capability != "offense" || !row.Ready || !row.WeaponStrike || row.GlobalCooldownMs != 1000 {
			t.Fatal("learned weapon strike was dropped or stationary-only", id, row)
		}
	}
	if row := obs.Spells["53"]; !row.RequiresBehind || row.DirectDamage != 52.5 || row.WeaponDamageMultiplier != 1.5 || !row.DamageModelKnown {
		t.Fatal("Backstab base weapon effect order missing", row)
	}
	if row := obs.Spells["6762"]; !row.RequiresComboPoints || row.DamageModelKnown {
		t.Fatal("finisher advertised invented combo damage", row)
	}
	host, err := behavior.NewSession(context.Background(), "weapon-policy", spellSelectorSource(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	target := map[string]any{"guid": encounterGUID(enemyGUID), "healthKnown": true, "health": 1000, "maxHealth": 1000, "positionKnown": true, "x": 3, "y": 0, "z": 0, "orientation": 0}
	choose := func(want float64) {
		t.Helper()
		refresh()
		// The live protobuf JSON omits scalar zeros, including cast time and
		// the caster's facing angle. EmitUnpopulated would mask this regression.
		raw, err := protojson.Marshal(obs)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		row := value["spells"].(map[string]any)["53"].(map[string]any)
		if _, present := row["castTimeMs"]; present {
			t.Fatal("fixture did not exercise an omitted instant cast time", row)
		}
		if _, err := host.Enqueue(behavior.Event{Kind: "spell_observation", Value: value}); err != nil {
			t.Fatal(err)
		}
		out, err := host.Decide(context.Background(), map[string]any{"target": target, "options": map[string]any{"instant_only": true}})
		if err != nil || out.Output["spellId"] != want {
			t.Fatal("wrong Lua weapon rotation", want, out.Output, err)
		}
	}
	choose(53)
	target["orientation"] = math.Pi
	choose(1760)
	emitPolymorph(s, opcode.SMSGUpdateComboPoints, comboPacket(enemyGUID, 5))
	choose(6762)
	emitPolymorph(s, opcode.SMSGUpdateComboPoints, comboPacket(enemyGUID+1, 5))
	choose(1760)
	f.world.Self.Power = 40
	s.improvedSinister2 = true
	choose(1760)
	target["positionKnown"] = false
	refresh()
	enqueueSpellObservation(t, host, obs)
	out, err := host.Decide(context.Background(), map[string]any{"target": target, "options": map[string]any{"instant_only": true}})
	if err != nil || out.Output["spellId"] != nil {
		t.Fatal("unknown position did not fall back to positioning", out.Output, err)
	}
}

func TestLuaInstantFilterRequiresKnownMetadataAndRejectsCastTime(t *testing.T) {
	for _, mode := range []string{"omitted_zero", "explicit_zero", "cast_time", "unknown_metadata", "invalid_cast_time"} {
		t.Run(mode, func(t *testing.T) {
			host, err := behavior.NewSession(context.Background(), "instant-filter", spellSelectorSource(t, ""))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(host.Close)
			row := map[string]any{"known": true, "ready": true, "capability": "offense", "metadataKnown": true,
				"familyId": float64(1752), "cost": float64(45), "directDamage": float64(25)}
			switch mode {
			case "explicit_zero":
				row["castTimeMs"] = float64(0)
			case "cast_time":
				row["castTimeMs"] = float64(1500)
			case "unknown_metadata":
				delete(row, "metadataKnown")
			case "invalid_cast_time":
				row["castTimeMs"] = "invalid"
			}
			if _, err := host.Enqueue(behavior.Event{Kind: "spell_observation", Value: map[string]any{"spells": map[string]any{"1752": row}}}); err != nil {
				t.Fatal(err)
			}
			out, err := host.Decide(context.Background(), map[string]any{"options": map[string]any{"instant_only": true}})
			if err != nil {
				t.Fatal(err)
			}
			want := any(nil)
			if mode == "omitted_zero" || mode == "explicit_zero" {
				want = float64(1752)
			}
			if out.Output["spellId"] != want {
				t.Fatal("wrong instant eligibility", mode, out.Output)
			}
		})
	}
}

func TestWeaponQueryReflectsActualBaseDamage(t *testing.T) {
	payload := weaponQueryFixture(2092, 15, 13)
	// Header/names24, fixed fields88, scaling8 precede two damage records.
	start := 16 + len("Dagger\x00\x00\x00\x00") + 88 + 8
	binary.LittleEndian.PutUint32(payload[start:], math.Float32bits(20))
	binary.LittleEndian.PutUint32(payload[start+4:], math.Float32bits(30))
	proof, err := decodeWeaponQuery(payload)
	if err != nil || !proof.damageKnown || proof.damageMin != 20 || proof.damageMax != 30 {
		t.Fatal(proof, err)
	}
}

func TestComboFinisherCatalogClassificationDoesNotAdmitOtherDummySpells(t *testing.T) {
	for _, id := range []uint32{2098, 6760, 6761, 6762} {
		spell, _ := spellcatalog.Default().Lookup(id)
		if !spell.DirectComboFinisher() || spell.Capability().Kind != spellcatalog.WeaponStrike {
			t.Fatal(id, spell.Capability())
		}
		spell.Effects[1].A = 21
		if spell.Capability().Kind == spellcatalog.WeaponStrike {
			t.Fatal("foreign dummy target admitted", id)
		}
	}
}

func TestFreshHostAcquiresOwnWeaponProofWithoutNativeCombatOrReconnect(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = f.party.SessionId
	self := f.world.Self
	self.ClassId, self.PowerType, self.Power, self.MaxPower = 4, 3, 100, 100
	self.ChannelSpellKnown, self.ChannelObjectKnown = true, true
	self.Mainhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: 11, MetadataKnown: true, Entry: 2092, DurabilityKnown: true, Durability: 10, MaxDurability: 10}
	learnSpells(s, autoAttack, 1760, 6770, 1784)
	activateEncounterWriter(t, s, f)
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGItemQuerySingle) != 1 || countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("host startup did not acquire only actual own weapon metadata", f.packets)
	}
	emitPolymorph(s, opcode.SMSGItemQuerySingleResponse, weaponQueryFixture(2092, 15, 13))
	out, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if err != nil || !out.GetHostOwned() || !out.GetMeleeKnown() || !out.Spells["1760"].GetReady() || !s.weaponReady(self) || out.GetSessionId() != f.party.SessionId {
		t.Fatal("ordinary item reply did not enable the same running host", out, err)
	}
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGItemQuerySingle) != 1 {
		t.Fatal("known weapon metadata was queried again", f.packets)
	}
}
