package modrt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type policySummoning struct {
	code                                uint32
	payload                             []byte
	self, offerSequence, selectedMember uint64
	unknown                             bool
}

func (p *SessionPolicy) resolveSummoning(action map[string]any, capability string) (PolicyDispatch, error) {
	sid, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	guid, bodyOK := instanceEntryUint64(self, false)
	visit, visitOK := instanceEntryUint64(action["visitId"], false)
	if sid == "" || !bodyOK || !visitOK || !p.instanceEntryIdentity(sid, self, guid) {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current summoning body and observed visit required")
	}
	method := capability
	var path string
	var req, res proto.Message
	switch capability {
	case "select_target":
		method, path = "select_target", actionsv1.BuddyActions_SelectTarget_FullMethodName
		req, res = &actionsv1.SelectTargetRequest{}, &actionsv1.SelectTargetResponse{}
	case "use_summoning_object":
		path = actionsv1.BuddyActions_UseSummoningObject_FullMethodName
		req, res = &actionsv1.UseSummoningObjectRequest{}, &actionsv1.SummoningObservation{}
	case "respond_to_summon":
		path = actionsv1.BuddyActions_RespondToSummon_FullMethodName
		req, res = &actionsv1.RespondToSummonRequest{}, &actionsv1.SummoningObservation{}
	}
	params := make(map[string]any, len(action))
	for name, val := range action {
		if name == "capability" {
			continue
		}
		if name != "sessionId" && name != "accept" {
			allowZero := name == "expectedUseRevision" || name == "expectedCreatorGuid" || name == "selectedMemberGuid"
			if _, ok := instanceEntryUint64(val, allowZero); !ok {
				return PolicyDispatch{}, status.Error(codes.InvalidArgument, "summoning GUIDs and revisions require exact decimal strings")
			}
		}
		params[name] = val
	}
	raw, err := json.Marshal(params)
	if err != nil || protojson.Unmarshal(raw, req) != nil {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "invalid summoning action fields")
	}
	binding := &policySummoning{self: guid}
	switch r := req.(type) {
	case *actionsv1.SelectTargetRequest:
		if r.ExpectedGroupGuid == 0 || r.TargetGuid == 0 || r.TargetGuid == guid || r.TargetGuid>>48 != 0 {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "an exact online group target required")
		}
		binding.code, binding.payload = opcode.CMSGSetSelection, binary.LittleEndian.AppendUint64(nil, r.TargetGuid)
	case *actionsv1.UseSummoningObjectRequest:
		if r.ObjectGuid>>48 != 0xf110 || r.ObjectGuid&0xffffff == 0 || r.ExpectedGroupGuid == 0 || params["expectedUseRevision"] == nil {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "an exact ritual object, group and current use revision required")
		}
		binding.code, binding.payload = opcode.CMSGGameobjUse, binary.LittleEndian.AppendUint64(nil, r.ObjectGuid)
		binding.selectedMember = r.SelectedMemberGuid
	case *actionsv1.RespondToSummonRequest:
		if r.Accept == nil || r.SummonerGuid == 0 || r.SummonerGuid == guid || r.SummonerGuid>>48 != 0 || r.OfferSequence == 0 {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "an exact summon offer, summoner and explicit choice required")
		}
		binding.code, binding.offerSequence = opcode.CMSGSummonResponse, r.OfferSequence
		binding.payload = append(binary.LittleEndian.AppendUint64(nil, r.SummonerGuid), 0)
		if *r.Accept {
			binding.payload[8] = 1
		}
	}
	op := &policySpell{vm: p.vm, capability: capability, encounterVisit: visit, summoning: binding}
	return p.prepareObservedAction(capability, sid, self, guid, visit, method, path, req, res, op)
}

func (m *Manager) writePolicySummonScoped(ctx context.Context, token, sessionID, module string, code uint32, payload []byte, scope *modv1.TransportScope) (bool, error) {
	registry := &m.policySpells
	registry.mu.Lock()
	op := registry.active[token]
	if op == nil || op.summoning == nil {
		registry.mu.Unlock()
		return false, nil
	}
	binding := op.summoning
	if !op.admitted || op.attempted || module != actionsModule || op.origin.Writer != "lua" || op.origin.Module != module ||
		op.origin.SessionID != sessionID || op.origin.ActionID == "" || !scope.GetVisitBound() || scope.GetConnectionId() == "" ||
		scope.GetVisitRevision() == 0 || scope.GetSelfGuid() != binding.self || scope.GetSummonOfferSequence() != binding.offerSequence ||
		scope.GetMeetingStoneTargetGuid() != binding.selectedMember || code != binding.code || !bytes.Equal(payload, binding.payload) || ctx.Err() != nil || m.session.SendScopedPacket == nil {
		registry.mu.Unlock()
		return true, modcb.PacketNotDispatchedError("summoning callback is outside the reserved body, offer or object action")
	}
	op.attempted = true
	origin := op.origin
	registry.mu.Unlock()
	err := m.session.SendScopedPacket(modsession.WithWriteOrigin(ctx, origin), code, append([]byte(nil), payload...), proto.Clone(scope).(*modv1.TransportScope))
	registry.mu.Lock()
	op.written, binding.unknown = err == nil, err != nil && !modcb.PacketWasNotDispatched(err)
	if modcb.PacketWasNotDispatched(err) {
		op.attempted = false
	}
	registry.mu.Unlock()
	return true, err
}
