package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcast"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const genericCastCapability = "cast_spell"

// CastSpell is the normal learned-spell transport. It does not interpret spell
// effects, select targets or pre-judge the server's gameplay requirements.
func (s *Service) CastSpell(ctx context.Context, r *actionsv1.CastSpellRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "current session, caster and visit required")
	}
	r = proto.Clone(r).(*actionsv1.CastSpellRequest)
	if _, err := spellcast.Encode(0, r); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireSelfChannelLocked()
	s.supportSnapshotLocked(nil)
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current connected caster required")
	}
	if !s.spellbookKnown || !s.knownSpells[r.SpellId] {
		return nil, status.Error(codes.FailedPrecondition, "exact currently learned spell ID required")
	}
	// These are outstanding physical writes, not gameplay readiness estimates.
	if s.state.CastPending {
		return nil, status.Error(codes.FailedPrecondition, "existing cast/channel write must reconcile first")
	}
	if err := s.genericItemBindingLocked(ctx, r); err != nil {
		return nil, err
	}
	target := r.GetTargets().GetObjectGuid()
	w, err := s.readWorldSnapshotRequest(ctx, &worldv1.SnapshotRequest{CreatureGuid: target, IncludeGatheringObjects: true})
	if err != nil || !s.matchesCurrentEncounterVisitLocked(w) || w.VisitRevision != r.VisitId {
		return nil, status.Error(codes.FailedPrecondition, "current caster and observed visit required")
	}
	if w.GetSelf().GetChannelSpellId() != 0 || w.GetSelf().GetChannelObjectGuid() != 0 {
		return nil, status.Error(codes.FailedPrecondition, "observed active channel must end or be explicitly cancelled first")
	}
	if r.GetTrajectory() != nil && len(r.Trajectory.Movement) != 0 && s.state.MotionUnconfirmed {
		return nil, status.Error(codes.FailedPrecondition, "trajectory movement requires reconciled physical movement")
	}
	if target != 0 && !genericObservedTarget(w, target) {
		return nil, status.Error(codes.FailedPrecondition, "explicit unit, object or corpse must be observed in this visit")
	}
	if t := r.Targets; t != nil && t.Mask&0x10 != 0 && !genericOwnedItem(w, t.ItemGuid) {
		return nil, status.Error(codes.FailedPrecondition, "exact currently owned item target required")
	}
	if t := r.Targets; t != nil {
		for _, loc := range []*actionsv1.SpellCastLocation{t.Source, t.Destination} {
			if loc != nil && loc.TransportGuid != 0 && !genericObservedTarget(w, loc.TransportGuid) {
				return nil, status.Error(codes.FailedPrecondition, "target transport must be observed in this visit")
			}
		}
	}
	if _, supplied := modcb.IncomingPolicySpell(ctx); supplied && !s.admitsPolicyEncounterLocked(ctx, w) {
		return nil, status.Error(codes.FailedPrecondition, "current policy encounter access required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.castCount++
	payload, _ := spellcast.Encode(s.castCount, r)
	until := s.now().Add(3 * time.Second)
	// Optional catalog facts improve lifecycle timing; missing semantics never
	// prevent the normal learned-ID packet from being sent.
	spell, channelMetadataKnown := s.learnedContractsLocked()[r.SpellId]
	if spell.CastMs > 0 {
		until = until.Add(time.Duration(spell.CastMs) * time.Millisecond)
	}
	op := &supportAttempt{view: actionsv1.SpellOperation{SpellId: r.SpellId, TargetGuid: target, Capability: genericCastCapability, Phase: "waiting_server"}, count: s.castCount, until: until, genericSpell: true, genericChannelUnknown: !channelMetadataKnown, policySpellToken: modcb.PolicySpellToken(ctx)}
	s.initializeSpellLifecycleLocked(op, isChanneledSpell(spell), r.VisitId)
	s.supportOp = op
	s.pendingSpell = r.SpellId
	s.pendingTarget = target
	s.castUntil = until
	s.state.CastPending = true
	s.state.LastCastSpellId = r.SpellId
	s.state.LastCastTargetGuid = target
	s.state.LastCastFailure = 0
	err = s.sendPacketLocked(modcb.ForwardPolicySpell(ctx), opcode.CMSGCastSpell, payload)
	if modcb.PacketWasNotDispatched(err) {
		op.view.Phase = "not_dispatched"
		s.state.CastPending = false
		s.pendingSpell = 0
		s.pendingTarget = 0
		op.policySpellToken = ""
		return nil, err
	}
	if err != nil {
		op.view.Phase = "transport_unconfirmed_no_retry"
	} else {
		op.view.TransportWritten = true
	}
	return s.stateLocked(), nil
}

func genericObservedTarget(w *worldv1.SnapshotResponse, guid uint64) bool {
	if observedSelectionTarget(w, guid, w.GetSelf().GetMapId()) {
		return true
	}
	for _, rows := range [][]*worldv1.GameObjectState{w.GetRequestedGameObjects(), w.GetNearbyGatheringObjects(), w.GetQuestObjects(), w.GetMailboxes(), w.GetSpellFocuses(), w.GetOwnFishingBobbers()} {
		for _, o := range rows {
			if o.GetGuid() == guid && o.GetPresent() && o.GetMapKnown() && o.GetMapId() == w.GetSelf().GetMapId() {
				return true
			}
		}
	}
	return false
}
func genericOwnedItem(w *worldv1.SnapshotResponse, guid uint64) bool {
	if guid == 0 {
		return false
	}
	if w.GetInventoryKnown() {
		for _, bag := range w.InventoryBags {
			for _, item := range bag.Slots {
				if item.ItemGuid == guid {
					return true
				}
			}
		}
	}
	if w.GetBackpackKnown() {
		for _, item := range w.Backpack {
			if item.ItemGuid == guid {
				return true
			}
		}
	}
	if w.GetSelfEquipmentKnown() {
		for _, item := range w.SelfEquipment {
			if item.SlotKnown && item.ItemGuid == guid {
				return true
			}
		}
	}
	return false
}
func (s *Service) genericItemBindingLocked(ctx context.Context, r *actionsv1.CastSpellRequest) error {
	t := r.Targets
	if t == nil || t.Mask&0x1000 == 0 {
		return nil
	}
	if t.TradeRevision == 0 || t.TradePartnerGuid == 0 {
		return status.Error(codes.InvalidArgument, "exact trade revision and partner required for slot target")
	}
	request, _ := anypb.New(&emptypb.Empty{})
	reply, err := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: "buddy-trade", Method: "state", Request: request})
	st := &tradev1.State{}
	if err != nil || reply.GetResult() == nil || reply.GetResult().UnmarshalTo(st) != nil || st.SessionId != r.SessionId || st.Revision != t.TradeRevision || st.PartnerGuid != t.TradePartnerGuid || st.Phase != "OPEN" {
		return status.Error(codes.FailedPrecondition, "current open trade and exact partner/revision required")
	}
	return nil
}

