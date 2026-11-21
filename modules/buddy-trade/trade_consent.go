package buddytrade

import (
	"context"
	"errors"
	"strconv"
	"time"
	"unicode/utf8"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/protobuf/proto"
)

// One pure policy read binds this exact native window. The existing ACCEPT_SENT
// reservation retains transport uncertainty; this is not another action ledger.
type tradeConsentAdmission struct {
	frame          *tradev1.State
	sourceRevision string
	token          string
	until          time.Time
	deadline       time.Time
	service        tradeServiceItem
	partnerSpell   uint32
	pickLock       tradePickLock
	mapKnown       bool
	mapID          uint32
}

func tradeConsentFrame(state *tradev1.State) (*tradev1.State, map[string]any, error) {
	if state == nil || state.SessionId == "" || len(state.SessionId) > 128 || !utf8.ValidString(state.SessionId) || state.Revision == 0 {
		return nil, nil, errors.New("current_trade_identity_required")
	}
	items := func(rows []*tradev1.Item, limit int, lastSlot uint32) ([]*tradev1.Item, []any, error) {
		if len(rows) > limit {
			return nil, nil, errors.New("bounded_trade_items_required")
		}
		cloned, projected := make([]*tradev1.Item, 0, len(rows)), make([]any, 0, len(rows))
		seen := map[uint32]bool{}
		for _, row := range rows {
			if row == nil || row.Slot > lastSlot || seen[row.Slot] || len(row.Name) > 255 || !utf8.ValidString(row.Name) || len(row.ProtoReflect().GetUnknown()) != 0 {
				return nil, nil, errors.New("exact_unique_trade_item_rows_required")
			}
			seen[row.Slot] = true
			cloned = append(cloned, proto.Clone(row).(*tradev1.Item))
			projected = append(projected, map[string]any{"slot": float64(row.Slot), "entry": float64(row.Entry), "count": float64(row.Count),
				"itemGuid": strconv.FormatUint(row.ItemGuid, 10), "name": row.Name, "metadataKnown": row.MetadataKnown})
		}
		return cloned, projected, nil
	}
	own, ownRows, err := items(state.OwnRequestedItems, 6, 5)
	if err != nil {
		return nil, nil, err
	}
	partner, partnerRows, err := items(state.PartnerItems, 7, 6)
	if err != nil {
		return nil, nil, err
	}
	frame := &tradev1.State{SessionId: state.SessionId, Revision: state.Revision, Phase: state.Phase,
		PartnerGuid: state.PartnerGuid, PartnerAccepted: state.PartnerAccepted, AcceptTransportWritten: state.AcceptTransportWritten,
		OwnRequestedItems: own, OwnRequestedCopper: state.OwnRequestedCopper, PartnerItems: partner, PartnerCopper: state.PartnerCopper}
	view := map[string]any{"sessionId": frame.SessionId, "revision": strconv.FormatUint(frame.Revision, 10), "phase": frame.Phase,
		"partnerGuid": strconv.FormatUint(frame.PartnerGuid, 10), "partnerAccepted": frame.PartnerAccepted,
		"acceptTransportWritten": frame.AcceptTransportWritten, "ownRequestedItems": ownRows,
		"ownRequestedCopper": float64(frame.OwnRequestedCopper), "partnerItems": partnerRows, "partnerCopper": float64(frame.PartnerCopper)}
	return frame, view, nil
}

func (s *Service) tradeConsentWait(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Never replace a server terminal, cleanup or changed-terms explanation.
	if s.state.Phase == "OPEN" && s.state.PartnerAccepted && !s.pendingCancel {
		s.state.Reason = reason
	}
}

