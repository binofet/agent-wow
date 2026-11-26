package world

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"slices"
	"testing"
	"time"

	actions "github.com/agent-wow/agent-wow/modules/buddy-actions"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func impulseOrder(guid byte, counter uint32, cos, sin, xy, z float32) []byte {
	b := binary.LittleEndian.AppendUint32([]byte{1, guid}, counter)
	for _, v := range []float32{cos, sin, xy, z} {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
	}
	return b
}

type knockbackTestClient struct {
	*bodyContinuitySession
	landing bool
}

func (c knockbackTestClient) GetClock(ctx context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*modv1.ClockResponse, error) {
	return &modv1.ClockResponse{ClientTimeMs: c.e.s.Clock()}, ctx.Err()
}

func (c knockbackTestClient) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module != "session" || r.Method != modcb.KnockbackReportMethod {
		return c.bodyContinuitySession.InvokeModule(ctx, r, opts...)
	}
	var order modv1.WorldPacket
	if err := r.Request.UnmarshalTo(&order); err != nil {
		return nil, err
	}
	out := c.e.s.readKnockback(&order, c.landing)
	if out == nil {
		return nil, errors.New("knockback report unavailable")
	}
	packed, err := anypb.New(out)
	return &modv1.InvokeModuleResponse{Result: packed}, err
}

func knockbackToWorld(t *testing.T, c *bodyContinuitySession, op uint16, wire []byte) *modv1.WorldPacket {
	t.Helper()
	if _, err := c.e.handle(op, wire); err != nil {
		t.Fatal(err)
	}
	p := &modv1.WorldPacket{Opcode: uint32(op), Payload: wire, TransportScope: c.e.transport.snapshot()}
	st := &bodyUpdateTrailer{}
	if _, err := c.w.OnPacket(grpc.NewContextWithServerTransportStream(context.Background(), st), p); err != nil {
		t.Fatal(err)
	}
	if data := st.md.Get(modcb.KnockbackPoseTrailer); len(data) == 1 {
		c.e.s.recordOwnPose(p.TransportScope, []byte(data[0]))
	}
	if op == opcode.SMSGUpdateObject || op == opcode.SMSGCompressedUpdateObject {
		class := modcb.BodyUpdateResult(st.md.Get(modcb.BodyInputUpdateTrailer), st.md.Get(modcb.BodyInputBarrierTrailer))
		c.e.s.recordParsedBodyUpdate(p.TransportScope, class)
	}
	return p
}

