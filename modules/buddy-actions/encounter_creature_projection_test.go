package buddyactions

import (
	"strings"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Check every CreatureState descriptor against the real encounter projection.
// A new field must either survive under its own name or gain an explicit mapping.
func TestEncounterCreatureProjectsEveryWorldFact(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = f.party.SessionId
	f.world.VisitRevision = 1
	encounterTestPacket(t, s, opcode.SMSGLoginVerifyWorld, make([]byte, 20))
	c := &worldv1.CreatureState{Guid: sheepTarget, Entry: uint32((sheepTarget >> 24) & 0xffffff), NpcFlags: 0x2002,
		Present: true, MapKnown: true, MapId: 0, PositionKnown: true, X: 5, Y: 2, Z: 4, Orientation: .5,
		PositionAgeMs: 73, PositionRevision: 93, ObservationAgeMs: 31, HealthKnown: true, Health: 992, MaxHealth: 1234,
		AliveKnown: true, Alive: true, UnitFlags: 0x80000, DynamicFlags: 1,
		DistanceToSelfObservedKnown: true, DistanceToSelfObserved: 15, DistanceToOwnerObservedKnown: true, DistanceToOwnerObserved: 17,
		MovementFlags: 1, FactionTemplate: 14, TargetKnown: true, TargetGuid: 93, UnitFlags_2: 2,
		AttackVictimKnown: true, AttackVictimGuid: 74, AttackAgeMs: 32,
		Trajectory:       &worldv1.CreatureTrajectory{Known: true, DurationMs: 490, Cyclic: true},
		CombatReachKnown: true, CombatReach: 2.75, LevelKnown: true, Level: 48, UnitFlagsKnown: true, DynamicFlagsKnown: true,
		ActiveCast:              &worldv1.CreatureCast{SpellId: 2054, CastCount: 2, RemainingMs: 717, TargetGuid: 83, Revision: 17},
		StationaryPositionKnown: true, NpcFlagsKnown: true, FactionTemplateKnown: true, UnitFlags_2Known: true, MovementFlagsKnown: true}
	f.world.NearbyCreatures = []*worldv1.CreatureState{c}
	projected := encounterTestRead(t, s, f).Targets[encounterGUID(c.Guid)]
	if projected == nil {
		t.Fatal("current creature missing")
	}
	paths := map[string]string{
		"target_known": "attack_observation.target_known", "target_guid": "attack_observation.target_guid",
		"attack_victim_known": "attack_observation.victim_known", "attack_victim_guid": "attack_observation.victim_guid", "attack_age_ms": "attack_observation.age_ms",
		"stationary_position_known": "flightmaster.stationary_position_known", "npc_flags_known": "?npc_flags", "dynamic_flags_known": "?dynamic_flags",
		"unit_flags_2_known": "?unit_flags_2", "movement_flags_known": "?movement_flags", "combat_reach_known": "?combat_reach",
		"distance_to_self_observed_known": "?distance_to_self_observed", "distance_to_owner_observed_known": "?distance_to_owner_observed",
	}
	source, target := c.ProtoReflect(), projected.ProtoReflect()
	fields := source.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		from := fields.Get(i)
		name := string(from.Name())
		path, ok := paths[name]
		if !ok {
			path = name
		}
		t.Run(name, func(t *testing.T) {
			m, dest := target, path
			if parent, leaf, ok := strings.Cut(path, "."); ok {
				m = m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(parent))).Message()
				dest = leaf
			}
			presence := strings.HasPrefix(dest, "?")
			fd := m.Descriptor().Fields().ByName(protoreflect.Name(strings.TrimPrefix(dest, "?")))
			if fd == nil {
				t.Fatalf("World field %s has no encounter representation", name)
			}
			want, got := source.Get(from), m.Get(fd)
			if presence {
				if !fd.HasPresence() || m.Has(fd) != want.Bool() {
					t.Fatal("missing qualified value", path)
				}
			} else if from.Kind() == protoreflect.MessageKind {
				if !proto.Equal(want.Message().Interface(), got.Message().Interface()) {
					t.Fatal("lost nested data", path)
				}
			} else if want.Interface() != got.Interface() {
				t.Fatalf("%s = %v, observed %v", path, got, want)
			}
		})
	}
	c.LevelKnown = false
	c.Level = 0xffffffff
	c.ActiveCast = nil
	c.PositionKnown = false
	c.DistanceToSelfObservedKnown = false
	c.DistanceToOwnerObservedKnown = false
	c.DynamicFlagsKnown = false
	c.NpcFlags, c.NpcFlagsKnown = 0x2, false
	c.UnitFlags_2, c.UnitFlags_2Known = 0x8, false
	c.MovementFlagsKnown = false
	row := encounterTestRead(t, s, f).Targets[encounterGUID(c.Guid)]
	if row.Level != nil || row.LevelKnown || row.ActiveCast != nil || row.PositionAgeMs != nil || row.DistanceToSelfObserved != nil ||
		row.DistanceToOwnerObserved != nil || row.DynamicFlags != nil || row.NpcFlags != nil || row.UnitFlags_2 != nil || row.MovementFlags != nil {
		t.Fatal("unknown observations were emitted as exact", row)
	}
	c.LevelKnown = true
	c.Level = 45
	c.NpcFlagsKnown, c.UnitFlags_2Known, c.MovementFlagsKnown, c.DynamicFlagsKnown = true, true, true, true
	c.MovementFlags, c.DynamicFlags, c.NpcFlags, c.UnitFlags_2 = 0, 0, 0, 0
	c.DistanceToSelfObservedKnown, c.DistanceToSelfObserved = true, 0
	row = encounterTestRead(t, s, f).Targets[encounterGUID(c.Guid)]
	if row.NpcFlags == nil || row.GetNpcFlags() != 0 || row.UnitFlags_2 == nil || row.GetUnitFlags_2() != 0 || row.MovementFlags == nil || row.GetMovementFlags() != 0 ||
		row.DynamicFlags == nil || row.GetDynamicFlags() != 0 || row.DistanceToSelfObserved == nil || row.GetDistanceToSelfObserved() != 0 || row.GetLevel() != 45 {
		t.Fatal("observed zero values lost", row)
	}
}

