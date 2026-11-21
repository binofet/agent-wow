package buddyinventory

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
	"time"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const rollLiveLimit = 16
const rollHistoryLimit = 256

type groupRoll struct {
	offer    *inventoryv1.RollOffer
	deadline time.Time
	attempt  *rollAttempt
}

type rollAttempt struct {
	request *inventoryv1.RollRequest
	receipt *inventoryv1.RollReceipt
	token   string
}

var rollChoices = map[string]byte{"pass": 0, "need": 1, "greed": 2, "disenchant": 3}

// Group::SendLootStartRoll, normal 3.3.5 public packet. The source is a roll
// GUID, not proof of an item carried by this character.
// https://www.azerothcore.org/doxygen/d0/db7/classGroup.html
func decodeRollOffer(p []byte) *inventoryv1.RollOffer {
	if len(p) != 37 {
		return nil
	}
	out := &inventoryv1.RollOffer{
		RollGuid: binary.LittleEndian.Uint64(p), MapId: binary.LittleEndian.Uint32(p[8:]),
		LootSlot: binary.LittleEndian.Uint32(p[12:]), ItemEntry: binary.LittleEndian.Uint32(p[16:]),
		RandomSuffix: binary.LittleEndian.Uint32(p[20:]), RandomProperty: binary.LittleEndian.Uint32(p[24:]),
		ItemCount: binary.LittleEndian.Uint32(p[28:]), RemainingMs: uint64(binary.LittleEndian.Uint32(p[32:])),
		OfferedMask: uint32(p[36]), Phase: "pending",
	}
	if !itemGUID(out.RollGuid) || out.LootSlot > 255 || out.ItemEntry == 0 || out.ItemEntry >= 1<<31 ||
		out.ItemCount == 0 || out.RemainingMs == 0 || out.RemainingMs > 60000 ||
		out.OfferedMask == 0 || out.OfferedMask&^uint32(15) != 0 {
		return nil
	}
	return out
}

func sameRollTuple(a, b *inventoryv1.RollOffer) bool {
	return a.RollGuid == b.RollGuid && a.MapId == b.MapId && a.LootSlot == b.LootSlot &&
		a.ItemEntry == b.ItemEntry && a.RandomSuffix == b.RandomSuffix &&
		a.RandomProperty == b.RandomProperty && a.ItemCount == b.ItemCount && a.OfferedMask == b.OfferedMask
}

func (s *Service) retireRollLocked(roll *groupRoll, phase string) {
	roll.offer.Available, roll.offer.Phase = false, phase
	if op := roll.attempt; op != nil && !op.receipt.ServerVoteObserved && op.receipt.Phase != "not_dispatched" {
		op.receipt.Phase = "outcome_unconfirmed_no_retry"
	}
	s.rollRevision++
}

func (s *Service) expireRollsLocked() {
	for _, roll := range s.groupRolls {
		if !s.now().Before(roll.deadline) && (roll.offer.Phase == "pending" || roll.offer.Phase == "reserved" || roll.offer.Phase == "voted") {
			s.retireRollLocked(roll, "expired")
		}
	}
}

func validRollEntry(p []byte) bool {
	if len(p) != 20 {
		return false
	}
	for at := 4; at < 20; at += 4 {
		n := float64(math.Float32frombits(binary.LittleEndian.Uint32(p[at:])))
		if !(math.Abs(n) < 1e6) {
			return false
		}
	}
	return true
}

func (s *Service) rollBoundaryLocked(code uint32, p []byte) bool {
	entry := code == opcode.SMSGLoginVerifyWorld || code == opcode.SMSGNewWorld
	pending := code == opcode.SMSGTransferPending
	if !entry && !pending {
		return false
	}
	if entry && !validRollEntry(p) || pending && len(p) != 4 && len(p) != 12 {
		s.state.MalformedPackets++
		return true
	}
	for _, roll := range s.groupRolls {
		if roll.offer.Phase == "pending" || roll.offer.Phase == "reserved" || roll.offer.Phase == "voted" {
			s.retireRollLocked(roll, "world_retired")
		}
	}
	s.rollVisitBound = entry
	if entry {
		s.rollVisitID++
		s.rollVisitMap = binary.LittleEndian.Uint32(p)
		s.rollSaturated = false
	}
	s.rollRevision++
	return true
}

