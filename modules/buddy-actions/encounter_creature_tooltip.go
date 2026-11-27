package buddyactions

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/creaturequery"
	"google.golang.org/protobuf/proto"
)

// One locale/realm session owns these static replies. Whole rows are replaced
// when new data arrives so a previously returned observation cannot change.
type encounterCreatureTooltips struct {
	entries map[uint32]*actionsv1.EncounterCreatureTemplate
	order   [64]uint32
	next    int
}

func (c *encounterCreatureTooltips) store(t creaturequery.Template) {
	if c.entries == nil {
		c.entries = make(map[uint32]*actionsv1.EncounterCreatureTemplate)
	}
	if c.entries[t.Entry] == nil {
		delete(c.entries, c.order[c.next])
		c.order[c.next] = t.Entry
		c.next = (c.next + 1) % len(c.order)
	}
	c.entries[t.Entry] = &actionsv1.EncounterCreatureTemplate{
		Entry: t.Entry, Name: t.Name, SubName: t.SubName, CreatureType: t.CreatureType,
		TypeFlags: proto.Uint32(t.TypeFlags), Family: proto.Uint32(t.Family), Rank: proto.Uint32(t.Rank),
	}
}
