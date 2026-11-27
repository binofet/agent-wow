package buddyactions

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
)

// Wrath FactionTemplate.dbc has fourteen little-endian uint32 fields. These
// template facts are only used for choosing an opportunity. The realm still
// owns reputation, interaction eligibility and all learned-node evidence.
type flightFactionTemplates struct {
	dir  string
	rows map[uint32][14]uint32
}

func readFlightFactionTemplates(dir string) map[uint32][14]uint32 {
	if dir == "" {
		return nil
	}
	file, err := os.Open(filepath.Join(dir, "dbc", "FactionTemplate.dbc"))
	if err != nil {
		return nil
	}
	defer file.Close()
	const limit = 256 << 10
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) < 20 || len(data) > limit || string(data[:4]) != "WDBC" {
		return nil
	}
	u32 := binary.LittleEndian.Uint32
	n := int64(u32(data[4:]))
	if n == 0 || u32(data[8:]) != 14 || u32(data[12:]) != 56 || 20+n*56+int64(u32(data[16:])) != int64(len(data)) {
		return nil
	}
	rows := make(map[uint32][14]uint32, n)
	for i := 0; i < int(n); i++ {
		var row [14]uint32
		for j := range row {
			row[j] = u32(data[20+56*i+4*j:])
		}
		if row[0] == 0 || rows[row[0]][0] != 0 {
			return nil
		}
		rows[row[0]] = row
	}
	return rows
}

func templateRelation(a, b [14]uint32) (friendly, hostile bool) {
	for _, enemy := range a[6:10] {
		if enemy != 0 && enemy == b[1] {
			return false, true
		}
	}
	for _, friend := range a[10:14] {
		if friend != 0 && friend == b[1] {
			return true, false
		}
	}
	return a[1] == b[1] || a[4]&b[3] != 0 || a[3]&b[4] != 0, a[5]&b[3] != 0
}

func (s *Service) flightmasterFriendlyLocked(self, npc uint32) bool {
	if self == 0 || npc == 0 {
		return false
	}
	if self == npc {
		return true
	}
	if s.flightFactions.dir != s.navDataDir {
		s.flightFactions = flightFactionTemplates{dir: s.navDataDir, rows: readFlightFactionTemplates(s.navDataDir)}
	}
	a, b := s.flightFactions.rows[self], s.flightFactions.rows[npc]
	if a[0] == 0 || b[0] == 0 {
		return false
	}
	ab, ah := templateRelation(a, b)
	ba, bh := templateRelation(b, a)
	return (ab || ba) && !ah && !bh
}

func (s *Service) currentFlightmasterLocked(w *worldv1.SnapshotResponse, expected *actionsv1.FlightmasterTarget) bool {
	c := w.GetRequestedCreature()
	if expected == nil || s.followRevision != expected.FollowRevision {
		return false
	}
	formation := s.followGoal
	currentFormation := formation.target.Valid && s.now().Before(formation.until) && formation.sessionID == s.sessionID &&
		formation.self == s.selfGUID && formation.visit == s.encounterVisit.revision && formation.followRevision == s.followRevision &&
		formation.hostEpoch == s.hostEncounterEpoch
	if expected.FollowOwnerGuid != 0 {
		owner := w.GetOwner()
		if s.independent || currentFormation && formation.owner != expected.FollowOwnerGuid ||
			!(expected.OwnerRangeYards > 0 && expected.OwnerRangeYards <= 40) ||
			owner.GetGuid() != expected.FollowOwnerGuid || !owner.Present || !owner.MapKnown || owner.MapId != w.GetSelf().GetMapId() ||
			!owner.AliveKnown || !owner.Alive || !owner.UnitFlagsKnown || owner.UnitFlags&0x80000 != 0 ||
			!owner.PositionKnown || owner.PositionInferred || !finite(playerPosition(owner)) ||
			c == nil || distance(playerPosition(owner), position{c.X, c.Y, c.Z, c.Orientation}) > float64(expected.OwnerRangeYards) {
			return false
		}
	} else if s.state.Following || currentFormation || expected.OwnerRangeYards != 0 {
		return false
	}
	return expected.Guid != 0 && expected.PositionRevision != 0 &&
		c.GetGuid() == expected.Guid && c.Entry != 0 && c.Entry == uint32((c.Guid>>24)&0xffffff) &&
		c.PositionRevision == expected.PositionRevision && c.Present && c.MapKnown && c.MapId == w.GetSelf().GetMapId() &&
		c.AliveKnown && c.Alive && c.HealthKnown && c.Health > 0 && c.UnitFlagsKnown && c.UnitFlags&(0x80000|0x2000000) == 0 &&
		c.NpcFlags&0x2000 != 0 && c.PositionKnown && finite(position{c.X, c.Y, c.Z, c.Orientation}) &&
		movementflags.GroundCompatible(c.MovementFlags, 0) && c.MovementFlags&0xff == 0 &&
		(c.Trajectory == nil || !c.Trajectory.Known || c.Trajectory.Stopped && !c.Trajectory.TransportRelative) &&
		(w.RequestedCreatureStationaryPositionKnown || c.PositionAgeMs <= 2000 && c.ObservationAgeMs <= 2000) &&
		s.flightmasterFriendlyLocked(w.GetSelf().GetFactionTemplate(), c.FactionTemplate)
}