func (s *Service) requestTradeConsent(ctx context.Context, before *worldv1.SnapshotResponse) (*tradeConsentAdmission, string) {
	const unavailable = "trade_consent_policy_unavailable"
	if s.session == nil || s.selfGUID == 0 || ctx.Err() != nil {
		return nil, unavailable
	}
	s.mu.Lock()
	frame, view, err := tradeConsentFrame(s.state)
	admission := &tradeConsentAdmission{frame: frame, token: s.token, deadline: s.deadline,
		service: s.serviceItem, partnerSpell: s.partnerSpell, pickLock: s.pickLock,
		mapKnown: before.GetSelf().GetMapKnown(), mapID: before.GetSelf().GetMapId()}
	ready := s.state.Phase == "OPEN" && s.state.PartnerGuid == ownerGUID && s.state.PartnerAccepted &&
		!s.state.AcceptTransportWritten && !s.pendingCancel && s.token != ""
	s.mu.Unlock()
	if err != nil || !ready {
		return nil, "trade_consent_window_changed"
	}
	input := map[string]any{"sessionId": frame.SessionId, "selfGuid": strconv.FormatUint(s.selfGUID, 10), "trade": view}
	available, revision, decision, err := modcb.TradeConsentDecision(ctx, s.session, frame.SessionId, s.selfGUID, input)
	reason, reasonKnown := decision["reason"].(string)
	if err != nil || ctx.Err() != nil || !available || revision == "" || len(decision) != 2 || !reasonKnown ||
		len(reason) == 0 || len(reason) > 128 || !utf8.ValidString(reason) {
		return nil, unavailable
	}
	if decision["kind"] != "accept" {
		if decision["kind"] == "wait" {
			return nil, "trade_consent_wait:" + reason
		}
		return nil, unavailable
	}
	admission.sourceRevision = revision
	return admission, ""
}

// General Trade.acquire historically accepted any nonempty token. Tighten only
// final consent: renewal must prove this same retained token and a live lease.
func (p *tradeConsentAdmission) renew(ctx context.Context, s *Service) error {
	if ctx.Err() != nil || p == nil || p.token == "" || s.token != p.token {
		return errors.New("exact_trade_consent_lease_required")
	}
	started := s.now()
	if !p.until.IsZero() && !started.Before(p.until) {
		return errors.New("trade_consent_lease_already_expired")
	}
	lease := &actionsv1.InteractionLease{}
	if err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "trade", Token: p.token, LeaseMs: 30000}, lease); err != nil {
		return err
	}
	if ctx.Err() != nil || lease.Token != p.token || !junkActionsSafe(lease.State) || lease.State.InteractionOwner != "trade" ||
		lease.State.InteractionLeaseRemainingMs < 3000 || lease.State.InteractionLeaseRemainingMs > 30000 {
		return errors.New("exact_trade_consent_lease_unconfirmed")
	}
	// Include elapsed renewal/final observations in the local expiry bound.
	p.until = started.Add(time.Duration(lease.State.InteractionLeaseRemainingMs) * time.Millisecond)
	if ctx.Err() != nil || !s.now().Before(p.until) {
		return errors.New("trade_consent_renewal_exceeded_lease")
	}
	return nil
}

// Called under mu immediately before the existing native write reservation.
// Every check is local; no policy or dependency read may follow this boundary.
func (p *tradeConsentAdmission) readyLocked(ctx context.Context, s *Service, world *worldv1.SnapshotResponse) bool {
	if p == nil || p.sourceRevision == "" || s.pendingCancel || s.serviceItem != p.service ||
		s.partnerSpell != p.partnerSpell || s.pickLock != p.pickLock || !s.deadline.Equal(p.deadline) {
		return false
	}
	current, _, err := tradeConsentFrame(s.state)
	if err != nil || !proto.Equal(p.frame, current) || world.GetSelf().GetGuid() != s.selfGUID ||
		!world.GetSelf().GetPresent() || !world.GetSelf().GetAliveKnown() || !world.GetSelf().GetAlive() ||
		!p.mapKnown || !world.GetSelf().GetMapKnown() || !world.GetOwner().GetMapKnown() ||
		world.GetSelf().GetMapId() != p.mapID || world.GetOwner().GetMapId() != world.GetSelf().GetMapId() ||
		world.GetSelf().GetUnitFlags()&0x80000 != 0 || world.GetOwner().GetGuid() != ownerGUID ||
		!world.GetOwner().GetPresent() || !world.GetOwner().GetAliveKnown() || !world.GetOwner().GetAlive() || world.GetOwner().GetUnitFlags()&0x80000 != 0 ||
		world.GetSelf().GetMovementFlagsKnown() && world.GetSelf().GetMovementFlags()&0xff != 0 {
		return false
	}
	now := s.now()
	return ctx.Err() == nil && p.token != "" && s.token == p.token && now.Before(p.until) &&
		(p.deadline.IsZero() || now.Before(p.deadline))
}
