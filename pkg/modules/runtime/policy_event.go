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

type policyEventRequest struct {
	SessionID     string         `json:"sessionId"`
	CharacterGUID string         `json:"characterGuid"`
	Kind          string         `json:"kind"`
	Value         map[string]any `json:"value"`
}

// sendPolicyEvent is an identity-bound input to the already attached script.
// The harness assigns ordering; only Lua decides what the message means.
func (m *Manager) sendPolicyEvent(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	fault := func(code int, err error) (json.RawMessage, error) {
		return nil, &CallError{Code: code, Module: "session", Method: "send_event", Err: err}
	}
	var r policyEventRequest
	d := json.NewDecoder(bytes.NewReader(params))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || r.SessionID == "" || r.CharacterGUID == "" || !policyPhase.MatchString(r.Kind) {
		return fault(-32602, status.Error(codes.InvalidArgument, "current session and character identity, event kind and object value required"))
	}
	c, _, err := m.observedSession()
	if err != nil {
		return fault(-32000, err)
	}
	if r.SessionID != c.SessionID || r.CharacterGUID != c.CharacterGUID {
		return fault(-32000, status.Error(codes.FailedPrecondition, "session identity changed"))
	}
	m.mu.Lock()
	host := m.policy
	m.mu.Unlock()
	if host == nil {
		return fault(-32601, status.Error(codes.NotFound, "no session policy is attached"))
	}
	if err := ctx.Err(); err != nil {
		return fault(-32000, status.FromContextError(err).Err())
	}
	seq, err := host.Enqueue(behavior.Event{Kind: r.Kind, Value: r.Value})
	if err != nil {
		return fault(-32000, err)
	}
	return json.Marshal(map[string]string{"sessionId": r.SessionID, "characterGuid": r.CharacterGUID, "sequence": strconv.FormatUint(seq, 10), "status": "queued"})
}
