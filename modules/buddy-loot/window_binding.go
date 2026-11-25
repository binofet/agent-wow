package buddyloot

import (
	"strconv"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
)

// A window binding describes the server-observed UI transaction for a retained
// caller-selected corpse or quest-container opening.
func (s *Service) policyWindowBinding() (string, uint64, string) {
	if s.exactCorpse && s.corpseRequest != nil {
		return s.corpseRequest.request.RequestId, s.corpseRequest.request.CorpseGuid, "corpse"
	}
	r := s.state.LastGather
	if s.questGather && r != nil && r.Id != 0 && r.LootWindowObserved && s.window.guid == r.ObjectGuid && r.ObjectGuid == s.pendingGUID() {
		if s.gatherRequest != nil && s.gatherRequest.request.SourceGuid == r.ObjectGuid {
			return s.gatherRequest.request.RequestId, r.ObjectGuid, "quest_container"
		}
		return "gather-" + strconv.FormatUint(r.Id, 10), r.ObjectGuid, "quest_container"
	}
	return "", 0, ""
}
func (s *Service) policyWindow() bool { _, source, _ := s.policyWindowBinding(); return source != 0 }
func takeWindowSource(r *lootv1.TakeLootRequest) uint64 {
	if r == nil {
		return 0
	}
	if r.SourceGuid != 0 {
		return r.SourceGuid
	}
	return r.CorpseGuid
}
func closeWindowSource(r *lootv1.CloseCorpseRequest) uint64 {
	if r == nil {
		return 0
	}
	if r.SourceGuid != 0 {
		return r.SourceGuid
	}
	return r.CorpseGuid
}

func (s *Service) windowOperation(source uint64) *lootv1.CorpseOperation {
	if s.corpseRequest != nil && s.corpseRequest.request.CorpseGuid == source {
		return s.corpseReceiptLocked()
	}
	if s.gatherRequest != nil && s.gatherRequest.request.SourceGuid == source {
		return s.gatherReceiptLocked(s.gatherRequest)
	}
	r := s.state.LastGather
	if r == nil || r.ObjectGuid != source || !r.LootWindowObserved {
		return nil
	}
	return &lootv1.CorpseOperation{SessionId: s.state.SessionId, SelfGuid: s.selfGUID, SourceGuid: source,
		RequestId: "gather-" + strconv.FormatUint(r.Id, 10), Phase: s.state.Phase, Reason: s.state.Reason,
		OpeningTransportWritten: r.OpeningTransportWritten, WindowObserved: r.LootWindowObserved,
		ReleaseObserved: r.ReleaseObserved, Terminal: s.pendingGUID() != source && s.cleanup.source != source}
}
