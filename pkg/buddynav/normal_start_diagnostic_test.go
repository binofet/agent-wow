package buddynav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/o0olele/detour-go/detour"
)

// Opt-in static-asset diagnostic. It never imports game transport or executes
// an action. Bearings/speed are hypothetical WALK inputs, not player intent or
// an observed movement speed. A later real action must replan its current body.
func TestNormalStartDiagnostic(t *testing.T) {
	raw := os.Getenv("BUDDY_NAV_NORMAL_START_INPUT")
	if raw == "" {
		t.Skip("set BUDDY_NAV_NORMAL_START_INPUT to the read-only asset query JSON")
	}
	if len(raw) > 4096 {
		t.Fatal("diagnostic input exceeds 4096 bytes")
	}
	var input struct {
		DataDir string  `json:"dataDir"`
		MapID   *uint32 `json:"mapId"`
		Start   *Point  `json:"start"`
		Target  *Point  `json:"target,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var shape map[string]json.RawMessage
	_ = json.Unmarshal([]byte(raw), &shape)
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.DataDir == "" || input.MapID == nil || input.Start == nil ||
		!normalStartPointPresent(shape["start"]) || !worldPoint(*input.Start) || input.Target != nil && (!normalStartPointPresent(shape["target"]) || !worldPoint(*input.Target)) {
		t.Fatal("expected <=4096-byte JSON {dataDir,mapId,start:{x,y,z},optional target:{x,y,z}}")
	}
	redact := func(err error) string {
		if err == nil {
			return ""
		}
		return strings.ReplaceAll(err.Error(), input.DataDir, "<dataDir>")
	}
	out := map[string]any{"format": 1, "staticAssetsOnly": true, "gameAction": false, "mapId": *input.MapID, "start": input.Start,
		"goVersion": runtime.Version(), "plannerUpstreamRevision": UpstreamRevision,
		"assetInventoryScope": "bounded window prerequisites and model caches retained after each query; not an exhaustive OS read trace"}
	defer func() {
		if recovered := recover(); recovered != nil {
			out["panic"] = redact(fmt.Errorf("diagnostic parser panic: %v", recovered))
			t.Error("static diagnostic parser panicked")
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			t.Fatal("diagnostic contained a non-JSON fact", err)
		}
		fmt.Printf("NORMAL_START_DIAGNOSTIC_JSON=%s\n", encoded)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // Diagnostic process budget, never a gameplay deadline.
	defer cancel()
	dir, err := mapDataDirectory(input.DataDir)
	if err != nil {
		out["setupError"] = redact(err)
		return
	}
	redact = func(err error) string {
		if err == nil {
			return ""
		}
		return strings.ReplaceAll(strings.ReplaceAll(err.Error(), dir, "<dataDir>"), input.DataDir, "<dataDir>")
	}
	w, err := newMapWindow(*input.MapID, *input.Start, *input.Start)
	if err != nil {
		out["setupError"] = redact(err)
		return
	}
	out["window"] = map[string]any{"gridX": w.x, "gridY": w.y, "meshTiles": w.meshTiles()}
	assets := map[string]int64{fmt.Sprintf("mmaps/%03d.mmap", w.mapID): 28, fmt.Sprintf("maps/%03d%02d%02d.map", w.mapID, w.x, w.y): 4 << 20,
		fmt.Sprintf("vmaps/%03d_%02d_%02d.vmtile", w.mapID, w.y, w.x): 2 << 20, fmt.Sprintf("vmaps/%03d.vmtree", w.mapID): 9}
	for _, name := range w.meshTiles() {
		assets["mmaps/"+name] = 32 << 20
	}
	withPlanner := func(window mapWindow, read func(*planner)) {
		for _, name := range window.meshTiles() {
			assets["mmaps/"+name] = 32 << 20
		}
		select {
		case <-ctx.Done():
			out["contextError"] = redact(ctx.Err())
			return
		case <-currentMapPlanners.gate:
		}
		defer func() { currentMapPlanners.gate <- struct{}{} }()
		p, err := cachedMapPlanner(ctx, dir, window)
		if err != nil {
			out["plannerError"] = redact(err)
			return
		}
		read(p)
		for name := range p.terrainHeaders {
			assets["maps/"+name] = 4 << 20
		}
		for name := range p.floors.tiles {
			assets["vmaps/"+name] = 2 << 20
		}
		for name := range p.floors.models {
			assets["vmaps/"+name+".vmo"] = p.floors.modelLimit
		}
		if p.floors.lastModel != "" {
			assets["vmaps/"+p.floors.lastModel+".vmo"] = p.floors.modelLimit
		}
		if p.floors.globalLoaded && !p.floors.globalTiled {
			assets[fmt.Sprintf("vmaps/%03d.vmtree", w.mapID)] = globalFloorBytes
		}
	}
	withPlanner(w, func(p *planner) {
		out["ground"] = normalStartGroundFacts(p, *input.Start, redact)
		terrain, terrainKnown := p.terrain(*input.Start)
		seconds := BodyMotionHorizon.Seconds()
		maxDrop := float32(BodyGravity * seconds * seconds / 2)
		collision, collisionKnown, floorErr := p.floors.heightContext(ctx, *input.Start, input.Start.Z, .5, .5+maxDrop)
		support, supportKnown := collision, collisionKnown && floorErr == nil && finite(collision)
		if floorErr == nil && terrainKnown && finite(terrain) && terrain >= input.Start.Z-maxDrop && terrain <= input.Start.Z+.5 && (!supportKnown || terrain > support) {
			support, supportKnown = terrain, true
		}
		physical := map[string]any{"terrainKnown": terrainKnown && finite(terrain), "terrain": normalStartNumber(terrain, terrainKnown),
			"collisionKnown": collisionKnown && floorErr == nil, "collision": normalStartNumber(collision, collisionKnown && floorErr == nil),
			"collisionError": redact(floorErr), "collisionModel": p.floors.lastModel, "supportKnown": supportKnown,
			"support": normalStartNumber(support, supportKnown), "maxGravityDrop": maxDrop, "contactBandYards": .025}
		if supportKnown {
			physical["startMinusSupport"] = float64(input.Start.Z) - float64(support)
		}
		out["physical"] = physical
		if input.Target != nil {
			out["target"] = input.Target
			out["targetCurrentObservation"] = false
			if !w.contains(*input.Target) || distance(*input.Start, *input.Target) > MaxRouteLength {
				out["route"] = map[string]any{"error": "target is outside the unchanged bounded start window or route budget"}
			} else {
				path, routeErr := rerouteBounded(ctx, p, func() ([]Point, error) {
					return planWithin(ctx, p, w.contains, "diagnostic fixed start window", *input.Start, *input.Target)
				})
				out["route"] = map[string]any{"points": path, "error": redact(routeErr)}
			}
		}
	})
	var walks []any
	for _, bearing := range []float64{0, math.Pi / 2, math.Pi, 3 * math.Pi / 2} {
		// Match PlanBodyMotionContext's exact loader window, including its
		// one-second speed envelope rather than just this 200ms WALK endpoint.
		envelope := *input.Start
		envelope.X += float32(math.Cos(bearing) * 7)
		envelope.Y += float32(math.Sin(bearing) * 7)
		bodyWindow, windowErr := newMapWindow(*input.MapID, *input.Start, envelope)
		points, planErr := PlanBodyMotionContext(ctx, dir, *input.MapID, *input.Start, bearing, 7, 200*time.Millisecond, false)
		walk := map[string]any{"absoluteBearingRadians": bearing, "speedYardsPerSecond": 7, "durationMs": 200, "jump": false,
			"error": redact(planErr), "endpointGround": map[string]any{"error": "no physically planned endpoint"}}
		var samples []any
		for _, sample := range points {
			samples = append(samples, map[string]any{"atMs": float64(sample.At) / float64(time.Millisecond), "point": sample.Point, "settling": sample.Settling})
		}
		walk["samples"] = samples
		if len(points) > 0 {
			walk["plannedDurationMs"] = float64(points[len(points)-1].At) / float64(time.Millisecond)
			walk["settlingOnly"] = points[0].Settling
		}
		if windowErr != nil {
			walk["windowError"] = redact(windowErr)
		} else {
			walk["window"] = map[string]any{"gridX": bodyWindow.x, "gridY": bodyWindow.y, "meshTiles": bodyWindow.meshTiles()}
			withPlanner(bodyWindow, func(p *planner) {
				if planErr == nil && len(points) > 0 {
					walk["endpointGround"] = normalStartGroundFacts(p, points[len(points)-1].Point, redact)
				}
			})
		}
		walks = append(walks, walk)
	}
	out["walkPlansOnly"] = walks
	names := make([]string, 0, len(assets))
	for name := range assets {
		names = append(names, name)
	}
	sort.Strings(names)
	var inventory []any
	for _, name := range names {
		inventory = append(inventory, normalStartAsset(dir, name, assets[name], redact))
	}
	out["assets"] = inventory
}

func normalStartPointPresent(raw json.RawMessage) bool {
	var values map[string]*float32
	if json.Unmarshal(raw, &values) != nil || len(values) != 3 {
		return false
	}
	for _, key := range []string{"x", "y", "z"} {
		if values[key] == nil || !finite(*values[key]) {
			return false
		}
	}
	return true
}

func normalStartNumber(v float32, known bool) any {
	if !known || !finite(v) {
		return nil
	}
	return v
}

func normalStartGroundFacts(p *planner, point Point, redact func(error) string) map[string]any {
	v, ext := recast(point), [3]float32{.5, 2, .5}
	var primary detour.DtPolyRef
	var nearest [3]float32
	primaryStatus := p.query.FindNearestPoly(v[:], ext[:], p.filter, &primary, nearest[:])
	var refs [maxGroundCandidates]detour.DtPolyRef
	var count int
	status := p.query.QueryPolygons(v[:], ext[:], p.filter, refs[:], &count, len(refs))
	complete := !detour.DtStatusFailed(status) && status&detour.DT_BUFFER_TOO_SMALL == 0 && count >= 0 && count <= len(refs)
	out := map[string]any{"point": point, "queryHalfExtentsRecast": ext, "primaryRef": strconv.FormatUint(uint64(primary), 10),
		"primaryStatus": fmt.Sprintf("0x%x", primaryStatus), "candidateCount": count, "candidateQueryComplete": complete, "candidateStatus": fmt.Sprintf("0x%x", status)}
	var candidates []any
	for _, ref := range refs[:max(0, min(count, len(refs)))] {
		var flags uint16
		flagStatus := p.mesh.GetPolyFlags(ref, &flags)
		var closest [3]float32
		var over bool
		closestStatus := p.query.ClosestPointOnPoly(ref, v[:], closest[:], &over)
		validClosest := !detour.DtStatusFailed(closestStatus) && finite(closest[0]) && finite(closest[1]) && finite(closest[2])
		candidate := map[string]any{"ref": strconv.FormatUint(uint64(ref), 10), "flagsKnown": !detour.DtStatusFailed(flagStatus), "flags": flags,
			"walkSurfaceFlags": !detour.DtStatusFailed(flagStatus) && walkSurfaceFlags(flags), "closestKnown": validClosest, "over": over}
		if validClosest {
			miss := math.Hypot(float64(closest[0]-v[0]), float64(closest[2]-v[2]))
			candidate["closest"] = Point{closest[2], closest[0], closest[1]}
			candidate["closestXYMiss"] = miss
			candidate["containsWithin2cm"] = over || miss <= .02
			var height float32
			heightStatus := p.query.GetPolyHeight(ref, closest[:], &height)
			candidate["closestHeight"] = normalStartNumber(height, !detour.DtStatusFailed(heightStatus))
		}
		height, err := p.groundHeightAt(ref, point)
		candidate["groundHeight"] = normalStartNumber(height, err == nil)
		candidate["groundError"] = redact(err)
		candidates = append(candidates, candidate)
	}
	ref, height, err := p.groundRef(point)
	out["candidates"], out["selectedRef"], out["height"], out["error"] = candidates, strconv.FormatUint(uint64(ref), 10), normalStartNumber(height, err == nil), redact(err)
	out["admitted"] = err == nil && ref != 0
	return out
}

func normalStartAsset(dir, name string, limit int64, redact func(error) string) map[string]any {
	out := map[string]any{"path": name}
	file, err := os.Open(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		out["error"] = redact(err)
		return out
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		out["error"] = "asset is not a readable regular file"
		return out
	}
	out["sizeBytes"] = info.Size()
	if limit == 9 { // The existing tiled VMAP reader consumes only this header.
		out["hashScope"] = "first9bytes"
	} else if limit <= 0 || info.Size() > limit {
		out["error"] = "asset exceeds existing reader bound"
		return out
	} else {
		out["hashScope"] = "completeFile"
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, limit))
	out["hashedBytes"] = n
	if err != nil {
		out["error"] = redact(err)
	} else {
		out["sha256"] = hex.EncodeToString(hash.Sum(nil))
	}
	return out
}
