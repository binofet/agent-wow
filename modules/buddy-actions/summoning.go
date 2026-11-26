package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type summoningState struct {
	visitScope, offerScope             *modv1.TransportScope
	useRevision                        uint64
	lastUse                            *actionsv1.SummoningObjectUse
	offer                              *actionsv1.SummonOffer
	expires                            time.Time
	selectionAttempted, selectionKnown bool
	selectedGUID                       uint64
}

func (s *Service) summonPartyLocked(ctx context.Context, groupGUID uint64) map[uint64]bool {
	party := s.readPartyStateLocked(ctx)
	if groupGUID == 0 || party.GetSessionId() != s.sessionID || party.GetGroup().GetGroupGuid() != groupGUID {
		return nil
	}
	return s.onlineHealingGroup(party.Group)
}

func (s *Service) summonVisitCurrent(w *worldv1.SnapshotResponse, visit uint64) bool {
	scope := s.summoning.visitScope
	return s.matchesCurrentEncounterVisitLocked(w) && visit == w.VisitRevision && scope.GetVisitBound() &&
		scope.GetConnectionId() != "" && scope.GetVisitRevision() != 0 && scope.SelfGuid == s.selfGUID && scope.MapId == w.Self.MapId
}

func (s *Service) observeSummonLocked(p *modv1.WorldPacket) {
	state := &s.summoning
	if state.offer != nil {
		state.offer.Pending = false
	}
	scope := p.TransportScope
	if p.Opcode == opcode.SMSGSummonCancel {
		return
	}
	if len(p.Payload) != 16 || scope.GetSummonOfferSequence() == 0 || !s.encounterVisit.bound || !samePortalCoreVisit(state.visitScope, scope) {
		s.state.MalformedPackets++
		return
	}
	caster := binary.LittleEndian.Uint64(p.Payload)
	timeout := binary.LittleEndian.Uint32(p.Payload[12:])
	if caster == 0 || caster>>48 != 0 || caster == s.selfGUID || timeout == 0 {
		s.state.MalformedPackets++
		return
	}
	state.offer = &actionsv1.SummonOffer{Sequence: scope.SummonOfferSequence, VisitId: s.encounterVisit.revision,
		SummonerGuid: caster, ZoneId: binary.LittleEndian.Uint32(p.Payload[8:]), TimeoutMs: timeout, Pending: true}
	state.offerScope = clonePortalScope(scope)
	state.expires = s.now().Add(time.Duration(timeout) * time.Millisecond)
}

func (s *Service) summoningObservationLocked() *actionsv1.SummoningObservation {
	state := &s.summoning
	out := &actionsv1.SummoningObservation{SessionId: s.sessionID, SelfGuid: s.selfGUID, UseRevision: state.useRevision}
	if !s.closed && s.encounterVisit.bound {
		out.VisitId = s.encounterVisit.revision
	}
	if state.lastUse != nil {
		out.LastUse = proto.Clone(state.lastUse).(*actionsv1.SummoningObjectUse)
	}
	if state.offer != nil {
		out.Offer = proto.Clone(state.offer).(*actionsv1.SummonOffer)
		out.Offer.Pending = out.Offer.Pending && out.VisitId == out.Offer.VisitId && s.now().Before(state.expires)
	}
	return out
}

func (s *Service) ObserveSummoning(ctx context.Context, _ *emptypb.Empty) (*actionsv1.SummoningObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summoningObservationLocked(), nil
}

