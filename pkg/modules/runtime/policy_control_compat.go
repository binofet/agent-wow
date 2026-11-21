package modrt

import (
	"context"
	"strconv"

	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func legacyControlRequest(ctx context.Context, name, path string) (string, bool) {
	if name != "buddy-actions" {
		return "", false
	}
	technique := ""
	switch path {
	case "/warcraftbuddy.actions.v1.BuddyActions/Polymorph":
		technique = "polymorph"
	case "/warcraftbuddy.actions.v1.BuddyActions/Sap":
		technique = "sap"
	default:
		return "", false
	}
	origin := modsession.WriteOriginFromContext(ctx)
	// Only a reservation created by the attached VM is an exact native cast.
	// A public/module legacy request is gameplay intent for that same VM.
	return technique, !(origin.Writer == "lua" && origin.Module == actionsModule && origin.ActionID != "")
}
func (m *Manager) queueLegacyControl(ctx context.Context, inst *instance, request, result proto.Message, technique string) error {
	view := request.ProtoReflect()
	fields := view.Descriptor().Fields()
	sid, rank, target := fields.ByName("session_id"), fields.ByName("spell_id"), fields.ByName("target_guid")
	prepare, cancel := fields.ByName("approach"), fields.ByName("cancel")
	if sid == nil || rank == nil || target == nil || prepare == nil || cancel == nil {
		return status.Error(codes.InvalidArgument, "current session, exact spell rank and NPC GUID required")
	}
	patient, spell := view.Get(target).Uint(), view.Get(rank).Uint()
	if view.Get(sid).String() == "" || spell == 0 || patient>>48 != 0xf130 || patient&0xffffff == 0 || view.Get(prepare).Bool() && view.Get(cancel).Bool() {
		return status.Error(codes.InvalidArgument, "current session, exact spell rank and NPC GUID required")
	}
	catalog, _, err := m.observedSession()
	if err != nil {
		return err
	}
	goal := map[string]any{"task": "control", "source": "legacy_rpc", "technique": technique,
		"sessionId": view.Get(sid).String(), "selfGuid": catalog.CharacterGUID, "targetGuid": strconv.FormatUint(patient, 10), "spellId": float64(spell), "maintain": false,
		"id": "legacy:" + technique + ":" + strconv.FormatUint(patient, 10) + ":" + strconv.FormatUint(spell, 10)}
	if view.Get(cancel).Bool() {
		goal["cancel"] = true
	}
	return m.queuePolicyActionInput(ctx, inst, result, "command", goal, "lua_control_request_queued:")
}