func TestCopiedOwnKnockbackRegainsPhysicalContactWithoutRealmSelfEcho(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR to copied client geometry")
	}
	for _, mode := range []string{"upper_floor", "pool", "far_bank"} {
		wet := mode == "pool"
		c, conn := newBodyContinuitySession(t)
		c.e.s.started = time.Now()
		if wet {
			c.e.s.started = c.e.s.started.Add(-time.Duration(math.MaxUint32-5000) * time.Millisecond)
		}
		a := actions.NewService(knockbackTestClient{c, false}, 99)
		a.SetNavigationDataDir(dir)
		c.w.ConfigureSession(knockbackTestClient{c, true})
		x, y, z := float32(475.6561), float32(50.766533), float32(-96.19031)
		if mode == "far_bank" {
			// Actual mutually witnessed pose 91 seconds before the incident.
			// The subsequent order, origin continuity and force are simulated.
			x, y, z = 384.65375, 26.86131, -130.93765
		}
		knockbackToWorld(t, c, opcode.SMSGLoginVerifyWorld, positionBody(349, 502, 157, -94, 0))
		knockbackToWorld(t, c, opcode.SMSGUpdateObject, bodyPlayerCreate(99, 502, 157, -94, 0))
		wire, _ := movementinfo.MoverFrame(99, movementinfo.Info{X: x, Y: y, Z: z})
		if _, _, err := c.e.writeContext(context.Background(), opcode.MSGMoveStop, wire); err != nil {
			t.Fatal(err)
		}
		// Prior ordinary selection generated no wire, retiring its action
		// receipt without erasing the actual previous physical position.
		stale := c.e.transport.snapshot()
		stale.MotionRevision--
		if _, _, err := c.e.writeContext(context.Background(), opcode.MSGMoveSetFacing, wire, stale); !modcb.PacketWasNotDispatched(err) {
			t.Fatal(err)
		}
		before, _ := c.w.Snapshot(context.Background(), &worldv1.SnapshotRequest{})
		horizontal := float32(0)
		if wet {
			horizontal = 9
		}
		bearing := math.Atan2(40.353012-float64(y), 446.6661-float64(x))
		order := impulseOrder(99, 0, float32(math.Cos(bearing)), float32(math.Sin(bearing)), horizontal, -4)
		p := knockbackToWorld(t, c, opcode.SMSGMoveKnockBack, order)
		motion := c.e.impulse
		if motion == nil || !slices.Equal(conn.opcodes, []uint32{opcode.MSGMoveStop, opcode.CMSGMoveKnockBackAck}) || motion.launch.X != x || motion.launch.Z != z {
			t.Fatalf("actual 22-byte order failed to ACK from the latest body %#v %v", motion, conn.opcodes)
		}
		ack := c.e.s.readKnockback(p, false)
		k, _ := movementinfo.DecodeKnockback(order)
		i, ok := k.ReadAcknowledgement(ack.Payload)
		if !ok || i.Jump.Velocity != -4 || i.Jump.XYSpeed != horizontal {
			t.Fatal("wrong impulse ACK", i)
		}
		pending, _ := c.w.Snapshot(context.Background(), &worldv1.SnapshotRequest{})
		if !pending.Self.Present || !pending.Self.Alive || pending.Self.PositionKnown || pending.Self.MovementFlagsKnown {
			t.Fatal("unconfirmed flight supplied a settled body", pending.Self)
		}
		st := &bodyUpdateTrailer{}
		_, err := a.OnPacket(grpc.NewContextWithServerTransportStream(context.Background(), st), p)
		data := st.md.Get(modcb.KnockbackContactTrailer)
		if err != nil || len(data) != 1 {
			t.Fatal("Actions did not measure the copied geometry", wet, err, st.md)
		}
		c.e.s.acceptKnockbackContact(p, []byte(data[0]))
		c.e.startKnockbackPlan()
		if motion.contact == nil || motion.contact.PitchPresent != wet {
			t.Fatal("invalid measured first contact", wet, motion.contact)
		}
		knockbackToWorld(t, c, opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, bodyValuesBlock(23, 0x19, 42)))
		// A shared area impulse generates acknowledged broadcasts for other
		// players while this client is still executing its own order.
		peer, _ := movementinfo.MoverFrame(23, motion.launch)
		peer = append(peer, make([]byte, 16)...)
		knockbackToWorld(t, c, opcode.MSGMoveKnockBack, peer)
		motion.start = time.Now().Add(-120 * time.Millisecond)
		if err := c.e.advanceKnockback(); err != nil {
			t.Fatal(err)
		}
		motion.start = time.Now().Add(-time.Duration(motion.contact.Time+5) * time.Millisecond)
		if err := c.e.advanceKnockback(); err != nil {
			t.Fatal(err)
		}
		// Delay World's first read past physical contact. Cross uint32 clock
		// wrap for the wet fixture while retaining the original landing age.
		landed, ok := movementinfo.OwnFrame(c.e.ownMovementReport.GetPayload(), 99)
		if !ok {
			t.Fatal("no valid physical terminal", conn.opcodes)
		}
		c.e.s.started = time.Now().Add(-time.Duration(landed.Time+7300) * time.Millisecond)
		after, _ := c.w.Snapshot(context.Background(), &worldv1.SnapshotRequest{})
		if !after.Self.PositionKnown || !after.Self.PositionInferred || after.Self.PositionAgeMs < 7300 || after.Self.PositionAgeMs > 8000 || after.Self.PositionRevision <= before.Self.PositionRevision || !after.Self.MovementFlagsKnown ||
			!after.Self.Alive || after.Self.Z != motion.contact.Z || !proto.Equal(before.SelfMovementFacts.Info, after.SelfMovementFacts.Info) || before.SelfMovementFacts.Revision != after.SelfMovementFacts.Revision {
			t.Fatalf("lost real self, fake realm facts or stale login wet=%v self=%v writes=%v", wet, after.Self, conn.opcodes)
		}
		want := uint32(opcode.MSGMoveFallLand)
		if wet {
			want = opcode.MSGMoveStartSwim
		}
		state, err := a.Status(context.Background(), &emptypb.Empty{})
		if err != nil || !state.GetPosition().GetKnown() || !state.Position.Estimated || state.Position.X != motion.contact.X || state.Position.Z != motion.contact.Z {
			t.Fatal("ordinary Actions still lacks a current client position", mode, state.GetPosition(), err)
		}
		if !slices.Equal(conn.opcodes, []uint32{opcode.MSGMoveStop, opcode.CMSGMoveKnockBackAck, opcode.MSGMoveHeartbeat, want}) || !bytes.Equal(c.e.ownMovementReport.Payload, c.e.s.readKnockback(p, true).Payload) {
			t.Fatal("missing exact physical contact", conn.opcodes)
		}
	}
}

