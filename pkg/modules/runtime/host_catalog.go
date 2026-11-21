package modrt

import (
	"reflect"
	"strings"
)

// HostMethod describes the typed core API beside the module protobuf catalog.
// Inputs derive from the same request structs decoded by the implementations.
type HostMethod struct {
	Name        string         `json:"name"`
	Effect      string         `json:"effect"`
	Description string         `json:"description"`
	Input       map[string]any `json:"input"`
	Output      map[string]any `json:"output,omitempty"`
}

func inputSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		ft := f.Type
		optional := ft.Kind() == reflect.Pointer
		if optional {
			ft = ft.Elem()
		}
		kind := "object"
		switch ft.Kind() {
		case reflect.String:
			kind = "string"
		case reflect.Bool:
			kind = "boolean"
		case reflect.Slice, reflect.Array:
			kind = "array"
		case reflect.Int, reflect.Int64, reflect.Uint64:
			kind = "integer"
		}
		props[name] = map[string]any{"type": kind, "optional": optional}
	}
	return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
}

func hostMethods() []HostMethod {
	sourceOutput := inputSchema(reflect.TypeOf(policySourceResponse{}))
	for name, property := range inputSchema(reflect.TypeOf(PolicySnapshot{}))["properties"].(map[string]any) {
		sourceOutput["properties"].(map[string]any)[name] = property
	}
	sourceOutput["properties"].(map[string]any)["followFallback"] = inputSchema(reflect.TypeOf(policyFollowFallbackMetadata{}))
	sourceOutput["properties"].(map[string]any)["starterSpell"] = inputSchema(reflect.TypeOf(policyFollowFallbackMetadata{}))
	identity := reflect.TypeOf(struct {
		SessionID     string `json:"sessionId"`
		CharacterGUID string `json:"characterGuid"`
	}{})
	return []HostMethod{
		{Name: "session.describe", Effect: "read", Description: "Discover this live body, module contracts and host controls.", Input: inputSchema(reflect.TypeOf(struct{}{}))},
		{Name: "session.attach", Effect: "read", Description: "Inspect the attached policy revision, pending actions and fault state.", Input: inputSchema(identity)},
		{Name: "session.policy_source", Effect: "read", Description: "Read all Lua source and the configured sourceLimitBytes for this body's attached policy. followFallback identifies its pure follow_formation export used after finite host goals expire; starterSpell identifies its pure starter_spell export for named starter spell selection. Both use this same accepted source revision. The same byte budget applies to every reload.", Input: inputSchema(identity), Output: sourceOutput},
		{Name: "session.policy_state", Effect: "read", Description: "Read one atomic committed Lua checkpoint, latest decision with its own revision/cursor/clock, current policy status and pending IDs. lastDispatch is an independent transport-return snapshot with its own body/revision/generation/cursor; it does not prove a later game effect. This read does not execute Lua or drain events; latestDecision is null until the current revision decides. Returned data is observational, not a command or writable state.", Input: inputSchema(reflect.TypeOf(policyStateRequest{})), Output: inputSchema(reflect.TypeOf(policyStateResponse{}))},
		{Name: "session.reload_policy", Effect: "mutation", Description: "Replace Lua in this same session using expectedRevision and source, or useBuiltin. State transfers without logout.", Input: inputSchema(reflect.TypeOf(policyEditRequest{}))},
		{Name: "session.send_event", Effect: "mutation", Description: "Deliver an ordered input event to the attached Lua policy. A queued acknowledgment is not completion of the requested task.", Input: inputSchema(reflect.TypeOf(policyEventRequest{}))},
		{Name: "session.clock", Effect: "read", Description: "Read the attached session's monotonic elapsed milliseconds and Unix wall milliseconds. Lua frame.clock uses the same clock, preserved across reloads; compare deadlines only within this session.", Input: inputSchema(reflect.TypeOf(policyClockRequest{})), Output: inputSchema(reflect.TypeOf(policyClockResponse{}))},
	}
}
