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

// Publish copies each packet into the ordered, dynamically sized subscriber
// queues. It must not wait for consumption: the world owner calling Publish
// also services the module callback writes needed to finish consumption.
// A burst is buffered, not a reason to terminate an otherwise healthy session.
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
		p := &modv1.WorldPacket{Opcode: uint32(op), Payload: bytesClone(body)}
		inst.queue = append(inst.queue, p)
		inst.queuedBytes += len(body)
		if m.pendingPackets == 0 {
			m.packetsIdle = make(chan struct{})
		}
		m.pendingPackets++
		// This signal may coalesce. Every packet lives in the FIFO above;
		// coalescing wakeups never discards or replays a packet.
		select {
		case inst.queueReady <- struct{}{}:
		default:
		}
	}
	return nil
}

func bytesClone(b []byte) []byte { return append([]byte(nil), b...) }

// nextPacket removes one FIFO entry under the same lock as Publish. Clear
// consumed references promptly, and compact only after consuming half the
// backing slice so an extended burst is drained in amortized linear work.
func (m *Manager) nextPacket(inst *instance) *modv1.WorldPacket {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil || inst.queueHead == len(inst.queue) {
		return nil
	}
	packet := inst.queue[inst.queueHead]
	inst.queue[inst.queueHead] = nil
	inst.queueHead++
	inst.queuedBytes -= len(packet.Payload)
	if inst.queueHead == len(inst.queue) {
		inst.queue, inst.queueHead = nil, 0
	} else if inst.queueHead >= len(inst.queue)-inst.queueHead {
		remaining := copy(inst.queue, inst.queue[inst.queueHead:])
		clear(inst.queue[remaining:])
		inst.queue, inst.queueHead = inst.queue[:remaining], 0
	}
	return packet
}

func (m *Manager) packetWorker(name string, inst *instance) {
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-inst.queueReady:
		}
		for packet := m.nextPacket(inst); packet != nil; packet = m.nextPacket(inst) {
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
	case <-m.ctx.Done():
		if err := m.Err(); err != nil {
			return err
		}
		return m.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