func TestKnockbackRequiresLocatedMovingOrigin(t *testing.T) {
	for _, source := range []string{"realm", "known_write"} {
		for _, flags := range []uint32{1, 0x10, 0x1000, movementinfo.SwimmingFlag | 1} {
			c, conn := newBodyContinuitySession(t)
			c.e.s.started = time.Now().Add(-time.Second)
			knockbackToWorld(t, c, opcode.SMSGLoginVerifyWorld, positionBody(349, 5, 15, 200, 0))
			knockbackToWorld(t, c, opcode.SMSGUpdateObject, bodyPlayerCreate(99, 5, 15, 200, 0))
			i := movementinfo.Info{Flags: flags, Time: c.e.timestamp() - 220, X: 5, Y: 15, Z: 180, JumpPresent: flags == 0x1000, PitchPresent: flags&movementinfo.SwimmingFlag != 0}
			if i.JumpPresent {
				i.FallTime = 800
				i.Jump = movementinfo.Jump{CosAngle: 1, XYSpeed: 12}
			}
			wire, _ := movementinfo.MoverFrame(99, i)
			if source == "realm" {
				knockbackToWorld(t, c, opcode.MSGMoveHeartbeat, wire)
			} else {
				// A raw unscoped movement can be known to the transport even
				// when it is outside the narrower normal route-report contract.
				if _, _, err := c.e.writeContext(context.Background(), opcode.MSGMoveHeartbeat, wire); err != nil {
					t.Fatal(err)
				}
			}
			before := conn.writes
			knockbackToWorld(t, c, opcode.SMSGMoveKnockBack, impulseOrder(99, 4, 1, 0, 10, -8))
			if c.e.impulse != nil || conn.writes != before {
				t.Fatalf("used stale %s 0x%x pose for a new impulse", source, flags)
			}
		}
	}
}

