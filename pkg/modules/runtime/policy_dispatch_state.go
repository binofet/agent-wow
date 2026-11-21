package modrt

import (
	"encoding/json"
	"strconv"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

// This is the last completed dispatch callback, not the last proposal or later
// game-effect receipt. Its provenance can precede the current Lua checkpoint.
type policyDispatchSnapshot struct {
	SessionID           string                 `json:"sessionId"`
	CharacterGUID       string                 `json:"characterGuid"`
	Revision            string                 `json:"revision"`
	Generation          string                 `json:"generation"`
	Cursor              string                 `json:"cursor"`
	ActionID            string                 `json:"actionId"`
	Module              string                 `json:"module"`
	TransportOutcome    behavior.ActionOutcome `json:"transportOutcome"`
	SameRevisionAsState bool                   `json:"sameRevisionAsState"`
	Payload             map[string]any         `json:"payload"`
	PayloadError        string                 `json:"payloadError,omitempty"`
}

// Publication owns detached immutable bytes. State readers never acquire
// advance, enter Lua, drain events, or share a writable adapter payload.
type policyDispatchRecord struct {
	snapshot policyDispatchSnapshot
	payload  json.RawMessage
}

func (p *SessionPolicy) publishDispatch(admitted behavior.SessionStatus, id uint64, module string, outcome behavior.ActionOutcome, payload map[string]any) {
	record := &policyDispatchRecord{snapshot: policyDispatchSnapshot{
		SessionID: admitted.SessionID, CharacterGUID: p.body, Revision: admitted.Revision,
		Generation: strconv.FormatUint(admitted.Epoch, 10), Cursor: strconv.FormatUint(admitted.Cursor, 10),
		ActionID: strconv.FormatUint(id, 10), Module: module, TransportOutcome: outcome,
	}}
	var err error
	record.payload, err = json.Marshal(payload)
	if err != nil {
		record.snapshot.PayloadError = err.Error()
	}
	p.lastDispatch.Store(record)
}

func (p *SessionPolicy) dispatchSnapshot(state behavior.SessionStatus) *policyDispatchSnapshot {
	record := p.lastDispatch.Load()
	if record == nil {
		return nil
	}
	out := record.snapshot
	out.SameRevisionAsState = out.SessionID == state.SessionID && out.CharacterGUID == p.body &&
		out.Revision == state.Revision && out.Generation == strconv.FormatUint(state.Epoch, 10)
	if len(record.payload) != 0 {
		if err := json.Unmarshal(record.payload, &out.Payload); err != nil {
			out.PayloadError = err.Error()
		}
	}
	return &out
}
