package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	contract "github.com/agent-wow/agent-wow/pkg/behaviorcontract"
	"github.com/agent-wow/agent-wow/pkg/dungeontrace"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestAutomaticDungeonTraceRecordsSeparateVisits(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = "auto-test-body"
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableAutomaticDungeonTrace(directory); err != nil {
		t.Fatal(err)
	}
	packet := func(op, mapID uint32) {
		size := 20
		if op == opcode.SMSGTransferPending {
			size = 4
		}
		payload := make([]byte, size)
		binary.LittleEndian.PutUint32(payload, mapID)
		if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	observe := func(mapID uint32, revision uint64) {
		f.world.Self.MapId, f.world.Owner.MapId = mapID, mapID
		f.world.Self.PositionRevision = revision
		s.observeDungeonWorld(f.world)
	}
	packet(opcode.SMSGLoginVerifyWorld, 48)
	observe(48, 1) // Reconnecting inside records the visible remainder as a partial visit.
	if s.dungeonTrace == nil || !s.dungeonTrace.Active() {
		t.Fatal("matched dungeon login did not start a partial visit")
	}
	packet(opcode.SMSGTransferPending, 48)
	packet(opcode.SMSGNewWorld, 48)
	observe(48, 1)
	if s.dungeonTrace != nil {
		t.Fatal("old same-map snapshot started a second visit")
	}
	observe(48, 2)
	if s.dungeonTrace == nil || !s.dungeonTrace.Active() {
		t.Fatal("new same-map visit did not start")
	}
	packet(opcode.SMSGTransferPending, 1)
	packet(opcode.SMSGNewWorld, 1)
	observe(1, 3)
	if s.dungeonTrace != nil {
		t.Fatal("outdoor map began recording")
	}
	packet(opcode.SMSGTransferPending, 34)
	observe(34, 4)
	if s.dungeonTrace != nil {
		t.Fatal("world snapshot started recording ahead of the server boundary")
	}
	packet(opcode.SMSGNewWorld, 34)
	observe(34, 4)
	if s.dungeonTrace == nil || !s.dungeonTrace.Active() {
		t.Fatal("different dungeon visit did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.FlushDungeonTrace(ctx)
	paths, err := filepath.Glob(filepath.Join(directory, "map-*", dungeontrace.Filename))
	if err != nil || len(paths) != 3 {
		t.Fatalf("want three distinct visit traces, got %v, %v", paths, err)
	}
	starts := map[string]int{}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSpace(content), []byte{'\n'})
		var start, end dungeontrace.Row
		if json.Unmarshal(lines[0], &start) != nil || json.Unmarshal(lines[len(lines)-1], &end) != nil || start.Sequence != 1 || start.Kind != "start" || end.Kind != "end" || start.MapID != end.MapID {
			t.Fatalf("malformed visit %s: %s", path, content)
		}
		starts[string(start.Payload)]++
		if !strings.HasPrefix(filepath.Base(filepath.Dir(path)), "map-") {
			t.Fatal(path)
		}
	}
	if starts[`{"reason":"observed_dungeon_login_partial_visit"}`] != 1 || starts[`{"reason":"observed_map_entry"}`] != 2 {
		t.Fatal(starts)
	}
}

func TestDungeonTraceRoundTripFromPacketAndDecisionHooks(t *testing.T) {
	ctx := context.Background()
	s, f, step := rig(t)
	s.sessionID = "actions-test-body"
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableDungeonTrace(directory, 36); err != nil {
		t.Fatal(err)
	}
	packet := func(op uint32, payload []byte) {
		t.Helper()
		if _, err := s.OnPacket(ctx, &modv1.WorldPacket{Opcode: op, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	world := func(op uint32, mapID uint32) {
		payload := make([]byte, 20)
		binary.LittleEndian.PutUint32(payload, mapID)
		packet(op, payload)
	}
	observe := func() {
		t.Helper()
		if _, err := s.snapshot(ctx); err != nil {
			t.Fatal(err)
		}
	}
	world(opcode.SMSGLoginVerifyWorld, 0)
	observe()
	step(time.Second)
	packet(opcode.SMSGTransferPending, []byte{36, 0, 0, 0})
	f.world.Self.MapId, f.world.Owner.MapId = 36, 36
	f.world.Owner.TargetKnown, f.world.Owner.TargetGuid = true, 0xf130000001234567
	f.world.Owner.Orientation = 1.5
	observe() // The world module can see entry before the actions callback.
	if s.dungeonTrace.Active() {
		t.Fatal("trace started before the actions module saw the entry boundary")
	}
	world(opcode.SMSGNewWorld, 36)
	observe()
	if !s.dungeonTrace.Active() {
		t.Fatal("trace did not start after matching packet and world snapshot")
	}

	step(time.Millisecond)
	packet(opcode.SMSGSpellFailure, healingRejectionPacket(opcode.SMSGSpellFailure, ownerGUID, 7, 585).Payload)
	step(time.Millisecond)
	revision := loadBehavior(t, s, `return {decide=function(s) return {targetGuid=s.defaultTargetGuid} end}`)
	in := contract.Input{Kind: "heal_target", Self: combatant(f.world.Self), DefaultTargetGuid: "3"}
	if got := s.behaviorDecisionLocked(in); got.TargetGuid != "3" {
		t.Fatal("recorder altered the Lua policy")
	}
	in.DefaultTargetGuid = "912345"
	s.behaviorDecisionLocked(in) // The exact payload is out of recording scope.
	step(time.Millisecond)
	packet(opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
	world(opcode.SMSGNewWorld, 0)
	flush, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s.FlushDungeonTrace(flush)
	content, err := os.ReadFile(filepath.Join(directory, dungeontrace.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte("912345")) {
		t.Fatal("trace retained an out-of-scope player")
	}

	var rows []dungeontrace.Row
	for _, line := range bytes.Split(bytes.TrimSpace(content), []byte{'\n'}) {
		var row dungeontrace.Row
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	var kinds []string
	for i, row := range rows {
		kinds = append(kinds, row.Kind)
		if row.Sequence != uint64(i+1) || row.SessionID != s.sessionID || row.MapID != 36 || row.ElapsedNS < 0 {
			t.Fatalf("non-joinable record %d: %+v", i, row)
		}
		if row.Kind == "policy_input" && (!bytes.Contains(row.Payload, []byte(revision)) ||
			!bytes.Contains(row.Payload, []byte(`"defaultTargetGuid":"3"`))) {
			t.Fatal("trace did not retain the exact in-scope decision input")
		}
	}
	if !slices.Equal(kinds, []string{"start", "owner_observation", "owner_cast", "policy_input", "observation_gap", "visibility", "end"}) {
		t.Fatalf("unexpected trace kinds: %v", kinds)
	}
}
