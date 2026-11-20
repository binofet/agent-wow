package world

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// TestLIFE37FixtureTransport drives only the synthetic fixture's prescribed
// actions. No controller decides these writes. Every positive attempt passes
// through an ordinary world session and is read by its independently framed
// mock peer. The separate fixture checker decodes the trace's spell payloads.
func TestLIFE37FixtureTransport(t *testing.T) {
	fixturePath := os.Getenv("WB_LIFE37_FIXTURE")
	if fixturePath == "" {
		t.Skip("set WB_LIFE37_FIXTURE to the synthetic observation fixture")
	}
	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	type action struct {
		ID         string
		ActorGUID  string
		TargetGUID uint64 `json:"targetGuid,string"`
		SpellID    uint32
	}
	var fixture struct {
		Provenance string
		MapID      uint32
		Actors     map[string]struct{ SessionID string }
		Frames     []struct {
			ID   string
			Emit []action
		}
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil || fixture.Provenance != "synthetic" ||
		fixture.MapID != 389 || len(fixture.Actors) != 5 || len(fixture.Frames) == 0 {
		t.Fatalf("expected the synthetic map-389 fixture: %v", err)
	}

	type body struct {
		session   *Session
		trace     *GameplayTrace
		id        string
		castCount byte
	}
	bodies := make(map[string]*body)
	orderedActors := make([]string, 0, len(fixture.Actors))
	for actor := range fixture.Actors {
		orderedActors = append(orderedActors, actor)
	}
	sort.Strings(orderedActors)
	for _, actor := range orderedActors {
		identity, err := strconv.ParseUint(actor, 10, 64)
		if err != nil || identity == 0 || fixture.Actors[actor].SessionID == "" {
			t.Fatalf("invalid actor identity: %q", actor)
		}
		var expected [][]byte
		for _, frame := range fixture.Frames {
			for _, request := range frame.Emit {
				if request.ActorGUID == actor {
					expected = append(expected, fixtureSpellBody(byte(len(expected)+1), request.SpellID, request.TargetGUID))
				}
			}
		}
		trace := NewGameplayTrace(32)
		conn := peerConnection(t, func(peer *testPeer) {
			if got := peer.expect(opcode.CMSGPlayerLogin); !bytes.Equal(got, binary.LittleEndian.AppendUint64(nil, identity)) {
				t.Errorf("wrong fixture login for %s: %x", actor, got)
			}
			peer.send(opcode.SMSGLoginVerifyWorld, positionBody(fixture.MapID, 1, 2, 3, .5))
			peer.sync(5)
			for _, want := range expected {
				if got := peer.expect(opcode.CMSGCastSpell); !bytes.Equal(got, want) {
					t.Errorf("unexpected spell bytes: %x, want %x", got, want)
				}
			}
			peer.closed()
		})
		s := enterTracedTestSession(t, conn, quietTimings(), identity, trace)
		bodies[actor] = &body{session: s, trace: trace, id: fixture.Actors[actor].SessionID}
	}

	type frameResult struct {
		FrameID           string          `json:"frameId"`
		TransportAttempts []GameplayWrite `json:"transportAttempts"`
	}
	type sessionResult struct {
		AttemptCount int    `json:"attemptCount"`
		Dropped      uint64 `json:"dropped"`
	}
	capture := struct {
		Schema        string                   `json:"schema"`
		Mode          string                   `json:"mode"`
		FixtureSHA256 string                   `json:"fixtureSHA256"`
		Frames        []frameResult            `json:"frames"`
		Sessions      map[string]sessionResult `json:"sessions"`
	}{
		Schema: "wb.life37.gameplay-write/1", Mode: "scripted_transport_smoke",
		FixtureSHA256: fmt.Sprintf("%x", sha256.Sum256(fixtureBytes)),
		Sessions:      make(map[string]sessionResult),
	}
	for _, frame := range fixture.Frames {
		result := frameResult{FrameID: frame.ID, TransportAttempts: []GameplayWrite{}}
		for _, request := range frame.Emit {
			actor := bodies[request.ActorGUID]
			if actor == nil || request.ID == "" || request.SpellID == 0 || request.TargetGUID == 0 {
				t.Fatalf("incomplete action in %s: %+v", frame.ID, request)
			}
			actor.castCount++
			ctx := modsession.WithWriteOrigin(context.Background(), modsession.WriteOrigin{
				SessionID: actor.id, Module: "buddy-actions", Writer: "lua", Generation: 1,
				PolicyRevision: "scripted-fixture", ActionID: request.ID,
			})
			payload := fixtureSpellBody(actor.castCount, request.SpellID, request.TargetGUID)
			if err := actor.session.SendPacket(ctx, opcode.CMSGCastSpell, payload); err != nil {
				t.Fatal(err)
			}
			snapshot := actor.trace.Snapshot()
			if len(snapshot.Attempts) != int(actor.castCount) || snapshot.Dropped != 0 {
				t.Fatalf("fixture transport missing write: %+v", snapshot)
			}
			result.TransportAttempts = append(result.TransportAttempts, snapshot.Attempts[len(snapshot.Attempts)-1])
		}
		capture.Frames = append(capture.Frames, result)
	}
	for _, actor := range bodies {
		if err := actor.session.Close(); err != nil {
			t.Fatal(err)
		}
		finished := actor.trace.Snapshot()
		capture.Sessions[actor.id] = sessionResult{AttemptCount: len(finished.Attempts), Dropped: finished.Dropped}
	}
	if output := os.Getenv("WB_LIFE37_CAPTURE"); output != "" {
		raw, err := json.MarshalIndent(capture, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func fixtureSpellBody(count byte, spell uint32, target uint64) []byte {
	body := make([]byte, 11)
	body[0] = count
	binary.LittleEndian.PutUint32(body[1:5], spell)
	binary.LittleEndian.PutUint32(body[6:10], 2)
	for i, value := range binary.LittleEndian.AppendUint64(nil, target) {
		if value != 0 {
			body[10] |= 1 << i
			body = append(body, value)
		}
	}
	return body
}