func (s *Service) RespondToSummon(ctx context.Context, r *actionsv1.RespondToSummonRequest) (*actionsv1.SummoningObservation, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 || r.GetOfferSequence() == 0 || r.GetSummonerGuid() == 0 || r.Accept == nil {
		return nil, status.Error(codes.InvalidArgument, "body, visit, exact summon offer and explicit acceptance decision required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	offer := s.summoning.offer
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || r.VisitId != offer.GetVisitId() ||
		r.OfferSequence != offer.GetSequence() || r.SummonerGuid != offer.GetSummonerGuid() {
		return nil, status.Error(codes.FailedPrecondition, "current summon recipient and exact offer required")
	}
	if offer.RequestedAccept != nil && *offer.RequestedAccept == *r.Accept {
		return s.summoningObservationLocked(), nil
	}
	if r.ExpectedGroupGuid != 0 && !s.summonPartyLocked(ctx, r.ExpectedGroupGuid)[r.SummonerGuid] {
		return nil, status.Error(codes.FailedPrecondition, "summoner must remain online in the selected group")
	}
	w, err := s.readWorldSnapshot(ctx)
	self := w.GetSelf()
	if err != nil || !s.summonVisitCurrent(w, r.VisitId) || !offer.Pending || !s.now().Before(s.summoning.expires) ||
		!self.GetAliveKnown() || !self.GetAlive() || !self.GetUnitFlagsKnown() || self.UnitFlags&0x00080000 != 0 {
		return nil, status.Error(codes.FailedPrecondition, "current unexpired offer to a living recipient out of combat required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	payload := binary.LittleEndian.AppendUint64(nil, r.SummonerGuid)
	agree := byte(0)
	if *r.Accept {
		agree = 1
	}
	offer.Pending, offer.RequestedAccept = false, proto.Bool(*r.Accept)
	err = s.sendPacketLocked(modcb.ForwardPolicySpell(ctx), opcode.CMSGSummonResponse, append(payload, agree), s.summoning.offerScope)
	if modcb.PacketWasNotDispatched(err) {
		offer.Pending, offer.RequestedAccept = true, nil
		return nil, err
	}
	offer.TransportWritten, offer.TransportUnknown = err == nil, err != nil
	return s.summoningObservationLocked(), nil
}

func (s *Service) UseSummoningObject(ctx context.Context, r *actionsv1.UseSummoningObjectRequest) (*actionsv1.SummoningObservation, error) {
	guid := r.GetObjectGuid()
	entry := uint32(guid >> 24 & 0xffffff)
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 || r.GetExpectedGroupGuid() == 0 ||
		guid>>48 != 0xf110 || entry == 0 || guid&0xffffff == 0 {
		return nil, status.Error(codes.InvalidArgument, "current body, visit, group and exact game object GUID required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := &s.summoning
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || !s.encounterVisit.bound || r.VisitId != s.encounterVisit.revision {
		return nil, status.Error(codes.FailedPrecondition, "current summoning body and visit required")
	}
	if proto.Equal(state.lastUse.GetRequest(), r) {
		return s.summoningObservationLocked(), nil
	}
	if r.ExpectedUseRevision != state.useRevision || state.useRevision == math.MaxUint64 {
		return nil, status.Error(codes.FailedPrecondition, "summoning object use revision changed")
	}
	group := s.summonPartyLocked(ctx, r.ExpectedGroupGuid)
	if !group[s.selfGUID] || r.ExpectedCreatorGuid != 0 && !group[r.ExpectedCreatorGuid] {
		return nil, status.Error(codes.FailedPrecondition, "current group and summoning object creator required")
	}
	w, err := s.readWorldSnapshotRequest(ctx, &worldv1.SnapshotRequest{GameObjectEntry: entry})
	if err != nil || !s.summonVisitCurrent(w, r.VisitId) || !w.Self.AliveKnown || !w.Self.Alive || !w.Self.PositionKnown ||
		s.state.MotionUnconfirmed || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "current living body and position required")
	}
	var object *worldv1.GameObjectState
	for _, candidate := range w.RequestedGameObjects {
		if candidate.GetGuid() == guid && candidate.Entry == entry {
			object = candidate
			break
		}
	}
	if !object.GetPresent() || !object.MapKnown || object.MapId != w.Self.MapId || !object.TypeKnown ||
		!object.FlagsKnown || object.Flags&(0x01|0x02|0x10) != 0 || !object.CreatedByKnown || object.CreatedByGuid != r.ExpectedCreatorGuid ||
		!object.PositionKnown || !(distance(s.pos, position{x: object.X, y: object.Y, z: object.Z}) <= 5) {
		return nil, status.Error(codes.FailedPrecondition, "exact nearby selectable summoning object and creator required")
	}
	switch object.GameObjectType {
	case 18:
		if object.CreatedByGuid == s.selfGUID || r.SelectedMemberGuid != 0 {
			return nil, status.Error(codes.FailedPrecondition, "assist another player's ritual without a target selection")
		}
		if r.ExpectedCreatorTargetGuid != 0 && (!group[r.ExpectedCreatorTargetGuid] || object.GetCreatorSelectedTargetGuid() != r.ExpectedCreatorTargetGuid) {
			return nil, status.Error(codes.FailedPrecondition, "visible ritual creator must still select the exact group member")
		}
	case 23:
		if r.ExpectedCreatorTargetGuid != 0 || r.SelectedMemberGuid == 0 || r.SelectedMemberGuid == s.selfGUID || !group[r.SelectedMemberGuid] ||
			!w.Self.TargetKnown || w.Self.TargetGuid != r.SelectedMemberGuid ||
			state.selectionAttempted && (!state.selectionKnown || state.selectedGUID != r.SelectedMemberGuid) {
			return nil, status.Error(codes.FailedPrecondition, "select the exact online group member before using the stone")
		}
	default:
		return nil, status.Error(codes.FailedPrecondition, "a summoning ritual or meeting stone required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	use := &actionsv1.SummoningObjectUse{Request: proto.Clone(r).(*actionsv1.UseSummoningObjectRequest)}
	scope := clonePortalScope(state.visitScope)
	scope.MeetingStoneTargetGuid = r.SelectedMemberGuid
	err = s.sendPacketLocked(modcb.ForwardPolicySpell(ctx), opcode.CMSGGameobjUse, binary.LittleEndian.AppendUint64(nil, guid), scope)
	if modcb.PacketWasNotDispatched(err) {
		return nil, err
	}
	use.TransportWritten, use.TransportUnknown = err == nil, err != nil
	state.useRevision++
	state.lastUse = use
	return s.summoningObservationLocked(), nil
}
