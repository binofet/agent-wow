package modrt

import (
	"context"
	"fmt"
	"time"

	"github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const packetTimeout = 5 * time.Second

const queueLimit = 64

const queueBytes = 8 << 20

// Publish queues a copy for each subscriber without waiting for handling. Delivery errors are fatal to the session.
func (m *Manager) Publish(op uint16, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return status.Error(codes.FailedPrecondition, "session is closed")
	}
	if m.err != nil {
		return m.err
	}
	for _, name := range m.order {
		inst := m.instances[name]
		if inst == nil {
			continue
		}
		if _, ok := inst.definition.Packet(op); !ok {
			continue
		}
		if inst.queuedBytes+len(body) > queueBytes {
			return fmt.Errorf("module %s: packet queue exceeds byte limit", name)
		}
		p := &modv1.WorldPacket{Opcode: uint32(op), Payload: bytesClone(body)}
		select {
		case inst.queue <- p:
			inst.queuedBytes += len(body)
			if m.pendingPackets == 0 {
				m.packetsIdle = make(chan struct{})
			}
			m.pendingPackets++
		default:
			return fmt.Errorf("module %s: packet queue is full", name)
		}
	}
	return nil
}

func bytesClone(b []byte) []byte { return append([]byte(nil), b...) }

func (m *Manager) packetWorker(name string, inst *instance) {
	for {
		select {
		case <-m.ctx.Done():
			return
		case packet := <-inst.queue:
			m.mu.Lock()
			inst.queuedBytes -= len(packet.Payload)
			m.mu.Unlock()
			ctx, cancel := context.WithTimeout(m.ctx, packetTimeout)
			md, _ := inst.definition.Packet(uint16(packet.Opcode))
			var trailers metadata.MD
			err := m.invoke(ctx, packetCall, "worldserver", name, inst, md, packet, &emptypb.Empty{}, grpc.Trailer(&trailers))
			cancel()
			if err != nil {
				m.fail(name, fmt.Errorf("packet 0x%x: %w", packet.Opcode, err))
				return
			}
			m.recordSpellEffect(name, trailers)
			m.mu.Lock()
			m.pendingPackets--
			if m.pendingPackets == 0 {
				close(m.packetsIdle)
			}
			m.mu.Unlock()
		}
	}
}

// FlushPackets waits for accepted packets to drain. The owner loop must keep servicing callback writes.
func (m *Manager) FlushPackets(ctx context.Context) error {
	m.mu.Lock()
	idle := m.packetsIdle
	m.mu.Unlock()
	select {
	case <-idle:
		return m.Err()
	case <-m.Done():
		return m.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
