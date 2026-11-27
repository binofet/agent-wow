package buddyactions

import (
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
)

// A speed buff or mounted flag alone does not identify a ground mount.
func (s *Service) groundMountAuraLocked(guid uint64) (uint32, uint64) {
	set := s.buffAuras[guid]
	if set == nil {
		return 0, 0
	}
	for _, a := range set.slots {
		if a.spell != 0 && a.caster == guid && a.positive && !a.negative &&
			(!a.durationKnown || a.remaining(s.now()) > 0) && s.spellCatalogLocked().GroundMountSpeed(a.spell) > 0 {
			return a.spell, a.revision
		}
	}
	return 0, 0
}

func (s *Service) groundTravelUnitFlags(p *worldv1.PlayerState) uint32 {
	flags := p.GetUnitFlags()
	if flags&movementflags.Mounted == 0 || !p.GetUnitFlagsKnown() || !p.GetMovementFlagsKnown() ||
		!movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) ||
		p.UnitFlags_2&movementflags.ForcedOrFeign != 0 || observedRunSpeed(p) == 0 {
		return flags
	}
	if id, _ := s.groundMountAuraLocked(p.Guid); id != 0 {
		return flags &^ movementflags.Mounted
	}
	return flags
}
