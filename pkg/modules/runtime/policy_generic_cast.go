package modrt

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strconv"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcast"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func (p *SessionPolicy) resolveGenericSpell(action map[string]any, cancel bool) (PolicyDispatch, error) {
	sid, _ := action["sessionId"].(string)
	body, _ := action["selfGuid"].(string)
	visitText, _ := action["visitId"].(string)
	visit, e := strconv.ParseUint(visitText, 10, 64)
	if sid != p.vm.Status().SessionID || body != p.body || e != nil || visit == 0 || visitText != strconv.FormatUint(visit, 10) {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current session/body and exact observed visit required")
	}
	fields := map[string]any{"sessionId": sid, "selfGuid": body, "visitId": visitText, "spellId": action["spellId"]}
	op := &policySpell{vm: p.vm, capability: "cast_spell", encounterVisit: visit}
	var req proto.Message
	method := "cast_spell"
	dismount := action["capability"] == "dismount_ground"
	if dismount {
		method = "dismount_ground"
		op.capability = method
		fields["followMount"] = action["followMount"]
		r := &actionsv1.DismountGroundRequest{}
		req = r
		op.dismount = r
	} else if cancel {
		method = "cancel_spell"
		op.capability = method
		fields["expectedOperationRevision"] = action["expectedOperationRevision"]
		fields["channel"] = action["channel"]
		r := &actionsv1.CancelSpellRequest{}
		req = r
		op.cancelRequest = r
	} else {
		if v, ok := action["followMount"]; ok {
			fields["followMount"] = v
		}
		if v, ok := action["targets"]; ok {
			fields["targets"] = v
		}
		if v, ok := action["trajectory"]; ok {
			fields["trajectory"] = v
		}
		r := &actionsv1.CastSpellRequest{}
		req = r
		op.genericRequest = r
	}
	wire, err := json.Marshal(fields)
	if err == nil {
		err = protojson.Unmarshal(wire, req)
	}
	if err != nil {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "invalid exact generic spell DTO: "+err.Error())
	}
	if dismount {
		r := op.dismount
		if r.SpellId == 0 || r.GetFollowMount().GetOwnerGuid() == 0 || r.GetFollowMount().GetOwnerMountRevision() == 0 {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact witnessed owner and ground mount required")
		}
		op.spell = r.SpellId
	} else if cancel {
		if op.cancelRequest.SpellId == 0 || op.cancelRequest.ExpectedOperationRevision == 0 {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact active spell and operation revision required")
		}
		op.spell = op.cancelRequest.SpellId
	} else {
		if _, err := spellcast.Encode(0, op.genericRequest); err != nil {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, err.Error())
		}
		op.spell = op.genericRequest.SpellId
		op.target = op.genericRequest.GetTargets().GetObjectGuid()
	}
	values := map[protoreflect.Name]protoreflect.Value{}
	req.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool { values[fd.Name()] = v; return true })
	return p.preparePolicyDispatch(method, values, op)
}

func (op *policySpell) matchesGenericPacket(code uint32, payload []byte) bool {
	if r := op.cancelRequest; r != nil {
		if r.Channel {
			return code == opcode.CMSGCancelChannelling && len(payload) == 4 && binary.LittleEndian.Uint32(payload) == r.SpellId
		}
		return code == opcode.CMSGCancelCast && len(payload) == 5 && binary.LittleEndian.Uint32(payload[1:]) == r.SpellId
	}
	if code != opcode.CMSGCastSpell || len(payload) == 0 {
		return false
	}
	expected, err := spellcast.Encode(payload[0], op.genericRequest)
	return err == nil && bytes.Equal(payload, expected)
}
