package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"strconv"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

func encounterTestRead(t *testing.T, s *Service, f *fakeSession) *actionsv1.EncounterObservationResponse {
	t.Helper()
	out, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.packets) != 0 || len(f.movementObservations) != 0 || f.clock != 0 {
		t.Fatal("encounter observation wrote or prepared gameplay packets", f.packets, f.movementObservations, f.clock)
	}
	return out
}

func encounterTestPacket(t *testing.T, s *Service, op uint32, payload []byte) {
	t.Helper()
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: payload}); err != nil {
		t.Fatal(err)
	}
}

func encounterTestCreature(guid uint64, mapID uint32) *worldv1.CreatureState {
	return &worldv1.CreatureState{Guid: guid, Entry: uint32(guid>>24) & 0xffffff, Present: true, MapKnown: true, MapId: mapID,
		HealthKnown: true, Health: 70, MaxHealth: 100, AliveKnown: true, Alive: true, UnitFlagsKnown: true, DynamicFlagsKnown: true,
		PositionKnown: true, PositionRevision: 9, X: 3}
}

func TestEncounterObservationWaitsForBothSameMapVisitWitnesses(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = f.party.SessionId
	const mapID = uint32(389)
	entry := make([]byte, 20)
	binary.LittleEndian.PutUint32(entry, mapID)
	f.world.Self.MapId, f.world.Owner.MapId = mapID, mapID
	f.world.NearbyCreatures = []*worldv1.CreatureState{encounterTestCreature(sheepTarget, mapID)}
	key := strconv.FormatUint(sheepTarget, 10)
	check := func(visit uint64, protected bool) {
		t.Helper()
		out := encounterTestRead(t, s, f)
		if out.GetVisitId() != visit {
			t.Fatalf("want visit %d, got %v", visit, out)
		}
		if visit == 0 {
			if out.GetReason() != "world_visit_unbound" || out.MapId != nil || len(out.Targets) != 0 || len(out.ProtectedAuras) != 0 {
				t.Fatalf("unbound visit exposed encounter facts: %v", out)
			}
			return
		}
		wantAuras := 0
		if protected {
			wantAuras = 1
		}
		if out.GetReason() != "" || out.GetMapId() != mapID || out.Targets[key] == nil || out.Targets[key].GetProtected() != protected || len(out.ProtectedAuras) != wantAuras {
			t.Fatalf("want visit %d and protection=%t, got %v", visit, protected, out)
		}
	}

	f.world.VisitRevision = 1
	check(0, false)
	encounterTestPacket(t, s, opcode.SMSGLoginVerifyWorld, entry)
	encounterTestPacket(t, s, opcode.SMSGAuraUpdate, sheepAura(sheepTarget, 1, 118, 20000))
	check(1, true)

	badEntry := append([]byte(nil), entry...)
	binary.LittleEndian.PutUint32(badEntry[4:], math.Float32bits(float32(math.NaN())))
	encounterTestPacket(t, s, opcode.SMSGNewWorld, badEntry)
	check(1, true)

	// The World queue completes first and already reports the same NPC GUID.
	f.world.VisitRevision = 2
	check(0, false)
	encounterTestPacket(t, s, opcode.SMSGNewWorld, entry)
	check(2, false)

	encounterTestPacket(t, s, opcode.SMSGAuraUpdate, sheepAura(sheepTarget, 1, 118, 20000))
	check(2, true)
	// The Actions queue completes first during a second same-map transfer.
	encounterTestPacket(t, s, opcode.SMSGTransferPending, entry[:4])
	check(0, false)
	encounterTestPacket(t, s, opcode.SMSGNewWorld, entry)
	check(0, false)
	f.world.VisitRevision = 3
	check(3, false)
}

