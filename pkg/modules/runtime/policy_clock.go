package modrt

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type policyClockRequest struct {
	SessionID     string `json:"sessionId"`
	CharacterGUID string `json:"characterGuid"`
}

type policyClockResponse struct {
	SessionID     string `json:"sessionId"`
	CharacterGUID string `json:"characterGuid"`
	MonotonicMs   int64  `json:"monotonicMs"`
	UnixMs        int64  `json:"unixMs"`
}

func (m *Manager) readPolicyClock(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	fault := func(code int, err error) (json.RawMessage, error) {
		return nil, &CallError{Code: code, Module: "session", Method: "clock", Err: err}
	}
	var request policyClockRequest
	d := json.NewDecoder(bytes.NewReader(params))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || d.Decode(new(any)) != io.EOF || request.SessionID == "" || request.CharacterGUID == "" {
		return fault(-32602, status.Error(codes.InvalidArgument, "current session and character identity required"))
	}
	catalog, _, err := m.observedSession()
	if err != nil {
		return fault(-32000, err)
	}
	if request.SessionID != catalog.SessionID || request.CharacterGUID != catalog.CharacterGUID {
		return fault(-32000, status.Error(codes.FailedPrecondition, "session identity changed"))
	}
	if err := ctx.Err(); err != nil {
		return fault(-32000, status.FromContextError(err).Err())
	}
	m.mu.Lock()
	host := m.policy
	m.mu.Unlock()
	if host == nil {
		return fault(-32601, status.Error(codes.NotFound, "no session policy is attached"))
	}
	reading, err := host.vm.Clock()
	if err != nil {
		return fault(-32000, status.Error(codes.FailedPrecondition, err.Error()))
	}
	return json.Marshal(policyClockResponse{SessionID: catalog.SessionID, CharacterGUID: catalog.CharacterGUID,
		MonotonicMs: reading.MonotonicMs, UnixMs: reading.UnixMs})
}
