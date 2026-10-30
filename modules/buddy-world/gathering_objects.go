package buddyworld

import (
	"math"
	"sort"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// The public type only identifies a chest candidate. No template, lock or
// profession eligibility is inferred, and the bounded cache is never changed.
func (s *observations) appendGatheringObjects(r *worldv1.SnapshotResponse, now time.Time) {
	type candidate struct {
		object   *observed
		distance float64
	}
	var rows []candidate
	for _, p := range s.gameObjects {
		if !gameObjectGUID(p.guid) || !p.present || !p.known[objectEntry] || p.fields[objectEntry] == 0 || uint32(p.guid>>24&0xffffff) != p.fields[objectEntry] || !p.known[gameObjectBytes] || p.fields[gameObjectBytes]>>8&0xff != 3 {
			continue
		}
		d, known := observedDistance(*p, s.players[0])
		if !known || math.IsNaN(d) || math.IsInf(d, 0) {
			continue
		}
		rows = append(rows, candidate{p, d})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].distance != rows[j].distance {
			return rows[i].distance < rows[j].distance
		}
		return rows[i].object.guid < rows[j].object.guid
	})
	if len(rows) > 32 {
		rows = rows[:32]
	}
	for _, row := range rows {
		r.NearbyGatheringObjects = append(r.NearbyGatheringObjects, gameObjectSnapshot(row.object, now))
	}
}
