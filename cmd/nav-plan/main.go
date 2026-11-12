package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
)

const maxInput = 4096

type inputPoint struct {
	X *float32 `json:"x"`
	Y *float32 `json:"y"`
	Z *float32 `json:"z"`
}

type request struct {
	MapID *uint32     `json:"mapId"`
	Start *inputPoint `json:"start"`
	Goal  *inputPoint `json:"goal"`
}

type planner func(context.Context, string, uint32, buddynav.Point, buddynav.Point) ([]buddynav.Point, error)

func point(p *inputPoint) (buddynav.Point, error) {
	if p == nil || p.X == nil || p.Y == nil || p.Z == nil {
		return buddynav.Point{}, errors.New("start and goal require numeric x, y and z")
	}
	q := buddynav.Point{X: *p.X, Y: *p.Y, Z: *p.Z}
	if !finite(q) {
		return buddynav.Point{}, errors.New("coordinates must be finite")
	}
	return q, nil
}

func finite(p buddynav.Point) bool {
	for _, v := range []float32{p.X, p.Y, p.Z} {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return false
		}
	}
	return true
}

func distance(a, b buddynav.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func failure(out io.Writer, code string, err error) int {
	message := err.Error()
	if len(message) > 240 {
		message = message[:240]
	}
	_ = json.NewEncoder(out).Encode(map[string]any{"ok": false, "error": map[string]string{"code": code, "message": message}})
	return 1
}

func run(args []string, input io.Reader, output io.Writer, plan planner) int {
	flags := flag.NewFlagSet("nav-plan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("data-dir", "", "absolute directory of copied client maps, mmaps and vmaps")
	if err := flags.Parse(args); err != nil {
		return failure(output, "invalid_arguments", err)
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*dir) {
		return failure(output, "invalid_arguments", errors.New("--data-dir must name an absolute client asset directory; no positional arguments"))
	}
	for _, child := range []string{"maps", "mmaps", "vmaps"} {
		info, err := os.Stat(filepath.Join(*dir, child))
		if err != nil || !info.IsDir() {
			return failure(output, "invalid_assets", fmt.Errorf("client asset directory requires %s", child))
		}
	}
	raw, err := io.ReadAll(io.LimitReader(input, maxInput+1))
	if err != nil || len(raw) > maxInput {
		return failure(output, "invalid_input", errors.New("stdin must contain at most 4096 bytes"))
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var r request
	if err := decoder.Decode(&r); err != nil {
		return failure(output, "invalid_input", errors.New("expected one JSON object with mapId, start and goal"))
	}
	if decoder.Decode(new(any)) != io.EOF || r.MapID == nil {
		return failure(output, "invalid_input", errors.New("exactly one JSON object and an explicit mapId are required"))
	}
	start, err := point(r.Start)
	if err != nil {
		return failure(output, "invalid_input", err)
	}
	goal, err := point(r.Goal)
	if err != nil {
		return failure(output, "invalid_input", err)
	}
	// The planner owns its unchanged one-second budget and all physical checks.
	points, err := plan(context.Background(), filepath.Clean(*dir), *r.MapID, start, goal)
	if err != nil {
		return failure(output, "route_unavailable", err)
	}
	if len(points) < 2 || len(points) > 256 || distance(points[0], start) > 2 {
		return failure(output, "invalid_route", errors.New("planner returned an invalid route bound"))
	}
	var length float64
	for i, p := range points {
		if !finite(p) {
			return failure(output, "invalid_route", errors.New("planner returned non-finite geometry"))
		}
		if i > 0 {
			step := distance(points[i-1], p)
			if step > 2 || math.Abs(float64(p.Z-points[i-1].Z)) > 1 {
				return failure(output, "invalid_route", errors.New("planner returned non-dense geometry"))
			}
			length += step
		}
	}
	end := points[len(points)-1]
	if length > buddynav.LegMaxLength || distance(end, start) <= 1 && distance(goal, start) > 3 {
		return failure(output, "invalid_route", errors.New("planner returned excessive length or no progress"))
	}
	err = json.NewEncoder(output).Encode(map[string]any{
		"ok": true, "mapId": *r.MapID, "start": start, "goal": goal,
		"endpoint": end, "points": points, "pointCount": len(points),
		"pathLength": length, "goalReached": distance(end, goal) <= 2,
	})
	if err != nil {
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, buddynav.PlanLegContext)) }