func (s *Service) observeRollPacketLocked(p *modv1.WorldPacket) bool {
	if s.rollBoundaryLocked(p.Opcode, p.Payload) {
		return true
	}
	if p.Opcode != opcode.SMSGLootStartRoll && p.Opcode != opcode.SMSGLootRoll {
		return false
	}
	if s.closed {
		return true
	}
	s.expireRollsLocked()
	if p.Opcode == opcode.SMSGLootStartRoll {
		offer := decodeRollOffer(p.Payload)
		if offer == nil {
			s.state.MalformedPackets++
			return true
		}
		if old := s.groupRolls[offer.RollGuid]; old != nil {
			if !sameRollTuple(old.offer, offer) {
				// The second tuple cannot replace an immutable source. Its
				// otherwise untracked echo could also alias another offer.
				s.rollSaturated = true
				for _, roll := range s.groupRolls {
					s.retireRollLocked(roll, "contradictory_offer")
				}
			}
			return true // A repeated offer never refreshes its original deadline.
		}
		live := 0
		for _, roll := range s.groupRolls {
			if s.now().Before(roll.deadline) && roll.offer.VisitId == s.rollVisitID {
				live++
			}
		}
		if live >= rollLiveLimit || len(s.groupRolls) >= rollHistoryLimit {
			s.rollSaturated = true // Unknown omitted tuples cannot prove unique echoes.
			for _, roll := range s.groupRolls {
				s.retireRollLocked(roll, "capacity_unknown")
			}
			return true
		}
		s.rollRevision++
		offer.Revision, offer.VisitId = s.rollRevision, s.rollVisitID
		offer.Available = s.rollVisitBound && offer.MapId == s.rollVisitMap && !s.rollSaturated
		if !offer.Available {
			offer.Phase = "world_retired"
		}
		s.groupRolls[offer.RollGuid] = &groupRoll{offer: offer, deadline: s.now().Add(time.Duration(offer.RemainingMs) * time.Millisecond)}
		return true
	}
	b := p.Payload
	if len(b) != 35 || b[34] > 1 {
		s.state.MalformedPackets++
		return true
	}
	if binary.LittleEndian.Uint64(b) != 0 || binary.LittleEndian.Uint64(b[12:]) != s.selfGUID ||
		b[34] != 0 || !s.rollVisitBound || s.rollSaturated {
		return true
	}
	var match *groupRoll
	for _, roll := range s.groupRolls {
		o := roll.offer
		if o.VisitId != s.rollVisitID || !s.now().Before(roll.deadline) ||
			o.LootSlot != binary.LittleEndian.Uint32(b[8:]) || o.ItemEntry != binary.LittleEndian.Uint32(b[20:]) ||
			o.RandomSuffix != binary.LittleEndian.Uint32(b[24:]) || o.RandomProperty != binary.LittleEndian.Uint32(b[28:]) {
			continue
		}
		if match != nil {
			return true
		}
		match = roll
	}
	if match == nil || match.attempt == nil || match.offer.Phase != "voted" {
		return true
	}
	op := match.attempt
	number, kind := byte(128), rollChoices[op.request.Choice]
	if kind == 1 {
		number, kind = 0, 0
	}
	if !op.receipt.TransportAttempted || op.receipt.ServerVoteObserved || b[32] != number || b[33] != kind {
		return true
	}
	op.receipt.ServerVoteObserved, op.receipt.Phase = true, "server_vote_observed"
	s.rollRevision++
	return true
}

