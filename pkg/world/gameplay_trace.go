package world

import (
	"context"
	"encoding/hex"
	"sync"

	"github.com/agent-wow/agent-wow/pkg/modules/session"
)

// GameplayWriteOutcome describes a client-side transport result.
type GameplayWriteOutcome string

const (
	GameplayWritten        GameplayWriteOutcome = "written"
	GameplayWriteFailed    GameplayWriteOutcome = "write_failed"
	GameplayDeadlineFailed GameplayWriteOutcome = "deadline_failed"
)

// GameplayWrite is a completed owner-loop transport attempt. Written means
// the whole packet was accepted by the connection. A failed write can still
// have accepted a prefix. Neither outcome establishes a server-side effect.
type GameplayWrite struct {
	Sequence      uint64                 `json:"sequence"`
	CharacterGUID uint64                 `json:"characterGuid,string"`
	Origin        modsession.WriteOrigin `json:"origin"`
	Opcode        uint32                 `json:"opcode"`
	PayloadHex    string                 `json:"payloadHex"`
	Outcome       GameplayWriteOutcome   `json:"outcome"`
	AnyBytes      bool                   `json:"anyBytes"`
}

// GameplayTrace retains a bounded prefix of the actual gameplay transport
// attempts. It has no callbacks, file output or dependency on the controller.
// Allocate one per observed session, only in a harness authorized to read
// gameplay payloads.
type GameplayTrace struct {
	mu       sync.Mutex
	attempts []GameplayWrite
	dropped  uint64
}

// NewGameplayTrace clamps the retained attempt count to 1..1024.
func NewGameplayTrace(limit int) *GameplayTrace {
	return &GameplayTrace{attempts: make([]GameplayWrite, 0, min(max(limit, 1), 1024))}
}

type GameplayTraceSnapshot struct {
	Attempts []GameplayWrite `json:"attempts"`
	Dropped  uint64          `json:"dropped"`
}

// Snapshot copies the retained records. A nonzero Dropped value makes a
// complete replay impossible.
func (t *GameplayTrace) Snapshot() GameplayTraceSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	snapshot := GameplayTraceSnapshot{Attempts: make([]GameplayWrite, len(t.attempts)), Dropped: t.dropped}
	copy(snapshot.Attempts, t.attempts)
	return snapshot
}

func (t *GameplayTrace) record(ctx context.Context, characterGUID uint64, op uint32, body []byte, outcome GameplayWriteOutcome, anyBytes bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.attempts) == cap(t.attempts) {
		t.dropped++
		return
	}
	t.attempts = append(t.attempts, GameplayWrite{
		Sequence: uint64(len(t.attempts)) + 1, CharacterGUID: characterGUID,
		Origin: modsession.WriteOriginFromContext(ctx), Opcode: op,
		PayloadHex: hex.EncodeToString(body), Outcome: outcome, AnyBytes: anyBytes,
	})
}
