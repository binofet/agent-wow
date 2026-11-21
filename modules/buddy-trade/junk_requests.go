package buddytrade

import (
	"regexp"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const junkRequestHistoryLimit = 256

var junkRequestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type junkRequestRecord struct {
	request *tradev1.JunkRequest
	receipt *tradev1.JunkDisposal
}

func validateJunkRequestHistory(r *tradev1.JunkRequest) error {
	if r.RequestId == "" {
		if r.ExpectedEquipmentRevision != 0 || r.ExpectedQuestLogRevision != 0 {
			return status.Error(codes.InvalidArgument, "request_id_required_for_context_binding")
		}
		return nil
	}
	if !junkRequestPattern.MatchString(r.RequestId) || r.ExpectedEquipmentRevision == 0 ||
		r.ExpectedQuestLogRevision == 0 || proto.Size(r) > 1024 || len(r.ProtoReflect().GetUnknown()) != 0 {
		return status.Error(codes.InvalidArgument, "bounded_request_id_and_exact_equipment_quest_revisions_required")
	}
	return nil
}

// Exact repeats are receipt reads, including after the global state revision
// changes. No peer read, lease operation or packet can occur on this path.
func (s *Service) replayJunkRequest(r *tradev1.JunkRequest) (*tradev1.State, bool, error) {
	if r.RequestId == "" {
		return nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.SessionId != s.state.SessionId {
		return nil, true, status.Error(codes.FailedPrecondition, "trade_session_changed")
	}
	old := s.junkRequests[r.RequestId]
	if old == nil {
		return nil, false, nil
	}
	if !proto.Equal(old.request, r) {
		return nil, true, status.Error(codes.AlreadyExists, "junk_request_id_payload_changed")
	}
	out := proto.Clone(s.state).(*tradev1.State)
	out.LastJunkDisposal = proto.Clone(old.receipt).(*tradev1.JunkDisposal)
	return out, true, nil
}

func junkRequestContextSafe(w *worldv1.SnapshotResponse, r *tradev1.JunkRequest) error {
	if r.RequestId == "" {
		return nil
	}
	if w == nil || !w.SelfEquipmentKnown || w.SelfEquipmentRevision != r.ExpectedEquipmentRevision ||
		!w.SelfQuestLogKnown || w.SelfQuestLogRevision != r.ExpectedQuestLogRevision ||
		len(w.SelfEquipment) != 19 || len(w.SelfQuestLog) != 25 {
		return status.Error(codes.FailedPrecondition, "exact_complete_equipment_and_quest_context_required")
	}
	var equipment [19]bool
	guids := make(map[uint64]bool)
	owned := make(map[uint64]bool)
	for _, row := range w.Backpack {
		if row.GetItemGuid() != 0 {
			owned[row.GetItemGuid()] = true
		}
	}
	for _, bag := range w.InventoryBags {
		if bag.GetContainerGuid() != 0 {
			owned[bag.GetContainerGuid()] = true
		}
		for _, row := range bag.GetSlots() {
			if row.GetItemGuid() != 0 {
				owned[row.GetItemGuid()] = true
			}
		}
	}
	for _, row := range w.SelfEquipment {
		if row == nil || row.Slot >= 19 || equipment[row.Slot] || !row.SlotKnown {
			return status.Error(codes.FailedPrecondition, "complete_unique_equipment_context_required")
		}
		equipment[row.Slot] = true
		if row.ItemGuid == 0 {
			if row.Entry != 0 || row.StackCount != 0 || row.MetadataKnown {
				return status.Error(codes.FailedPrecondition, "empty_equipment_context_inconsistent")
			}
		} else if !carriedJunkItemGUID(row.ItemGuid) || guids[row.ItemGuid] || owned[row.ItemGuid] ||
			row.ItemGuid == r.ExpectedItemGuid ||
			!row.MetadataKnown || row.Entry == 0 || row.StackCount != 1 {
			return status.Error(codes.FailedPrecondition, "owned_equipment_context_inconsistent")
		}
		guids[row.ItemGuid] = true
	}
	var slots [25]bool
	quests := make(map[uint32]bool)
	for _, row := range w.SelfQuestLog {
		if row == nil || row.Slot >= 25 || slots[row.Slot] || row.QuestId != 0 && quests[row.QuestId] {
			return status.Error(codes.FailedPrecondition, "complete_unique_quest_context_required")
		}
		slots[row.Slot] = true
		if row.QuestId != 0 {
			quests[row.QuestId] = true
		}
	}
	return nil
}

// Called only under mu after the complete final dispatch validation. History
// starts with the write reservation and is never evicted during this session.
func (s *Service) reserveJunkRequestLocked(r *tradev1.JunkRequest, w *worldv1.SnapshotResponse) {
	if r.RequestId == "" {
		return
	}
	s.junkPending.RequestId = r.RequestId
	s.junkPending.EquipmentRevision = w.SelfEquipmentRevision
	s.junkPending.QuestLogRevision = w.SelfQuestLogRevision
	if s.junkRequests == nil {
		s.junkRequests = make(map[string]*junkRequestRecord)
	}
	s.junkRequests[r.RequestId] = &junkRequestRecord{request: proto.Clone(r).(*tradev1.JunkRequest), receipt: s.junkPending}
	s.state.JunkOperations = append(s.state.JunkOperations, s.junkPending)
}

func (s *Service) junkReceiptStatusLocked(value string) {
	if s.junkPending != nil {
		s.junkPending.Status = value
	}
	if s.state.LastJunkDisposal != nil {
		s.state.LastJunkDisposal.Status = value
	}
}