func TestSocketOwnerClocksKnockbackAndResumesOrdinaryInput(t *testing.T) {
	c, conn := newBodyContinuitySession(t)
	e := c.e
	knockbackToWorld(t, c, opcode.SMSGLoginVerifyWorld, positionBody(349, 0, 0, 3, 0))
	knockbackToWorld(t, c, opcode.SMSGUpdateObject, bodyPlayerCreate(99, 5, 15, 3, 1))
	rt := &scopeObservedRuntime{observedRuntime: newObserved(), facts: make(chan *modv1.WorldPacket, 8)}
	e.s.modules, e.s.ctx, e.s.started = rt, context.Background(), time.Now()
	e.s.abort, e.s.startLogin, e.s.writes = make(chan struct{}), make(chan struct{}), make(chan writeRequest)
	e.s.knockbackReady = make(chan struct{}, 1)
	in := make(chan packet, 8)
	done := make(chan error, 1)
	go func() { done <- e.loop(in) }()
	t.Cleanup(func() { close(e.s.abort); <-done; e.pongTimer.Stop() })
	order := impulseOrder(99, 0, 1, 0, 10, -4)
	in <- packet{opcode: opcode.SMSGMoveKnockBack, body: order}
	var p *modv1.WorldPacket
	select {
	case p = <-rt.facts:
	case <-time.After(time.Second):
		t.Fatal("no observed own order")
	}
	k, _ := movementinfo.DecodeKnockback(order)
	a := e.s.readKnockback(p, false)
	i, ok := k.ReadAcknowledgement(a.GetPayload())
	if !ok {
		t.Fatal("normal owner loop did not acknowledge")
	}
	const duration = 250 * time.Millisecond
	point := (&knockbackMotion{order: k, launch: i}).point(duration)
	contact, _ := movementinfo.MoverFrame(99, movementinfo.Info{Time: uint32(duration.Milliseconds()), X: point.X, Y: point.Y, Z: point.Z, Orientation: i.Orientation})
	e.s.acceptKnockbackContact(p, contact)
	deadline := time.Now().Add(2 * time.Second)
	var landed *modv1.WorldPacket
	for time.Now().Before(deadline) {
		landed = e.s.readKnockback(p, true)
		if landed != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	frame, ok := movementinfo.OwnFrame(landed.GetPayload(), 99)
	if !ok || landed.Opcode != opcode.MSGMoveFallLand || frame.Time != i.Time+250 || time.Since(e.s.started) < duration {
		t.Fatal("socket owner did not wait for real flight", landed)
	}
	req := writeRequest{ctx: context.Background(), op: opcode.MSGMoveStartForward, body: ownReportPacket(t, 1), reply: make(chan error, 1)}
	e.s.writes <- req
	if err := <-req.reply; err != nil {
		t.Fatal("normal input after contact", err)
	}
	if e.s.readKnockback(p, true) != nil || len(conn.opcodes) < 4 || !slices.Contains(conn.opcodes, opcode.MSGMoveHeartbeat) {
		t.Fatal("ambiguous stale contact or missing physical sequence", conn.opcodes)
	}
}

func TestOwnKnockbackSupersessionAndUnknown(t *testing.T) {
	for _, mode := range []string{"repeat_zero", "partial_ack", "partial_landing", "transfer", "control", "malformed", "foreign", "missing_pose", "stale_login", "competing_stop", "mismatched_plan"} {
		t.Run(mode, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			c.e.s.started = time.Now()
			knockbackToWorld(t, c, opcode.SMSGLoginVerifyWorld, positionBody(349, 55, 15, 3, 1))
			if mode != "missing_pose" {
				knockbackToWorld(t, c, opcode.SMSGUpdateObject, bodyPlayerCreate(99, 5, 15, 3, 1))
			}
			b := impulseOrder(99, 0, 1, 0, 10, -8)
			if mode == "foreign" {
				b[1]++
			}
			if mode == "malformed" {
				b = append(b, 0)
			}
			if mode == "stale_login" {
				knockbackToWorld(t, c, opcode.SMSGClientControlUpdate, []byte{1, 99, 0})
			}
			if mode == "partial_ack" {
				conn.fail = true
				if _, err := c.e.handle(opcode.SMSGMoveKnockBack, b); err == nil || c.e.impulse != nil || c.e.s.knockbackReports.ack != nil {
					t.Fatal("partial ACK became known", err)
				}
				return
			}
			order := knockbackToWorld(t, c, opcode.SMSGMoveKnockBack, b)
			k := c.e.impulse
			if mode == "malformed" || mode == "foreign" || mode == "missing_pose" || mode == "stale_login" {
				if k != nil || conn.writes != 0 {
					t.Fatal("unavailable body/order invented an ACK", conn.opcodes)
				}
				return
			}
			if k == nil {
				t.Fatal("missing launch")
			}
			if mode == "competing_stop" {
				if _, _, e := c.e.writeContext(context.Background(), opcode.MSGMoveStop, ownReportPacket(t, 0)); !modcb.PacketWasNotDispatched(e) || conn.writes != 1 {
					t.Fatal("normal STOP overtook forced flight", e, conn.opcodes)
				}
				return
			}
			end := uint32(800)
			pt := k.point(time.Duration(end) * time.Millisecond)
			info := movementinfo.Info{Time: end, X: pt.X, Y: pt.Y, Z: pt.Z, Orientation: k.launch.Orientation}
			if mode == "mismatched_plan" {
				info.X += 2
			}
			contact, _ := movementinfo.MoverFrame(99, info)
			c.e.s.acceptKnockbackContact(order, contact)
			c.e.startKnockbackPlan()
			if mode == "mismatched_plan" {
				if k.contact != nil || conn.writes != 1 {
					t.Fatal("unrelated contact accepted")
				}
				return
			}
			if k.contact == nil {
				t.Fatal("missing plan")
			}
			if mode == "repeat_zero" {
				k.start = time.Now().Add(-200 * time.Millisecond)
				b2 := knockbackToWorld(t, c, opcode.SMSGMoveKnockBack, b)
				k2 := c.e.impulse
				if k2 == nil || k2 == k || k2.launch.X < 6.9 || k2.launch.X > 7.2 || c.e.s.readKnockback(order, false) != nil || c.e.s.readKnockback(b2, false) == nil || conn.writes != 2 {
					t.Fatal("legitimate repeated zero order lost its current flight/identity", k2, conn.opcodes)
				}
				c.e.s.acceptKnockbackContact(order, contact)
				c.e.startKnockbackPlan()
				if k2.contact != nil {
					t.Fatal("stale geometry replaced new order")
				}
				return
			}
			if mode == "transfer" {
				knockbackToWorld(t, c, opcode.SMSGNewWorld, positionBody(349, 55, 15, 3, 1))
			}
			if mode == "control" {
				knockbackToWorld(t, c, opcode.SMSGClientControlUpdate, []byte{1, 99, 0})
			}
			if mode == "partial_landing" {
				conn.fail = true
			}
			k.start = time.Now().Add(-time.Second)
			e := c.e.advanceKnockback()
			if (mode == "partial_landing") != (e != nil) || c.e.s.readKnockback(order, true) != nil {
				t.Fatal("unconfirmed contact was published", mode, e, conn.opcodes)
			}
		})
	}
}
