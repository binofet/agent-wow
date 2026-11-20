package modrt

import (
	"context"
	"errors"
	"os"
	"time"
)

func (m *Manager) Stop() {
	m.stopOnce.Do(func() { m.mu.Lock(); m.closing = true; m.mu.Unlock(); m.cancel() })
}

func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.Stop()
		for _, inst := range m.instances {
			if inst.server != nil {
				inst.server.Stop()
			}
			if inst.conn != nil {
				_ = inst.conn.Close()
			}
		}
		m.wg.Wait()
		// Workers have stopped. Release packets canceled by explicit shutdown
		// even if a caller retains the closed Manager for diagnostics.
		m.mu.Lock()
		for _, inst := range m.instances {
			inst.queue, inst.queueHead, inst.queuedBytes = nil, 0, 0
		}
		m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := len(m.order) - 1; i >= 0; i-- {
			inst := m.instances[m.order[i]]
			if inst != nil && inst.attempted {
				m.closeErr = errors.Join(m.closeErr, m.runner.Down(ctx, inst.launch))
			}
		}
		if m.dir != "" {
			m.closeErr = errors.Join(m.closeErr, os.RemoveAll(m.dir))
		}
	})
	return m.closeErr
}
