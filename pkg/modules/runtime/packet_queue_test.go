package modrt

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/internal/modulefixture"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modtest "github.com/agent-wow/agent-wow/pkg/modules/internal/testutil"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
)

func TestPacketQueueBuffersStartupBurstsAndDeliversEveryPacketInOrder(t *testing.T) {
	for _, test := range []struct {
		name        string
		count, size int
	}{
		{"more_than_fixed_packet_capacity", 10000, 4},
		{"more_than_fixed_byte_capacity", 24, 1 << 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			modtest.WriteModule(t, root, "worker", "")
			registry, err := moddisc.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var delivered []uint32
			r := &fixtureRunner{configure: func(_ string, s *modulefixture.Service) {
				s.Packet = func(_ context.Context, p *modv1.WorldPacket) error {
					mu.Lock()
					delivered = append(delivered, binary.LittleEndian.Uint32(p.Payload))
					mu.Unlock()
					return nil
				}
			}}
			m := New(registry, r, nil)
			t.Cleanup(func() {
				if err := m.Close(); err != nil {
					t.Error(err)
				}
			})
			payload := make([]byte, test.size)
			for i := range test.count {
				binary.LittleEndian.PutUint32(payload, uint32(i))
				if err := m.Publish(0x236, payload); err != nil {
					t.Fatalf("burst packet %d terminated session: %v", i, err)
				}
			}
			binary.LittleEndian.PutUint32(payload, ^uint32(0)) // Caller retains ownership of its input.
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := m.Start(ctx, modsession.Session{SendPacket: func(context.Context, uint32, []byte) error { return nil }, Clock: func() uint32 { return 1 }}); err != nil {
				t.Fatal(err)
			}
			if err := m.FlushPackets(ctx); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(delivered) != test.count {
				t.Fatalf("delivered %d of %d accepted packets", len(delivered), test.count)
			}
			for i, value := range delivered {
				if value != uint32(i) {
					t.Fatalf("packet %d delivered value%d", i, value)
				}
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			inst := m.instances["worker"]
			if m.pendingPackets != 0 || inst.queuedBytes != 0 || len(inst.queue) != 0 || inst.queueHead != 0 {
				t.Fatal("drained queue retained packet references or accounting")
			}
		})
	}
}

func TestPacketQueueContinuesAcceptingWhileConsumerNeedsOwnerCallback(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var receivedMu sync.Mutex
	var received []uint32
	r := &fixtureRunner{configure: func(_ string, s *modulefixture.Service) {
		s.PacketReplyOpcode = 123
		s.Packet = func(ctx context.Context, packet *modv1.WorldPacket) error {
			once.Do(func() { close(entered) })
			select {
			case <-release:
				receivedMu.Lock()
				received = append(received, binary.LittleEndian.Uint32(packet.Payload))
				receivedMu.Unlock()
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}}
	m := startGraph(t, r, map[string]string{"worker": ""}, func(context.Context, uint32, []byte) error { return nil })
	if err := m.Publish(0x236, make([]byte, 4)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("consumer never entered")
	}
	published := make(chan error, 1)
	go func() {
		for i := 1; i <= 4096; i++ {
			payload := binary.LittleEndian.AppendUint32(nil, uint32(i))
			if err := m.Publish(0x236, payload); err != nil {
				published <- err
				return
			}
		}
		published <- nil
	}()
	select {
	case err := <-published:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Publish blocked owner loop behind its own consumer")
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.FlushPackets(ctx); err != nil {
		t.Fatal(err)
	}
	receivedMu.Lock()
	defer receivedMu.Unlock()
	if len(received) != 4097 {
		t.Fatalf("accepted burst lost records: %d", len(received))
	}
	for i, value := range received {
		if value != uint32(i) {
			t.Fatalf("burst reordered packet %d to %d", i, value)
		}
	}
}

func TestPacketQueueStopUnblocksIdleWorkerAndFlush(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	r := &fixtureRunner{configure: func(_ string, s *modulefixture.Service) {
		s.Packet = func(ctx context.Context, _ *modv1.WorldPacket) error {
			once.Do(func() { close(entered) })
			<-ctx.Done()
			return ctx.Err()
		}
	}}
	m := startGraph(t, r, map[string]string{"worker": ""}, nil)
	if err := m.Publish(0x236, []byte{1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("consumer never entered")
	}
	for range 1024 {
		if err := m.Publish(0x236, []byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	flushed := make(chan error, 1)
	go func() { flushed <- m.FlushPackets(context.Background()) }()
	m.Stop()
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close left a packet worker blocked")
	}
	select {
	case err := <-flushed:
		if err == nil {
			t.Fatal("canceled packets were reported as delivered")
		}
	case <-time.After(time.Second):
		t.Fatal("Flush stayed blocked after Stop")
	}
	m.mu.Lock()
	inst := m.instances["worker"]
	retained := len(inst.queue) != 0 || inst.queueHead != 0 || inst.queuedBytes != 0
	m.mu.Unlock()
	if retained {
		t.Fatal("closed manager retained canceled packets")
	}
	if err := m.Publish(0x236, []byte{2}); err == nil {
		t.Fatal("stopped session accepted new packets")
	}
	// An empty FIFO's worker must also observe cancellation without a wake.
	idle := startGraph(t, &fixtureRunner{}, map[string]string{"worker": ""}, nil)
	if err := idle.Close(); err != nil {
		t.Fatal(err)
	}
}
