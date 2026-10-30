package buddyworld

import worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"

// Only the session character's private slot GUID and actually owned item
// observations establish equipment. Nearby visible item entries are not proof.
func (s *observations) offhandSnapshot() *worldv1.EquippedWeapon {
	p := s.players[0]
	r := &worldv1.EquippedWeapon{}
	if !p.isSelf || !p.present || !p.offhandKnown || p.offhand != 0 && !itemGUID(p.offhand) {
		return r
	}
	r.SlotKnown, r.ItemGuid = true, p.offhand
	item := s.items[p.offhand]
	if item == nil {
		return r
	}
	owner := uint64(item.fields[itemOwner]) | uint64(item.fields[itemOwner+1])<<32
	if !item.present || !item.known[objectEntry] || !item.known[itemOwner] || !item.known[itemOwner+1] || owner != p.guid || item.fields[objectEntry] == 0 {
		return r
	}
	r.MetadataKnown, r.Entry = true, item.fields[objectEntry]
	r.Durability, r.MaxDurability = item.fields[0x3c], item.fields[0x3d]
	r.DurabilityKnown = item.known[0x3c] && item.known[0x3d] && r.Durability <= r.MaxDurability
	return r
}