func (s *Service) rollSnapshotLocked() *inventoryv1.RollState {
	s.expireRollsLocked()
	out := &inventoryv1.RollState{SessionId: s.state.SessionId, SelfGuid: s.selfGUID, Revision: s.rollRevision, VisitId: s.rollVisitID}
	ids := make([]uint64, 0, len(s.groupRolls))
	for id := range s.groupRolls {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		roll := s.groupRolls[id]
		if !s.now().Before(roll.deadline) || roll.offer.VisitId != s.rollVisitID {
			continue
		}
		o := proto.Clone(roll.offer).(*inventoryv1.RollOffer)
		o.RemainingMs = uint64(max(0, roll.deadline.Sub(s.now()).Milliseconds()))
		out.Offers = append(out.Offers, o)
	}
	keys := make([]string, 0, len(s.rollRequests))
	for key := range s.rollRequests {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out.Receipts = append(out.Receipts, proto.Clone(s.rollRequests[key].receipt).(*inventoryv1.RollReceipt))
	}
	if s.closed || !s.rollVisitBound || s.rollSaturated {
		out.Reason = "current_roll_visit_unavailable"
	}
	return out
}

func (s *Service) rollWorld(ctx context.Context) (*worldv1.SnapshotResponse, error) {
	w := &worldv1.SnapshotResponse{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w); err != nil {
		return nil, err
	}
	s.mu.Lock()
	valid := !s.closed && s.selfGUID != 0 && s.selfGUID>>48 == 0 && s.rollVisitBound && !s.rollSaturated && w.GetVisitRevision() == s.rollVisitID &&
		w.GetSelf().GetGuid() == s.selfGUID && w.Self.Present && w.Self.MapKnown && w.Self.MapId == s.rollVisitMap
	s.mu.Unlock()
	if !valid {
		return nil, fail("current_own_roll_world_visit_required")
	}
	return w, nil
}