func TestEncounterCreatureTooltipCacheIsBoundedAndSessionLocal(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = f.party.SessionId
	f.world.VisitRevision = 1
	encounterTestPacket(t, s, opcode.SMSGLoginVerifyWorld, make([]byte, 20))
	row := encounterTestCreature(sheepTarget, 0)
	inconsistent := encounterTestCreature(sheepTarget+(1<<24), 0)
	inconsistent.Entry = row.Entry
	f.world.NearbyCreatures = []*worldv1.CreatureState{row, encounterTestCreature(sheepTarget+1, 0), inconsistent}
	packet := sapTemplateReply(row.Entry, 7)
	encounterTestPacket(t, s, opcode.SMSGCreatureQueryResponse, packet)
	view := encounterTestRead(t, s, f)
	if len(view.Targets) != 3 || len(view.CreatureTemplates) != 1 || view.CreatureTemplates["555"].GetName() == "" ||
		view.Targets[encounterGUID(row.Guid)].GetTemplateEntry() != row.Entry ||
		view.Targets[encounterGUID(inconsistent.Guid)].TemplateEntry != nil {
		t.Fatal("per-entry tooltip missing or duplicated", view.CreatureTemplates)
	}
	encounterTestPacket(t, s, opcode.SMSGCreatureQueryResponse, packet[:len(packet)-1])
	encounterTestPacket(t, s, opcode.SMSGCreatureQueryResponse, sapTemplateReply(row.Entry+1, 1))
	if got := encounterTestRead(t, s, f).CreatureTemplates; len(got) != 1 || !proto.Equal(got["555"], view.CreatureTemplates["555"]) {
		t.Fatal("unrelated or truncated reply changed a current tooltip", got)
	}
	for i := uint32(0); i < 64; i++ {
		encounterTestPacket(t, s, opcode.SMSGCreatureQueryResponse, sapTemplateReply(1000+i, 7))
	}
	if got := encounterTestRead(t, s, f).CreatureTemplates; len(got) != 0 || len(s.creatureTooltips.entries) != 64 {
		t.Fatal("bounded metadata cache", len(got))
	}
	if len(s.controlTemplates) != 0 {
		t.Fatal("passive tooltip reply admitted a Sap query")
	}
}
