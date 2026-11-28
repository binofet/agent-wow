// navsurface generates one moving model's local Detour mesh from installed
// client collision geometry. It never connects to the game.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
)

func generate() error {
	root := flag.String("data-dir", "", "installed navigation data directory")
	display := flag.Uint("display-id", 0, "observed GameObject display ID")
	generator := flag.String("generator", "", "compiled tools/navsurface/recast.cpp executable")
	output := flag.String("output", "", "new local surface tile file")
	flag.Parse()
	if *display == 0 || uint64(*display) > 0xffffffff || *generator == "" || *output == "" || flag.NArg() != 0 {
		return fmt.Errorf("data-dir, exact display-id, generator and new output required")
	}
	surface, err := buddynav.LoadTransportSurface(context.Background(), *root, uint32(*display))
	if err != nil {
		return err
	}
	vertices, triangles := surface.Triangles()
	var input bytes.Buffer
	binary.Write(&input, binary.LittleEndian, [3]uint32{0x31535741, uint32(len(vertices)), uint32(len(triangles))})
	// Match the existing body's quarter-yard corridor and head contact, with
	// half-yard steps. Recast erodes edges and checks overhead clearance.
	settings := [6]float32{.25, 1.8, .5, 45, .1, .05}
	binary.Write(&input, binary.LittleEndian, settings)
	for _, p := range vertices {
		binary.Write(&input, binary.LittleEndian, [3]float32{p.X, p.Z, -p.Y})
	}
	for _, t := range triangles {
		binary.Write(&input, binary.LittleEndian, [3]int32{int32(t[0]), int32(t[1]), int32(t[2])})
	}
	command := exec.Command(*generator)
	command.Stdin, command.Stderr = &input, os.Stderr
	data, err := command.Output()
	if err != nil {
		return fmt.Errorf("Recast surface generation: %w", err)
	}
	sum := sha256.Sum256(data)
	metadata, err := json.Marshal(buddynav.SurfaceMeshMetadata{Version: 1, DisplayID: uint32(*display),
		Model: surface.ModelName, ModelSHA: surface.ModelSHA, MeshSHA: hex.EncodeToString(sum[:]),
		Generator: "recastnavigation/b4554541b658630816dba41466eb1cefb624519e", PolyRefBits: 64, BodyRadius: settings[0],
		BodyHeight: settings[1], BodyClimb: settings[2], CellSize: settings[4], CellHeight: settings[5]})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	var encoded bytes.Buffer
	encoded.WriteString("AWSURF1\x00")
	binary.Write(&encoded, binary.LittleEndian, uint32(len(metadata)))
	encoded.Write(metadata)
	encoded.Write(data)
	_, err = file.Write(encoded.Bytes())
	return err
}

func main() {
	if err := generate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
