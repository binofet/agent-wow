package modrt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/modules/runner"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
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
	dir, err := os.MkdirTemp("", "aw-")
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.dir = dir
	m.mu.Unlock()
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
		if name == actionsModule {
			l.Environment[modcb.FollowPolicyCallbackEnv] = "true"
			l.Environment[modcb.StarterSpellPolicyCallbackEnv] = "true"
			var key [32]byte
			if _, err := rand.Read(key[:]); err != nil {
				return err
			}
			l.Environment[modcb.PolicyEncounterKeyEnv] = hex.EncodeToString(key[:])
		}
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
			SendPacket:       m.modulePacketWriter(filepath.Base(dir), name),
			SendScopedPacket: m.moduleScopedPacketWriter(filepath.Base(dir), name),
			Clock:            m.session.Clock,
			InvokeModule:     m.invokeModule,
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

func (m *Manager) moduleScopedPacketWriter(sessionID, module string) func(context.Context, uint32, []byte, *modv1.TransportScope) error {
	return func(ctx context.Context, opcode uint32, payload []byte, scope *modv1.TransportScope) error {
		if scope == nil || m.session.SendScopedPacket == nil {
			return modcb.PacketNotDispatchedError("transport scope is unsupported by this host")
		}
		followsOwner, route, marked := modcb.IncomingRouteBodyInput(ctx)
		if marked && (!route || module != actionsModule || modcb.IncomingMovementAttempt(ctx) == "" || ctx.Err() != nil || !buddynav.RouteBodyFrame(opcode, payload, scope.SelfGuid)) {
			return modcb.PacketNotDispatchedError("invalid scoped route body frame")
		}
		if token, supplied := modcb.IncomingPolicySpell(ctx); supplied {
			if handled, err := m.writePolicyScopedMovement(ctx, token, sessionID, module, opcode, payload, scope); handled {
				return err
			}
			if handled, err := m.writePolicySummonScoped(ctx, token, sessionID, module, opcode, payload, scope); handled {
				return err
			}
			return m.writePolicyInstanceEntryScoped(ctx, token, sessionID, module, opcode, payload, scope)
		}
		origin := modsession.WriteOrigin{SessionID: sessionID, Module: module, Writer: "module"}
		if marked {
			m.mu.Lock()
			current := m.ctx.Err() == nil && !m.closing && !m.preparing && !m.prepared && m.err == nil &&
				filepath.Base(m.dir) == sessionID && scope.SelfGuid == m.session.Identity.CharacterGUID
			m.mu.Unlock()
			if !current || !routeScopeBound(scope) {
				return modcb.PacketNotDispatchedError("native route body or visit unavailable")
			}
			origin.MovementAttempt = modcb.IncomingMovementAttempt(ctx)
			ctx = modsession.WithRouteBodyInput(ctx, followsOwner)
			if p, ok := modcb.IncomingFollowPosition(ctx); ok {
				ctx = modsession.WithFollowPosition(ctx, p)
			}
		}
		return m.session.SendScopedPacket(modsession.WithWriteOrigin(ctx, origin), opcode, append([]byte(nil), payload...), proto.Clone(scope).(*modv1.TransportScope))
	}
}

func (m *Manager) modulePacketWriter(sessionID, module string) func(context.Context, uint32, []byte) error {
	return func(ctx context.Context, opcode uint32, payload []byte) error {
		if _, _, marked := modcb.IncomingRouteBodyInput(ctx); marked {
			return modcb.PacketNotDispatchedError("route body input requires its physical source scope")
		}
		if token, supplied := modcb.IncomingPolicySpell(ctx); supplied {
			return m.writePolicySpell(ctx, token, sessionID, module, opcode, payload)
		}
		// The callback server supplies module identity. A callback without a
		// matched policy dispatch cannot claim a Lua generation or action ID.
		origin := modsession.WriteOrigin{SessionID: sessionID, Module: module, Writer: "module"}
		if module == actionsModule {
			origin.MovementAttempt = modcb.IncomingMovementAttempt(ctx)
		}
		return m.session.SendPacket(modsession.WithWriteOrigin(ctx, origin), opcode, payload)
	}
}
