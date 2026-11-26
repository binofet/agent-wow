package modrt

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/modules/runner"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
)

type instance struct {
	definition                   *moddisc.Definition
	launch                       modrunner.Launch
	conn                         *grpc.ClientConn
	server                       *grpc.Server
	queue                        []*modv1.WorldPacket
	queueHead                    int
	queueReady                   chan struct{}
	queuedBytes                  int
	healthy, prepared, attempted bool
}

// Manager implements Runtime for one session's fixed module registry. Construct it
// with New and call Close after Start returns, including when startup fails.
type Manager struct {
	registry                              *moddisc.Registry
	order                                 []string
	runner                                modrunner.Runner
	logger                                *slog.Logger
	mu                                    sync.Mutex
	instances                             map[string]*instance
	session                               modsession.Session
	dir                                   string
	started, closing, preparing, prepared bool
	prepareDeadline                       time.Time
	err                                   error
	ctx                                   context.Context
	cancel                                context.CancelFunc
	done                                  chan struct{}
	failOnce                              sync.Once
	stopOnce                              sync.Once
	closeOnce                             sync.Once
	closeErr                              error
	wg                                    sync.WaitGroup
	pendingPackets                        int
	worldPackets                          uint64
	packetsIdle                           chan struct{}
	healthInterval, timeHealth            time.Duration
	policyObserver                        PolicyObserver
	policy                                *SessionPolicy
	policySpells                          policySpellRegistry
}

// New constructs a manager. Nil arguments select no modules, the Compose runner, and discarded logs.
func New(registry *moddisc.Registry, runner modrunner.Runner, logger *slog.Logger) *Manager {
	if registry == nil {
		registry = &moddisc.Registry{}
	}
	if runner == nil {
		runner = modrunner.ComposeRunner{}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{registry: registry, order: registry.Order(), runner: runner, logger: logger, instances: map[string]*instance{}, ctx: ctx, cancel: cancel, done: make(chan struct{}), packetsIdle: make(chan struct{}), healthInterval: time.Second, timeHealth: 2 * time.Second}
	close(m.packetsIdle)
	// Queue core packets received during startup, before a module is healthy
	// enough to consume them. Registration is fixed for the whole session.
	for _, name := range m.order {
		d, _ := registry.Lookup(name)
		m.instances[name] = &instance{definition: d, queueReady: make(chan struct{}, 1)}
	}
	return m
}
