package modrt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type policyEditRequest struct {
	SessionID        string  `json:"sessionId"`
	CharacterGUID    string  `json:"characterGuid"`
	ExpectedRevision *string `json:"expectedRevision"`
	Source           *string `json:"source"`
	UseBuiltin       bool    `json:"useBuiltin"`
}

type policySourceResponse struct {
	PolicySnapshot
	Source           string                       `json:"source"`
	SessionID        string                       `json:"sessionId"`
	CharacterGUID    string                       `json:"characterGuid"`
	SourceLimitBytes int                          `json:"sourceLimitBytes"`
	FollowFallback   policyFollowFallbackMetadata `json:"followFallback"`
	StarterSpell     policyFollowFallbackMetadata `json:"starterSpell"`
	TradeConsent     policyFollowFallbackMetadata `json:"tradeConsent"`
}

// The local control plane edits the existing host using the same revision
// fence as native callers. It never starts a second policy or world session.
func (m *Manager) editPolicy(ctx context.Context, full string, params json.RawMessage) (json.RawMessage, error) {
	method := full[len("session."):]
	fault := func(code int, err error) (json.RawMessage, error) {
		return nil, &CallError{Code: code, Module: "session", Method: method, Err: err}
	}
	var request policyEditRequest

	d := json.NewDecoder(bytes.NewReader(params))
	d.DisallowUnknownFields()
	reload := method == "reload_policy"
	if err := d.Decode(&request); err != nil || request.SessionID == "" || request.CharacterGUID == "" ||
		reload != (request.Source != nil || request.UseBuiltin) || reload != (request.ExpectedRevision != nil) ||
		(request.Source != nil && request.UseBuiltin) {
		return fault(-32602, status.Error(codes.InvalidArgument, "explicit body identity and, for reload, source and expected revision are required"))
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
	if reload {
		source := host.builtin
		if request.Source != nil {
			source = *request.Source
		}
		revision, err := host.Reload(ctx, source, *request.ExpectedRevision)
		if err != nil {
			switch {
			case errors.Is(err, behavior.ErrRevisionConflict), errors.Is(err, behavior.ErrSessionClosed):
				return fault(-32000, status.Error(codes.FailedPrecondition, err.Error()))
			default:
				return fault(-32602, status.Error(codes.InvalidArgument, err.Error()))
			}
		}
		return json.Marshal(map[string]string{"sessionId": request.SessionID, "characterGuid": request.CharacterGUID, "revision": revision})
	}
	host.advance.Lock()
	defer host.advance.Unlock()
	view, err := host.observe(ctx, request.SessionID, request.CharacterGUID)
	if err != nil {
		return fault(-32000, err)
	}
	return json.Marshal(policySourceResponse{PolicySnapshot: view, Source: host.source, SessionID: request.SessionID,
		CharacterGUID: request.CharacterGUID, SourceLimitBytes: host.vm.SourceLimitBytes(), FollowFallback: host.followFallbackMetadata(),
		StarterSpell: host.starterSpellMetadata(), TradeConsent: host.tradeConsentMetadata()})
}