func (s *Service) Rolls(ctx context.Context, _ *emptypb.Empty) (*inventoryv1.RollState, error) {
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	w, err := s.rollWorld(call)
	s.mu.Lock()
	out := s.rollSnapshotLocked()
	s.mu.Unlock()
	if err != nil {
		out.Reason = "current_own_roll_world_visit_required"
		for _, offer := range out.Offers {
			offer.Available = false
		}
		return out, nil
	}
	p := w.Self
	out.Player = &worldv1.PlayerState{Guid: p.Guid, Present: p.Present, MapKnown: p.MapKnown, MapId: p.MapId,
		ClassKnown: p.ClassKnown, ClassId: p.ClassId, RaceKnown: p.RaceKnown, RaceId: p.RaceId,
		LevelKnown: p.LevelKnown, Level: p.Level}
	if equipment, known := equipmentView(view{w: w}); known {
		out.EquipmentKnown, out.EquipmentRevision = true, w.SelfEquipmentRevision
		out.Equipment = equipment[:]
	}
	out.SkillsKnown, out.SkillsRevision = w.SelfSkillsKnown, w.SelfSkillsRevision
	if _, known := ownedSkills(w); known {
		out.SkillsComplete = true
		for _, row := range w.SelfSkills {
			if row.SkillId != 0 {
				out.Skills = append(out.Skills, row)
			}
		}
	}
	entries := map[uint32]bool{}
	for _, offer := range out.Offers {
		entries[offer.ItemEntry] = true
	}
	for _, row := range out.Equipment {
		if row.MetadataKnown && row.Entry != 0 {
			entries[row.Entry] = true
		}
	}
	ids := make([]uint32, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out.Metadata = s.rollItemMetadata(call, ids)
	s.mu.Lock()
	fresh := s.rollSnapshotLocked()
	s.mu.Unlock()
	fresh.Player, fresh.EquipmentKnown, fresh.EquipmentRevision, fresh.Equipment = out.Player, out.EquipmentKnown, out.EquipmentRevision, out.Equipment
	fresh.SkillsKnown, fresh.SkillsRevision, fresh.SkillsComplete, fresh.Skills = out.SkillsKnown, out.SkillsRevision, out.SkillsComplete, out.Skills
	fresh.Metadata = out.Metadata
	if fresh.VisitId != w.VisitRevision || fresh.Reason != "" {
		fresh.Player, fresh.Equipment, fresh.Skills, fresh.Metadata = nil, nil, nil, nil
		fresh.EquipmentKnown, fresh.SkillsKnown, fresh.SkillsComplete = false, false, false
	} else {
		fresh.Reason = out.Reason
	}
	return fresh, nil
}

func containsRollEntry(ids []uint32, id uint32) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func (s *Service) Roll(ctx context.Context, request *inventoryv1.RollRequest) (*inventoryv1.RollState, error) {
	choice, ok := rollChoices[request.GetChoice()]
	if request == nil || !requestPattern.MatchString(request.RequestId) || request.SessionId == "" ||
		!itemGUID(request.RollGuid) || request.ExpectedOfferRevision == 0 || request.VisitId == 0 || !ok {
		return nil, status.Error(codes.InvalidArgument, "exact_session_request_offer_revision_visit_and_vote_required")
	}
	token, supplied := modcb.IncomingPolicySpell(ctx)
	if supplied && token == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid_policy_roll_token")
	}
	s.mu.Lock()
	if old := s.rollRequests[request.RequestId]; old != nil {
		if !proto.Equal(old.request, request) {
			s.mu.Unlock()
			return nil, fail("roll_request_id_already_bound")
		}
		out := s.rollSnapshotLocked()
		s.mu.Unlock()
		return out, nil
	}
	s.expireRollsLocked()
	roll := s.groupRolls[request.RollGuid]
	if s.closed || s.session == nil || request.SessionId != s.state.SessionId || !s.rollVisitBound || s.rollSaturated ||
		request.VisitId != s.rollVisitID || roll == nil || !roll.offer.Available || roll.attempt != nil ||
		roll.offer.Revision != request.ExpectedOfferRevision || roll.offer.VisitId != request.VisitId ||
		roll.offer.LootSlot != request.ExpectedLootSlot || roll.offer.OfferedMask&(1<<choice) == 0 ||
		len(s.rollRequests) >= rollHistoryLimit {
		s.mu.Unlock()
		return nil, fail("current_exact_available_roll_offer_required")
	}
	op := &rollAttempt{request: proto.Clone(request).(*inventoryv1.RollRequest), token: token,
		receipt: &inventoryv1.RollReceipt{RequestId: request.RequestId, RollGuid: request.RollGuid,
			OfferRevision: request.ExpectedOfferRevision, VisitId: request.VisitId, Choice: request.Choice,
			LootSlot: request.ExpectedLootSlot, Phase: "reserved"}}
	roll.attempt, roll.offer.Available, roll.offer.Phase = op, false, "reserved"
	s.rollRequests[request.RequestId] = op
	s.rollRevision++
	s.mu.Unlock()

	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	w, err := s.rollWorld(call)
	s.mu.Lock()
	s.expireRollsLocked()
	if err != nil || call.Err() != nil || s.closed || roll.offer.Phase != "reserved" ||
		w.GetSelf().GetMapId() != roll.offer.MapId || s.rollVisitID != request.VisitId {
		op.receipt.Phase = "not_dispatched"
		s.rollRevision++
		out := s.rollSnapshotLocked()
		s.mu.Unlock()
		return out, nil
	}
	op.receipt.TransportAttempted, op.receipt.Phase = true, "waiting_echo"
	roll.offer.Phase = "voted"
	payload := binary.LittleEndian.AppendUint64(nil, request.RollGuid)
	payload = append(binary.LittleEndian.AppendUint32(payload, roll.offer.LootSlot), choice)
	s.mu.Unlock()
	_, err = s.session.SendPacket(modcb.ForwardPolicySpell(call), &modv1.SendPacketRequest{Opcode: opcode.CMSGLootRoll, Payload: payload})
	s.mu.Lock()
	defer s.mu.Unlock()
	op.receipt.TransportWritten = err == nil
	if err == nil {
		s.state.TransportWrites++
	} else if !op.receipt.ServerVoteObserved && op.receipt.Phase == "waiting_echo" {
		op.receipt.Phase = "transport_unconfirmed_no_retry"
	}
	s.rollRevision++
	return s.rollSnapshotLocked(), nil
}

func (s *Service) reportRollEffectsLocked(ctx context.Context) {
	for _, op := range s.rollRequests {
		if op.token != "" && (op.receipt.Phase == "server_vote_observed" || op.receipt.Phase == "outcome_unconfirmed_no_retry") &&
			modcb.ReportPolicyRollEffect(ctx, op.token, op.receipt) {
			op.token = ""
			return // One effect per trailer. Later packet boundaries drain the rest.
		}
	}
}
