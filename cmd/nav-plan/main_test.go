package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
)

func assets(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, child := range []string{"maps", "mmaps", "vmaps"} {
		if err := os.Mkdir(filepath.Join(dir, child), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const validInput = `{"mapId":0,"start":{"x":0,"y":0,"z":0},"goal":{"x":1,"y":0,"z":0}}`

func TestExplicitZeroCoordinatesAndExactPlannerGeometry(t *testing.T) {
	dir := assets(t)
	var out bytes.Buffer
	path := []buddynav.Point{{X: 0, Y: 0, Z: 0}, {X: 1, Y: 0, Z: 0}}
	code := run([]string{"--data-dir", dir}, strings.NewReader(validInput), &out,
		func(_ context.Context, got string, mapID uint32, start, goal buddynav.Point) ([]buddynav.Point, error) {
			if got != dir || mapID != 0 || start != path[0] || goal != path[1] {
				t.Fatal("input not forwarded exactly", got, mapID, start, goal)
			}
			return path, nil
		})
	var response struct {
		OK       bool             `json:"ok"`
		Points   []buddynav.Point `json:"points"`
		Endpoint buddynav.Point   `json:"endpoint"`
		Length   float64          `json:"pathLength"`
	}
	if code != 0 || json.Unmarshal(out.Bytes(), &response) != nil || !response.OK || response.Length != 1 || response.Endpoint != path[1] || len(response.Points) != 2 || response.Points[0] != path[0] || response.Points[1] != path[1] {
		t.Fatal(code, out.String())
	}
}

func TestInvalidInputNeverReachesPlanner(t *testing.T) {
	for _, input := range []string{"", "null", "[]", `{}`, strings.Replace(validInput, `"mapId":0,`, "", 1), strings.Replace(validInput, `"x":0,`, "", 1), strings.Replace(validInput, `"x":0`, `"x":null`, 1), strings.Replace(validInput, `"x":0`, `"x":"0"`, 1), strings.Replace(validInput, `"x":0`, `"x":1e99`, 1), strings.Replace(validInput, `"mapId":0`, `"mapId":-1`, 1), strings.Replace(validInput, `"mapId":0`, `"mapId":0,"path":[]`, 1), validInput + " {}", validInput + strings.Repeat(" ", maxInput)} {
		t.Run(input[:min(len(input), 25)], func(t *testing.T) {
			var out bytes.Buffer
			code := run([]string{"--data-dir", assets(t)}, strings.NewReader(input), &out, func(context.Context, string, uint32, buddynav.Point, buddynav.Point) ([]buddynav.Point, error) {
				t.Fatal("invalid input reached planner")
				return nil, nil
			})
			var r map[string]any
			if code == 0 || json.Unmarshal(out.Bytes(), &r) != nil || r["ok"] != false || r["error"] == nil || r["points"] != nil {
				t.Fatal(code, out.String())
			}
		})
	}
}

func TestFailedAndMalformedPlansNeverEmitSuccess(t *testing.T) {
	for _, mode := range []string{"unavailable", "missing", "nonfinite", "jump", "height", "length", "wrong_start", "no_progress"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			code := run([]string{"--data-dir", assets(t)}, strings.NewReader(strings.Replace(validInput, `"x":1`, `"x":10`, 1)), &out, func(context.Context, string, uint32, buddynav.Point, buddynav.Point) ([]buddynav.Point, error) {
				path := []buddynav.Point{{X: 0, Y: 0, Z: 0}, {X: 1, Y: 0, Z: 0}}
				switch mode {
				case "unavailable":
					return nil, errors.New("no supported ground polygon")
				case "missing":
					return nil, nil
				case "nonfinite":
					path[1].X = float32(math.NaN())
				case "jump":
					path[1].X = 3
				case "height":
					path[1].Z = 1.1
				case "length":
					path = make([]buddynav.Point, 182)
					for i := range path {
						path[i].X = float32(i)
					}
				case "wrong_start":
					path[0].X = -3
				case "no_progress":
					path[1].X = 0
				}
				return path, nil
			})
			var r map[string]any
			if code == 0 || json.Unmarshal(out.Bytes(), &r) != nil || r["ok"] != false || r["points"] != nil {
				t.Fatal(code, out.String())
			}
		})
	}
}

func TestAssetAndArgumentFailuresAreStructured(t *testing.T) {
	for _, args := range [][]string{nil, {"--data-dir", "relative"}, {"--data-dir", t.TempDir()}, {"--other"}, {"--data-dir", assets(t), "extra"}} {
		var out bytes.Buffer
		code := run(args, strings.NewReader(validInput), &out, nil)
		if code == 0 || !json.Valid(out.Bytes()) || !bytes.Contains(out.Bytes(), []byte(`"ok":false`)) {
			t.Fatal(code, out.String())
		}
	}
}
