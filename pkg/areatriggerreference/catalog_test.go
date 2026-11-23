package areatriggerreference

import (
	"math"
	"testing"
)

func TestReferenceMaraudonEntrancesAreOrientedBoxesNotSpheres(t *testing.T) {
	rows := All()
	for _, id := range []uint32{3133, 3134} {
		var found bool
		for _, row := range rows {
			if row.ID != id {
				continue
			}
			found = true
			if row.SourceMap != 1 || row.DestinationMap != 349 || row.Radius != 0 ||
				!row.Contains(1, row.X, row.Y, row.Z, .03) || row.Contains(0, row.X, row.Y, row.Z, 0) {
				t.Fatal("invalid Maraudon reference volume", row)
			}
			c, s := math.Cos(row.Orientation), math.Sin(row.Orientation)
			if !row.Contains(1, row.X+c*(row.Length/2-.04), row.Y+s*(row.Length/2-.04), row.Z, .03) ||
				row.Contains(1, row.X+c*(row.Length/2+.04), row.Y+s*(row.Length/2+.04), row.Z, 0) ||
				row.Contains(1, row.X, row.Y, row.Z+row.Height/2+.01, 0) {
				t.Fatal("rotated box bounds or height were ignored", row)
			}
		}
		if !found {
			t.Fatal("missing pinned Maraudon reference", id)
		}
	}
}

func TestReferenceVolumesRejectInvalidInputAndRemainDetached(t *testing.T) {
	sphere := Trigger{ID: 1, SourceMap: 0, DestinationMap: 1, Radius: 5}
	if !sphere.Contains(0, 4.9, 0, 0, .03) || sphere.Contains(0, 5, 0, 0, .03) ||
		sphere.Contains(0, math.NaN(), 0, 0, 0) || sphere.Contains(0, 0, 0, 0, -1) ||
		(Trigger{ID: 2, DestinationMap: 1}).Contains(0, 0, 0, 0, 0) {
		t.Fatal("invalid volume admitted")
	}
	rows := All()
	old := rows[0]
	rows[0].X = math.Inf(1)
	if All()[0] != old {
		t.Fatal("caller mutated shared geometry reference")
	}
}
