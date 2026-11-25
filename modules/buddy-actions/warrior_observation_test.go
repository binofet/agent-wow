package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
)

func TestEncounterReflectsLearnedWarriorMechanicsWithoutAutomaticClassifier(t *testing.T) {
	s, f, _ := genericCastRig(t)
	learnSpells(s, 355, 23922, 12294)
	self := f.world.Self
	self.ClassKnown, self.ClassId, self.PowerKnown, self.PowerType, self.Power, self.MaxPower = true, 1, true, 1, 1000, 1000
	self.ShapeshiftFormKnown, self.ShapeshiftForm = true, 18
	out := &actionsv1.EncounterObservationResponse{Spells: map[string]*actionsv1.EncounterSpell{}}
	s.encounterSpells(f.world, nil, out)
	for _, id := range []string{"355", "23922", "12294"} {
		r := out.Spells[id]
		if r == nil || !r.Known || !r.MetadataKnown || r.PowerType != 1 || r.Capability != "generic" {
			t.Fatal(id, r)
		}
	}
	// Projection remains a read; the normal generic learned-spell transport
	// sends exactly the requested class ability, with no new handler/writer.
	if len(f.packets) != 0 {
		t.Fatal(f.packets)
	}
	req := genericRequest(s, 23922)
	if _, err := s.CastSpell(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCastSpell {
		t.Fatal(f.packets)
	}
}
