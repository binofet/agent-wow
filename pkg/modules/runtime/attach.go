package modrt

import (
	"context"
	"encoding/json"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PolicySnapshot is one atomic read from the policy host for this body.
type PolicySnapshot struct {
	Revision         string   `json:"revision"`
	Generation       string   `json:"generation"`
	StateVersion     uint64   `json:"stateVersion,omitempty"`
	Cursor           string   `json:"cursor,omitempty"`
	Faulted          bool     `json:"faulted,omitempty"`
	PendingActionIDs []string `json:"pendingActionIds,omitempty"`
}

// PolicyObserver reads the policy state for the specified ready session. It
// must reject another body's identity and must not take control of the session.
type PolicyObserver func(ctx context.Context, sessionID, characterGUID string) (PolicySnapshot, error)

// BindPolicyObserver installs the same local policy host for the lifetime of
// this manager. Replacing a Lua revision does not replace its observer.
func (m *Manager) BindPolicyObserver(observer PolicyObserver) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if observer == nil || m.policyObserver != nil || m.started || m.closing {
		return status.Error(codes.FailedPrecondition, "policy observer must be installed once before startup")
	}
	m.policyObserver = observer
	return nil
}

type attachment struct {
	SessionCatalog
	Policy *PolicySnapshot `json:"policy"`
}

// attach inspects an already discovered body. It changes neither writer
// ownership nor the policy and uses the existing local gameplay RPC endpoint.
func (m *Manager) attach(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	fault := func(code int, err error) (json.RawMessage, error) {
		return nil, &CallError{Code: code, Module: "session", Method: "attach", Err: err}
	}
	var identity map[string]string
	if err := json.Unmarshal(params, &identity); err != nil || len(identity) != 2 || identity["sessionId"] == "" {
		return fault(-32602, status.Error(codes.InvalidArgument, "session.attach requires sessionId and characterGuid strings"))
	}
	guid, err := strconv.ParseUint(identity["characterGuid"], 10, 64)
	if err != nil || guid == 0 || strconv.FormatUint(guid, 10) != identity["characterGuid"] {
		return fault(-32602, status.Error(codes.InvalidArgument, "characterGuid must be a nonzero decimal string"))
	}
	catalog, observer, err := m.observedSession()
	if err != nil {
		return fault(-32000, err)
	}
	if catalog.SessionID != identity["sessionId"] || catalog.CharacterGUID != identity["characterGuid"] {
		return fault(-32000, status.Error(codes.FailedPrecondition, "session identity changed"))
	}
	result := attachment{SessionCatalog: catalog}
	if observer != nil {
		lookup, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		stop := context.AfterFunc(m.ctx, cancel)
		defer stop()
		policy, err := observer(lookup, catalog.SessionID, catalog.CharacterGUID)
		if err != nil {
			return fault(-32000, err)
		}
		current, _, err := m.observedSession()
		if err != nil {
			return fault(-32000, err)
		}
		if current.SessionID != catalog.SessionID || current.CharacterGUID != catalog.CharacterGUID {
			return fault(-32000, status.Error(codes.FailedPrecondition, "session identity changed"))
		}
		result.Policy = &policy
	}
	return json.Marshal(result)
}
