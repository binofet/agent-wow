// Package areatriggerreference exposes pinned public trigger-volume references.
// These are neither deployed realm facts, walkable routes nor admission/arrival
// receipts. The realm still validates its actual trigger and instance access.
package areatriggerreference

import (
	_ "embed"
	"encoding/json"
	"math"
)

const Commit = "b8c2e189651d262ab94d6f012b1a9d38591a091b"

type Trigger struct {
	ID             uint32  `json:"id"`
	SourceMap      uint32  `json:"sourceMap"`
	DestinationMap uint32  `json:"destinationMap"`
	X              float64 `json:"x"`
	Y              float64 `json:"y"`
	Z              float64 `json:"z"`
	Radius         float64 `json:"radius"`
	Length         float64 `json:"length"`
	Width          float64 `json:"width"`
	Height         float64 `json:"height"`
	Orientation    float64 `json:"orientation"`
}

// Contains uses the ordinary sphere/oriented-box definition, conservatively
// excluding the caller's inner margin. It does not expand player reach or use
// the teleport's destination coordinates as an approach point.
func (t Trigger) Contains(mapID uint32, x, y, z, margin float64) bool {
	if mapID != t.SourceMap || !t.valid() || !finite(x, y, z, margin) || margin < 0 {
		return false
	}
	dx, dy, dz := x-t.X, y-t.Y, z-t.Z
	if t.Radius > 0 {
		r := t.Radius - margin
		return r > 0 && dx*dx+dy*dy+dz*dz <= r*r
	}
	c, s := math.Cos(t.Orientation), math.Sin(t.Orientation)
	localX, localY := dx*c+dy*s, -dx*s+dy*c
	return t.Length/2 > margin && t.Width/2 > margin && t.Height/2 > margin &&
		math.Abs(localX) <= t.Length/2-margin && math.Abs(localY) <= t.Width/2-margin && math.Abs(dz) <= t.Height/2-margin
}

func finite(values ...float64) bool {
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

func (t Trigger) valid() bool {
	return t.ID != 0 && t.SourceMap != t.DestinationMap &&
		finite(t.X, t.Y, t.Z, t.Radius, t.Length, t.Width, t.Height, t.Orientation) &&
		t.Radius >= 0 && t.Length >= 0 && t.Width >= 0 && t.Height >= 0 &&
		(t.Radius > 0 || t.Length > 0 && t.Width > 0 && t.Height > 0)
}

//go:embed reference.json
var data []byte

var triggers = func() []Trigger {
	var document struct {
		Format          uint32    `json:"format"`
		Commit          string    `json:"commit"`
		GeometrySHA256  string    `json:"geometrySHA256"`
		TeleportsSHA256 string    `json:"teleportsSHA256"`
		Triggers        []Trigger `json:"triggers"`
	}
	if err := json.Unmarshal(data, &document); err != nil || document.Format != 1 || document.Commit != Commit ||
		document.GeometrySHA256 != "6f9e067e66812e485b80d194e316e44989396e666a77d81a6f0845b5ca987e10" ||
		document.TeleportsSHA256 != "94acced79fe57b7338376102d51dcb8d47aafa0c3a551f0289db8d636a178fcc" || len(document.Triggers) != 222 {
		panic("invalid pinned area-trigger references")
	}
	var previous uint32
	for _, row := range document.Triggers {
		if !row.valid() || row.ID <= previous {
			panic("invalid or duplicate area-trigger reference")
		}
		previous = row.ID
	}
	return document.Triggers
}()

// All returns detached value records; callers cannot mutate the shared source.
func All() []Trigger { return append([]Trigger(nil), triggers...) }
