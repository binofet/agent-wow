package modrt

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"

	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SessionCatalog binds immutable public contracts to this actual ready body.
// The embedded Revision deliberately excludes the body identity.
type SessionCatalog struct {
	SessionID     string `json:"sessionId"`
	CharacterGUID string `json:"characterGuid"`
	moddisc.Catalog
	HostMethods []HostMethod `json:"hostMethods"`
}

func (m *Manager) describe(params json.RawMessage) (json.RawMessage, error) {
	fault := func(code int, err error) (json.RawMessage, error) {
		return nil, &CallError{Code: code, Module: "session", Method: "describe", Err: err}
	}
	params = bytes.TrimSpace(params)
	if len(params) != 0 && !bytes.Equal(params, []byte("null")) {
		var fields map[string]json.RawMessage
		if json.Unmarshal(params, &fields) != nil || fields == nil || len(fields) != 0 {
			return fault(-32602, status.Error(codes.InvalidArgument, "session.describe takes an empty object"))
		}
	}
	catalog, _, err := m.observedSession()
	if err != nil {
		return fault(-32000, err)
	}
	return json.Marshal(catalog)
}

func (m *Manager) observedSession() (SessionCatalog, PolicyObserver, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.closing || m.preparing || m.prepared || m.err != nil || m.session.Identity.CharacterGUID == 0 {
		return SessionCatalog{}, nil, status.Error(codes.FailedPrecondition, "live owned session catalog unavailable")
	}
	for _, inst := range m.instances {
		if !m.invocationAllowed(inst, true) {
			return SessionCatalog{}, nil, status.Error(codes.FailedPrecondition, "module invocation unavailable in current session state")
		}
	}
	// The session identity is available only after the directory is published.
	if m.dir == "" {
		return SessionCatalog{}, nil, status.Error(codes.FailedPrecondition, "live owned session catalog unavailable")
	}
	return SessionCatalog{SessionID: filepath.Base(m.dir), CharacterGUID: strconv.FormatUint(m.session.Identity.CharacterGUID, 10), Catalog: m.registry.Catalog(), HostMethods: hostMethods()}, m.policyObserver, nil
}