// Generic START/GO correlation establishes execution only, never an effect.
// Area spells may hit zero/many units and normalize their explicit targets.
func (s *Service) genericCastEventLocked(op *supportAttempt, code uint32, p []byte, off int) error {
	source, _, err := readGUID(p)
	if err != nil || source != s.selfGUID {
		return errors.New("generic cast source mismatch")
	}
	pos := off + 8
	if pos > len(p) {
		return errors.New("truncated generic cast event")
	}
	if code == opcode.SMSGSpellGo {
		if pos == len(p) {
			return errors.New("missing spell hit list")
		}
		n := int(p[pos])
		pos++
		if n > (len(p)-pos)/8 {
			return errors.New("truncated hit list")
		}
		pos += n * 8
		if pos == len(p) {
			return errors.New("missing miss list")
		}
		n = int(p[pos])
		pos++
		for range n {
			if len(p)-pos < 9 {
				return errors.New("truncated miss list")
			}
			reason := p[pos+8]
			pos += 9
			if reason == 11 {
				if pos == len(p) {
					return errors.New("truncated reflect")
				}
				pos++
			}
		}
	}
	if _, err := spellcast.TargetPrefixLen(p[pos:]); err != nil {
		return err
	}
	if code == opcode.SMSGSpellStart {
		duration := binary.LittleEndian.Uint32(p[off+4:])
		op.until = s.now().Add(time.Duration(duration)*time.Millisecond + 3*time.Second)
		s.castUntil = op.until
		return nil
	}
	op.view.ServerCastGo = true
	if op.selfChannel {
		if op.view.ChannelEnded {
			s.finishSelfChannelLocked("channel_ended_effect_unverified")
		}
		return nil
	}
	if op.genericChannelUnknown {
		// GO precedes CHANNEL_START on the ordinary server path. Without
		// channel metadata it cannot establish that physical casting ended.
		// Keep the existing bounded operation until actual lifecycle evidence
		// or its original uncertainty deadline; never replay the learned cast.
		op.view.Phase = "waiting_channel_observation"
		return nil
	}
	op.view.Phase = "server_cast_go_effect_unverified"
	s.state.CastPending = false
	s.pendingSpell = 0
	s.pendingTarget = 0
	return nil
}
