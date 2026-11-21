package modrt

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const ownedDisposalInputKind = "owned_disposal"

// ownedDisposalInput collects facts through the session's existing routing.
// In particular, raw_gear_facts never enters Inventory.observe or Trade.state.
// This finishes before classification, and before the native executor's final
// World read. The returned witness is factual data, not packet authority.
func (m *Manager) ownedDisposalInput(ctx context.Context, sid, self string, input map[string]any) (map[string]any, map[string]any, error) {
	if len(input) != 7 {
		return nil, nil, errors.New("exact owned disposal input required")
	}
	mode, ok := input["mode"].(string)
	if !ok || mode != "misc_destroy" && mode != "gray_armor_destroy" && mode != "vendor_sale" {
		return nil, nil, errors.New("known disposal mode required")
	}
	source := &inventoryv1.ItemRef{}
	item := &tradev1.ItemDetails{}
	if decodeDisposalMessage(input["source"], source) != nil || decodeDisposalMessage(input["item"], item) != nil ||
		source.ItemGuid == 0 || source.Entry == 0 || source.StackCount == 0 || source.Entry != item.ItemId {
		return nil, nil, errors.New("exact typed source and matching item template required")
	}
	expected, ok := input["expected"].(map[string]any)
	if !ok || len(expected) != 3 {
		return nil, nil, errors.New("exact native inventory equipment and quest revisions required")
	}
	revisions := make([]uint64, 0, 3)
	for _, key := range []string{"inventoryRevision", "equipmentRevision", "questLogRevision"} {
		text, ok := expected[key].(string)
		n, err := strconv.ParseUint(text, 10, 64)
		if !ok || err != nil || n == 0 || strconv.FormatUint(n, 10) != text {
			return nil, nil, errors.New("positive canonical native context revisions required")
		}
		revisions = append(revisions, n)
	}
	world := &worldv1.SnapshotResponse{}
	inv := &inventoryv1.State{}
	journal := &questsv1.JournalResponse{}
	if err := m.disposalFactRead(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, world); err != nil {
		return nil, nil, err
	}
	if world.GetSelf().GetGuid() == 0 || strconv.FormatUint(world.GetSelf().GetGuid(), 10) != self ||
		world.InventoryRevision != revisions[0] || world.SelfEquipmentRevision != revisions[1] || world.SelfQuestLogRevision != revisions[2] {
		return nil, nil, errors.New("native disposal before-frame changed")
	}
	if err := m.disposalFactRead(ctx, "buddy-inventory", "raw_gear_facts", &emptypb.Empty{}, inv); err != nil {
		return nil, nil, err
	}
	if inv.SessionId != sid || strconv.FormatUint(inv.SelfGuid, 10) != self {
		return nil, nil, errors.New("cached disposal facts belong to another body")
	}
	if err := m.disposalFactRead(ctx, "buddy-quests", "journal", &questsv1.JournalRequest{}, journal); err != nil {
		return nil, nil, err
	}
	// The requested mode determines the factual projection, never the item
	// class or its disposal eligibility. Template-only modes do not require
	// physical equipment destinations that miscellaneous items cannot have.
	project := projectDisposalOwnershipContext
	if mode == "gray_armor_destroy" {
		project = projectDisposalContext
	}
	context, err := project(inv, world, journal, source)
	if err != nil {
		return nil, nil, err
	}
	template, err := disposalMessageMap(item)
	if err != nil {
		return nil, nil, err
	}
	// The witness retains the actual raw equipment and quest vectors as well
	// as the selected instance. A later executor must compare them to its last
	// World read; a matching revision alone is not a substitute for those facts.
	witnessWorld := &worldv1.SnapshotResponse{
		Self:           &worldv1.PlayerState{Guid: world.GetSelf().GetGuid(), Present: world.GetSelf().GetPresent()},
		InventoryKnown: world.InventoryKnown, InventoryRevision: world.InventoryRevision,
		SelfEquipmentKnown: world.SelfEquipmentKnown, SelfEquipmentRevision: world.SelfEquipmentRevision, SelfEquipment: world.SelfEquipment,
		SelfQuestLogKnown: world.SelfQuestLogKnown, SelfQuestLogRevision: world.SelfQuestLogRevision, SelfQuestLog: world.SelfQuestLog,
	}
	proof, err := disposalMessageMap(witnessWorld)
	if err != nil {
		return nil, nil, err
	}
	witness := map[string]any{"world": proof, "source": context["carriedSource"], "sessionId": sid, "selfGuid": self}
	frame := map[string]any{"sessionId": sid, "selfGuid": self, "mode": mode, "item": template, "context": context}
	return frame, witness, nil
}

func (m *Manager) disposalFactRead(ctx context.Context, module, alias string, request, result proto.Message) error {
	inst, method, err := m.lookup(module, alias, false)
	if err != nil {
		return err
	}
	return m.invoke(ctx, internalCall, "session-disposal", module, inst, method, request, result)
}

func decodeDisposalMessage(value any, message proto.Message) error {
	fields, ok := value.(map[string]any)
	if !ok {
		return errors.New("typed disposal object required")
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return protojson.Unmarshal(raw, message)
}

func disposalMessageMap(message proto.Message) (map[string]any, error) {
	raw, err := protojson.Marshal(message)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(raw, &out)
	return out, err
}