func TestEncounterObservationUsesObservedLeaderExactKeysAndProtection(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = f.party.SessionId
	s.UseHostStandingHealing() // Standing-heal ownership cannot grant encounter ownership.
	const engagedGUID, ownerFocusGUID, foreignVictimGUID = sheepTarget, sheepTarget + 1, sheepTarget + 2
	f.world.VisitRevision, f.world.ObservedPackets = 1, 9007199254740993
	encounterTestPacket(t, s, opcode.SMSGLoginVerifyWorld, make([]byte, 20))
	f.world.Owner.TargetKnown, f.world.Owner.TargetGuid = true, ownerFocusGUID
	leader := triagePlayer(f, 4, 60)
	leader.TargetKnown, leader.TargetGuid = true, engagedGUID
	foreigner := triagePlayer(f, 8, 60)
	f.world.NearbyPlayers = []*worldv1.PlayerState{leader, foreigner}
	f.party.Revision = 12
	f.party.Group.GroupGuid, f.party.Group.LeaderGuid = 0x1234, leader.Guid
	f.party.Group.Members = append(f.party.Group.Members, &partyv1.Member{Guid: leader.Guid, Online: true})
	f.party.RaidTargets = &partyv1.RaidTargetState{GroupGuid: f.party.Group.GroupGuid, Revision: 11, ListRevision: 10,
		Complete: true, Marks: []*partyv1.RaidTarget{{Icon: 8, TargetGuid: engagedGUID}}}
	engaged := encounterTestCreature(engagedGUID, 0)
	engaged.AttackVictimKnown, engaged.AttackVictimGuid, engaged.AttackAgeMs = true, leader.Guid, 900
	selection := encounterTestCreature(ownerFocusGUID, 0)
	selection.TargetKnown, selection.TargetGuid = true, leader.Guid // Selection alone is not an attack.
	foreign := encounterTestCreature(foreignVictimGUID, 0)
	foreign.AttackVictimKnown, foreign.AttackVictimGuid = true, foreigner.Guid
	f.world.NearbyCreatures = []*worldv1.CreatureState{engaged, selection, foreign}
	f.world.OwnerTarget = selection // The duplicate source must not duplicate the row.

	encounterTestPacket(t, s, opcode.SMSGAuraUpdate, sheepAura(engagedGUID, 1, 118, 20000))
	second := sheepAura(engagedGUID, leader.Guid, 118, 18000)
	second[len(packedGUID(engagedGUID))] = 7
	encounterTestPacket(t, s, opcode.SMSGAuraUpdate, second)
	encounterTestPacket(t, s, opcode.SMSGAuraUpdate, sheepAura(sheepTarget+3, 1, 118, 20000))

	out := encounterTestRead(t, s, f)
	key, ownerKey, foreignKey := strconv.FormatUint(engagedGUID, 10), strconv.FormatUint(ownerFocusGUID, 10), strconv.FormatUint(foreignVictimGUID, 10)
	marks := out.GetParty().GetRaidTargets().GetMarks()
	if out.GetSessionId() != s.sessionID || out.GetSelfGuid() != 1 || out.HostOwned == nil || out.GetHostOwned() || !out.GetHealing().GetHostOwned() ||
		out.GetParty().GetGroup().GetLeaderGuid() != leader.Guid || out.GetObservedOwnerFocus() != engagedGUID || len(marks) != 1 || marks[0].GetTargetGuid() != engagedGUID ||
		out.Players["4"].GetGuid() != leader.Guid || out.Players["8"] != nil || len(out.Targets) != 3 || !out.Targets[key].GetAttackable() ||
		out.Targets[key].GetEngaged() || len(out.Engagement) != 0 ||
		!out.Targets[key].GetAttackObservation().GetVictimKnown() || out.Targets[key].GetAttackObservation().GetVictimGuid() != leader.Guid || out.Targets[key].GetAttackObservation().GetAgeMs() != 900 ||
		out.Targets[foreignKey].GetAttackObservation().GetVictimGuid() != foreigner.Guid || out.Targets[ownerKey].GetAttackObservation().GetVictimKnown() || !out.Targets[ownerKey].GetAttackObservation().GetTargetKnown() {
		t.Fatalf("incorrect encounter identity, leader, marks, or attack evidence: %v", out)
	}
	if !out.Targets[key].GetProtected() || len(out.ProtectedAuras) != 2 || out.ProtectedAuras[0].GetSlot() != 2 || out.ProtectedAuras[0].GetCasterGuid() != 1 || out.ProtectedAuras[1].GetSlot() != 7 || out.ProtectedAuras[1].GetCasterGuid() != leader.Guid {
		t.Fatalf("protection lost slot/caster identity or leaked unrelated NPC aura: %v", out.GetProtectedAuras())
	}
	wire, err := protojson.Marshal(out)
	var jsonView struct {
		MapID                    *uint32
		HostOwned                *bool
		WorldObservationRevision string
		Targets                  map[string]struct{ Guid string }
	}
	if err != nil || json.Unmarshal(wire, &jsonView) != nil || jsonView.MapID == nil || *jsonView.MapID != 0 ||
		jsonView.HostOwned == nil || *jsonView.HostOwned || jsonView.Targets[key].Guid != key || jsonView.Targets[ownerKey].Guid != ownerKey ||
		jsonView.WorldObservationRevision != "9007199254740993" {
		t.Fatalf("wire lost map zero, explicit ownership, exact GUIDs, or revision: %s (%v)", wire, err)
	}

	encounterTestPacket(t, s, opcode.SMSGAuraUpdate, buffAura(engagedGUID, 1, 2, 0, 0))
	engaged.TargetKnown, engaged.TargetGuid = true, 1 // Contradicts the older victim observation.
	out = encounterTestRead(t, s, f)
	if out.GetObservedOwnerFocus() != engagedGUID || len(out.Engagement) != 0 ||
		out.Targets[key].GetAttackObservation().GetVictimGuid() != leader.Guid || out.Targets[key].GetAttackObservation().GetTargetGuid() != 1 ||
		!out.Targets[key].GetProtected() || len(out.ProtectedAuras) != 1 || out.ProtectedAuras[0].GetSlot() != 7 {
		t.Fatalf("raw contradiction or removed protection slot was lost: %v", out)
	}
	engaged.TargetGuid = leader.Guid
	leader.Present = false
	out = encounterTestRead(t, s, f)
	if out.GetObservedOwnerFocus() != 0 || len(out.Engagement) != 0 {
		t.Fatalf("missing leader fell back to fixed-owner focus or attack authority: %v", out)
	}
	leader.Present, leader.Alive, leader.Health = true, false, 0
	engaged.AttackAgeMs = 9007199254740993
	out = encounterTestRead(t, s, f)
	if raw := out.Targets[key].GetAttackObservation(); raw.GetVictimGuid() != leader.Guid || raw.GetAgeMs() != engaged.AttackAgeMs {
		t.Fatal("Go filtered attack facts by dead victim or freshness", out)
	}
	selection.AttackVictimKnown, selection.TargetKnown = false, false
	selection.AttackVictimGuid, selection.TargetGuid = 0, 0
	wire, err = protojson.Marshal(encounterTestRead(t, s, f))
	var sparse map[string]any
	if err != nil || json.Unmarshal(wire, &sparse) != nil {
		t.Fatal(err)
	}
	if _, ok := sparse["targets"].(map[string]any)[ownerKey].(map[string]any)["attackObservation"].(map[string]any); !ok {
		t.Fatal("new backend's empty raw observation was indistinguishable from legacy feed", string(wire))
	}
}

func TestEncounterObservationReflectsAsRead(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	const name = "buddy-actions.encounter_observation"
	catalog, err := registry.Catalog().ForMethod(name)
	if err != nil || len(catalog.Methods) != 1 || catalog.Methods[0].Effect != moddisc.EffectRead {
		t.Fatalf("missing reflected encounter read: %v, %v", catalog, err)
	}
	module, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("missing Actions descriptor")
	}
	method, ok := module.RPC("encounter_observation")
	if !ok || method.Descriptor().Input().FullName() != "google.protobuf.Empty" || method.Descriptor().Output().FullName() != "warcraftbuddy.actions.v1.EncounterObservationResponse" {
		t.Fatal("incompatible encounter observation RPC descriptor")
	}
}
