package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	contract "github.com/agent-wow/agent-wow/pkg/behaviorcontract"
	"github.com/agent-wow/agent-wow/pkg/dungeontrace"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

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
