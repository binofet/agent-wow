package buddytrade

import (
	"context"
	"encoding/json"
	"strconv"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// This finite admission belongs to one native exact request. The existing
// request history, not another action ledger, reserves its one transport
// attempt. A later policy reload does not reinterpret this captured decision.
type junkPolicyAdmission struct {
	revision string
	world    *worldv1.SnapshotResponse
	source   *worldv1.BackpackSlot
	template *tradev1.ItemDetails
}

func (s *Service) admitJunkPolicy(ctx context.Context, request *tradev1.JunkRequest, before *worldv1.SnapshotResponse, selected *worldv1.BackpackSlot) (*junkPolicyAdmission, error) {
	refuse := func() (*junkPolicyAdmission, error) {
		return nil, status.Error(codes.FailedPrecondition, "current_owned_disposal_policy_admission_required")
	}
	if s.session == nil || before == nil || !before.InventoryKnown || before.InventoryRevision == 0 || selected == nil {
		return refuse()
	}
	s.mu.Lock()
	observed := s.lookup.items[selected.Entry]
	var template *tradev1.ItemDetails
	if observed.GetAvailable() && !observed.GetPending() && observed.ItemId == selected.Entry && observed.RawTemplateFacts != nil {
		template = proto.Clone(observed).(*tradev1.ItemDetails)
	}
	s.mu.Unlock()
	if template == nil {
		return refuse()
	}
	item, err := junkPolicyObject(template)
	if err != nil {
		return refuse()
	}
	self := strconv.FormatUint(s.selfGUID, 10)
	input := map[string]any{
		"sessionId": request.SessionId, "selfGuid": self, "kind": "owned_disposal", "mode": "owned_destroy",
		"item": item, "source": map[string]any{"bag": float64(selected.Bag), "slot": float64(selected.Slot),
			"itemGuid": strconv.FormatUint(selected.ItemGuid, 10), "entry": float64(selected.Entry), "stackCount": float64(selected.StackCount)},
		"expected": map[string]any{"inventoryRevision": strconv.FormatUint(before.InventoryRevision, 10),
			"equipmentRevision": strconv.FormatUint(before.SelfEquipmentRevision, 10), "questLogRevision": strconv.FormatUint(before.SelfQuestLogRevision, 10)},
	}
	available, revision, decision, err := modcb.DisposalDecision(ctx, s.session, request.SessionId, s.selfGUID, input)
	reason, reasonKnown := decision["reason"].(string)
	if err != nil || !available || decision["status"] != "eligible" || !reasonKnown || len(reason) == 0 || len(reason) > 128 {
		return refuse()
	}
	witness, ok := decision["nativeContext"].(map[string]any)
	if !ok || len(witness) != 4 || witness["sessionId"] != request.SessionId || witness["selfGuid"] != self {
		return refuse()
	}
	raw, ok := witness["world"].(map[string]any)
	if !ok || !junkPolicyKeys(raw, "self", "inventoryKnown", "inventoryRevision", "selfEquipmentKnown", "selfEquipmentRevision", "selfEquipment", "selfQuestLogKnown", "selfQuestLogRevision", "selfQuestLog") || len(raw) != 9 {
		return refuse()
	}
	body, ok := raw["self"].(map[string]any)
	if !ok || len(body) != 2 || body["guid"] != self || body["present"] != true {
		return refuse()
	}
	instance, ok := witness["source"].(map[string]any)
	if !ok || !junkPolicyKeys(instance, "bag", "slot", "itemGuid", "entry", "stackCount", "metadataKnown", "enhancementsKnown", "hasEnhancements", "durabilityKnown", "durability", "maxDurability") {
		return refuse()
	}
	admission := &junkPolicyAdmission{revision: revision, world: &worldv1.SnapshotResponse{}, source: &worldv1.BackpackSlot{}, template: template}
	if junkPolicyDecode(raw, admission.world) != nil || junkPolicyDecode(instance, admission.source) != nil || admission.matches(before, selected, request, s.selfGUID) != nil {
		return refuse()
	}
	return admission, nil
}

// matches is pure: the final World remains the last dependency before the
// existing native reservation and send. Compare all referenced instance and
// raw context fields, even when their known flags are false.
func (p *junkPolicyAdmission) matches(current *worldv1.SnapshotResponse, selected *worldv1.BackpackSlot, request *tradev1.JunkRequest, self uint64) error {
	refuse := func() error { return status.Error(codes.FailedPrecondition, "owned_disposal_policy_context_changed") }
	if p == nil || p.revision == "" || p.world == nil || p.source == nil || current == nil || request == nil || request.RequestId == "" ||
		p.world.GetSelf().GetGuid() != self || !p.world.GetSelf().GetPresent() || current.GetSelf().GetGuid() != self || !current.GetSelf().GetPresent() ||
		!p.world.InventoryKnown || p.world.InventoryRevision == 0 || !current.InventoryKnown || current.InventoryRevision != p.world.InventoryRevision ||
		!p.world.SelfEquipmentKnown || p.world.SelfEquipmentRevision == 0 || !current.SelfEquipmentKnown || current.SelfEquipmentRevision != p.world.SelfEquipmentRevision ||
		!p.world.SelfQuestLogKnown || p.world.SelfQuestLogRevision == 0 || !current.SelfQuestLogKnown || current.SelfQuestLogRevision != p.world.SelfQuestLogRevision ||
		!proto.Equal(p.source, selected) || junkRequestContextSafe(p.world, request) != nil || junkRequestContextSafe(current, request) != nil {
		return refuse()
	}
	var equipment [19]*worldv1.EquipmentSlotState
	var log [25]*worldv1.QuestLogEntry
	for _, row := range p.world.SelfEquipment {
		equipment[row.Slot] = row
	}
	for _, row := range current.SelfEquipment {
		if !proto.Equal(equipment[row.Slot], row) {
			return refuse()
		}
	}
	for _, row := range p.world.SelfQuestLog {
		log[row.Slot] = row
	}
	for _, row := range current.SelfQuestLog {
		if !proto.Equal(log[row.Slot], row) {
			return refuse()
		}
	}
	return nil
}

func junkPolicyKeys(value map[string]any, allowed ...string) bool {
	for key := range value {
		found := false
		for _, name := range allowed {
			found = found || key == name
		}
		if !found {
			return false
		}
	}
	return true
}

func junkPolicyObject(message proto.Message) (map[string]any, error) {
	raw, err := protojson.Marshal(message)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	err = json.Unmarshal(raw, &value)
	return value, err
}

func junkPolicyDecode(value map[string]any, message proto.Message) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return protojson.Unmarshal(raw, message)
}
