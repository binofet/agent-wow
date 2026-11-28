package world

import (
	"bytes"
	"context"
	"strings"
	"testing"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestGroundCheckpointRefusalPreservesOnlyTheCurrentWrittenBody(t *testing.T) {
	for _, event := range []string{"bystander update", "new own position", "new visit", "another writer", "partial write"} {
		t.Run(event, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			c.observe(opcode.SMSGLoginVerifyWorld, positionBody(0, 5, 6, 7, .5))
			c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(99, 5, 6, 7, 0))
			wire := func(x float32, flags uint32) []byte {
				body, err := movementinfo.MoverFrame(99, movementinfo.Info{Flags: flags, Time: uint32(x * 100), X: x, Y: 6, Z: 7, Orientation: .5})
				if err != nil {
					t.Fatal(err)
				}
				return body
			}
			previousID, otherID := strings.Repeat("a", 64), strings.Repeat("b", 64)
			first := modsession.WithWriteOrigin(context.Background(), modsession.WriteOrigin{Module: "buddy-actions", Writer: "module", SessionID: "ground-test", MovementAttempt: previousID})
			if _, _, err := c.e.writeContext(first, opcode.MSGMoveStartForward, wire(5, 1), c.e.transport.snapshot()); err != nil {
				t.Fatal(err)
			}
			body := wire(6, 1)
			if _, _, err := c.e.writeContext(first, opcode.MSGMoveHeartbeat, body, c.e.transport.snapshot()); err != nil {
				t.Fatal(err)
			}
			reader := modsession.WithSelfBodyInput(context.Background())
			original, reason := c.e.s.bodyInputReport(reader, previousID)
			if reason != "" || original == nil || !bytes.Equal(original.Payload, body) {
				t.Fatal("missing physical heartbeat", reason, original)
			}
			requested := c.e.transport.snapshot()
			c.e.beforeScopedWrite = func() error {
				c.e.beforeScopedWrite = nil
				switch event {
				case "bystander update":
					result, _ := c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, bodyValuesBlock(42, 0x18, 100)))
					if result != modcb.BodyUpdateHarmless {
						t.Fatal("bystander changed the observed body", result)
					}
				case "new own position":
					c.observe(opcode.MSGMoveHeartbeat, wire(15, 0))
				case "new visit":
					c.observe(opcode.SMSGNewWorld, positionBody(0, 5, 6, 7, .5))
				case "another writer":
					other := modsession.WithWriteOrigin(context.Background(), modsession.WriteOrigin{Module: "buddy-actions", Writer: "module", SessionID: "ground-test", MovementAttempt: otherID})
					_, _, err := c.e.writeContext(other, opcode.MSGMoveHeartbeat, body)
					return err
				case "partial write":
					conn.fail = true
				}
				return nil
			}
			_, _, err := c.e.writeContext(context.Background(), opcode.MSGMoveHeartbeat, wire(7, 1), requested)
			if event == "partial write" {
				if err == nil || modcb.PacketWasNotDispatched(err) || conn.writes != 3 {
					t.Fatal("expected one uncertain physical write", err, conn.writes)
				}
			} else if !modcb.PacketWasNotDispatched(err) {
				t.Fatal("new checkpoint reached transport with a stale reservation", err)
			}
			result, reason := c.e.s.bodyInputReport(reader, previousID)
			if event != "bystander update" {
				if result != nil || reason == "" {
					t.Fatal("superseded or uncertain frame regained custody", result)
				}
				if event == "another writer" {
					actual, reason := c.e.s.bodyInputReport(reader, otherID)
					if reason != "" || !bytes.Equal(original.Payload, actual.GetPayload()) || actual.GetTransportScope().GetMotionRevision() <= original.TransportScope.MotionRevision {
						t.Fatal("refusal lost the genuinely later writer", actual, reason)
					}
				}
				return
			}
			if reason != "" || !proto.Equal(result, original) || conn.writes != 2 {
				t.Fatal("unwritten checkpoint erased or relabeled the last physical heartbeat", reason, result, conn.writes)
			}
			if _, _, err := c.e.writeContext(context.Background(), opcode.MSGMoveStop, wire(6, 0), c.e.transport.snapshot()); err != nil || conn.writes != 3 {
				t.Fatal("fresh cleanup at the preceding verified pose failed", err, conn.writes)
			}
		})
	}
}
