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
	identity := reflect.TypeOf(struct {
		SessionID     string `json:"sessionId"`
		CharacterGUID string `json:"characterGuid"`
	}{})
	return []HostMethod{
		{"session.describe", "read", "Discover this live body, module contracts and host controls.", inputSchema(reflect.TypeOf(struct{}{}))},
		{"session.attach", "read", "Inspect the attached policy revision, pending actions and fault state.", inputSchema(identity)},
		{"session.policy_source", "read", "Read all Lua source for this body's attached policy.", inputSchema(identity)},
		{"session.reload_policy", "mutation", "Replace Lua in this same session using expectedRevision and source, or useBuiltin. State transfers without logout.", inputSchema(reflect.TypeOf(policyEditRequest{}))},
		{"session.send_event", "mutation", "Deliver an ordered input event to the attached Lua policy. A queued acknowledgment is not completion of the requested task.", inputSchema(reflect.TypeOf(policyEventRequest{}))},
	}
}
