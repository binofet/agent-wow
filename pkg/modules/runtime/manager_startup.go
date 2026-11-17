package modrt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/modules/runner"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Start launches modules in dependency order and waits for health. ctx governs startup only.
func (m *Manager) Start(ctx context.Context, session modsession.Session) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return errors.New("module runtime already started")
	}
	m.started = true
	m.session = session
	m.mu.Unlock()
	if session.SendPacket == nil || session.Clock == nil {
		return errors.New("session callbacks are required")
	}
	if len(m.order) == 0 {
		return nil
	}
	dir, err := os.MkdirTemp("", "aw-")
	if err != nil {
		return err
	}
	m.dir = dir
	// Runtime failure cancels startup without inheriting its deadline after success.
	startup, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	for index, name := range m.order {
		if err := startup.Err(); err != nil {
			if m.Err() != nil {
				return m.Err()
			}
			return err
		}
		d := m.instances[name].definition
		socketDir := filepath.Join(dir, strconv.Itoa(index))
		if err := os.Mkdir(socketDir, 0700); err != nil {
			return err
		}
		if err := os.Chmod(socketDir, 0777); err != nil {
			return err
		}
		l := modrunner.Launch{Manifest: d.Manifest(), Project: fmt.Sprintf("%s-%d", filepath.Base(dir), index), SocketDir: socketDir, Override: filepath.Join(dir, fmt.Sprintf("%d.json", index)), Environment: map[string]string{
			"AGENT_WOW_MODULE_SOCKET":  "/run/agent-wow/module.sock",
			"AGENT_WOW_SESSION_SOCKET": "/run/agent-wow/session.sock",
			"AGENT_WOW_MODULE_NAME":    name, "AGENT_WOW_SESSION_ID": filepath.Base(dir),
			"AGENT_WOW_CHARACTER_GUID": strconv.FormatUint(session.Identity.CharacterGUID, 10), "AGENT_WOW_CHARACTER_NAME": session.Identity.CharacterName,
			"AGENT_WOW_REALM_ID": strconv.Itoa(int(session.Identity.RealmID)), "AGENT_WOW_REALM_NAME": session.Identity.RealmName, "AGENT_WOW_CLIENT_BUILD": "12340",
		}}
		listener, err := net.Listen("unix", filepath.Join(socketDir, "session.sock"))
		if err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(socketDir, "session.sock"), 0666); err != nil {
			listener.Close()
			return err
		}
		inst := m.instances[name]
		inst.launch = l
		inst.server = grpc.NewServer()
		modv1.RegisterSessionServer(inst.server, modcb.New(m.ctx, name, modcb.Handlers{
			SendPacket:   m.modulePacketWriter(filepath.Base(dir), name),
			Clock:        m.session.Clock,
			InvokeModule: m.invokeModule,
		}))
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			if err := inst.server.Serve(listener); err != nil {
				m.fail(name, fmt.Errorf("callback server: %w", err))
			}
		}()
		inst.attempted = true // even a failed up may have created containers
		if err := m.runner.Up(startup, l); err != nil {
			return fmt.Errorf("module %s startup: %w", name, err)
		}
		conn, err := grpc.NewClient("unix://"+filepath.Join(socketDir, "module.sock"), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxRetryRPCBufferSize(0)))
		if err != nil {
			return err
		}
		m.mu.Lock()
		inst.conn = conn
		m.mu.Unlock()
		if err := m.waitHealthy(startup, inst); err != nil {
			return fmt.Errorf("module %s readiness: %w", name, err)
		}
		m.mu.Lock()
		inst.healthy = true
		m.mu.Unlock()
		m.wg.Add(3)
		go func() { defer m.wg.Done(); m.packetWorker(name, inst) }()
		go func() { defer m.wg.Done(); m.watchHealth(name, inst) }()
		go func() { defer m.wg.Done(); m.pollHealth(name, inst) }()
	}
	return m.Err()
}

func (m *Manager) modulePacketWriter(sessionID, module string) func(context.Context, uint32, []byte) error {
	return func(ctx context.Context, opcode uint32, payload []byte) error {
		// The callback server supplies module identity. A callback without a
		// matched policy dispatch cannot claim a Lua generation or action ID.
		origin := modsession.WriteOrigin{SessionID: sessionID, Module: module, Writer: "module"}
		return m.session.SendPacket(modsession.WithWriteOrigin(ctx, origin), opcode, payload)
	}
}
