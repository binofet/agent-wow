// buddynav validates static navigation assets without contacting a game server.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func point(s string) (buddynav.Point, error) {
	v := strings.Split(s, ",")
	if len(v) != 3 {
		return buddynav.Point{}, fmt.Errorf("point must be x,y,z")
	}
	var a [3]float32
	for i := range v {
		n, e := strconv.ParseFloat(strings.TrimSpace(v[i]), 32)
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return buddynav.Point{}, fmt.Errorf("point must contain three finite numbers")
		}
		a[i] = float32(n)
	}
	return buddynav.Point{X: a[0], Y: a[1], Z: a[2]}, nil
}
func run() error {
	dir := flag.String("data-dir", os.Getenv("BUDDY_NAV_DATA_DIR"), "static assets root containing mmaps/ and maps/")
	mapID := flag.Uint("map", 0, "map ID (only 0 is supported)")
	startText := flag.String("start", "", "WoW start x,y,z")
	destText := flag.String("dest", "", "WoW destination x,y,z")
	flag.Parse()
	if flag.NArg() != 0 || uint64(*mapID) > math.MaxUint32 {
		return fmt.Errorf("unexpected arguments or invalid map ID")
	}
	start, err := point(*startText)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	dest, err := point(*destText)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	points, err := buddynav.PlanContext(ctx, *dir, uint32(*mapID), start, dest)
	if err != nil {
		return err
	}
	var length float64
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
		length += math.Sqrt(dx*dx + dy*dy + dz*dz)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		MapID    uint32           `json:"mapId"`
		Complete bool             `json:"complete"`
		Length   float64          `json:"lengthYards"`
		Revision string           `json:"upstreamRevision"`
		Points   []buddynav.Point `json:"points"`
	}{uint32(*mapID), true, length, buddynav.UpstreamRevision, points})
}
