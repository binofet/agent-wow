package modrt

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type policyStateRequest struct {
	SessionID     string `json:"sessionId"`
	CharacterGUID string `json:"characterGuid"`
}

type policyStateResponse struct {
	SourceLimitBytes int                        `json:"sourceLimitBytes"`
	SessionID        string                     `json:"sessionId"`
	CharacterGUID    string                     `json:"characterGuid"`
	Revision         string                     `json:"revision"`
	Generation       string                     `json:"generation"`
	Cursor           string                     `json:"cursor"`
	BufferedEvents   int                        `json:"bufferedEvents"`
	PendingActionIDs []string                   `json:"pendingActionIds"`
	Swapping         bool                       `json:"swapping"`
	Fault            string                     `json:"fault"`
	Clock            behavior.ClockReading      `json:"clock"`
	Checkpoint       map[string]any             `json:"checkpoint"`
	LatestDecision   *behavior.DecisionSnapshot `json:"latestDecision"`
}

func (m *Manager) readPolicyState(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	fault := func(code int, err error) (json.RawMessage, error) {
		return nil, &CallError{Code: code, Module: "session", Method: "policy_state", Err: err}
	}
	var request policyStateRequest
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
	m.mu.Lock()
	host := m.policy
	m.mu.Unlock()
	if host == nil {
		return fault(-32601, status.Error(codes.NotFound, "no session policy is attached"))
	}
	view, err := host.vm.State(ctx)
	if err != nil {
		return fault(-32000, err)
	}
	current, _, err := m.observedSession()
	if err != nil {
		return fault(-32000, err)
	}
	m.mu.Lock()
	sameHost := m.policy == host
	m.mu.Unlock()
	if !sameHost || current.SessionID != request.SessionID || current.CharacterGUID != request.CharacterGUID ||
		view.Status.SessionID != request.SessionID || host.body != request.CharacterGUID {
		return fault(-32000, status.Error(codes.FailedPrecondition, "policy session identity changed"))
	}
	out := policyStateResponse{SessionID: request.SessionID, CharacterGUID: request.CharacterGUID,
		SourceLimitBytes: view.SourceLimitBytes,
		Revision:         view.Status.Revision, Generation: strconv.FormatUint(view.Status.Epoch, 10),
		Cursor: strconv.FormatUint(view.Status.Cursor, 10), BufferedEvents: view.Status.Buffered,
		PendingActionIDs: make([]string, 0, len(view.Pending)), Swapping: view.Status.Swapping,
		Fault: view.Status.Fault, Clock: view.Clock, Checkpoint: view.Checkpoint, LatestDecision: view.LatestDecision}
	for _, receipt := range view.Pending {
		out.PendingActionIDs = append(out.PendingActionIDs, strconv.FormatUint(receipt.ActionID, 10))
	}
	return json.Marshal(out)
}
