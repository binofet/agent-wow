package buddytraining

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	trainingv1 "github.com/agent-wow/agent-wow/modules/buddy-training/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type sessionAPI interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}
type Service struct {
	trainingv1.UnimplementedBuddyTrainingServer
	mu                     sync.Mutex
	session                sessionAPI
	selfGUID               uint64
	now                    func() time.Time
	state                  *trainingv1.State
	offerUntil, queryUntil time.Time
	queryNPC               uint64
	wait                   chan struct{}
	waitKind               string
	professionLevel        uint32
	professionLevelKnown   bool
	freeSlotsRevision      uint64
}

func NewService(session sessionAPI, sessionID string, selfGUID uint64) *Service {
	return &Service{session: session, selfGUID: selfGUID, now: time.Now, state: &trainingv1.State{SessionId: sessionID, Reason: "explicit_training_only"}}
}
func (s *Service) invoke(ctx context.Context, module, method string, input, output proto.Message) error {
	if s.session == nil {
		return status.Error(codes.FailedPrecondition, "session callback unavailable")
	}
	r, e := anypb.New(input)
	if e != nil {
		return e
	}
	out, e := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: module, Method: method, Request: r})
	if e != nil {
		return e
	}
	if out == nil || out.Result == nil {
		return status.Error(codes.Unavailable, "module returned no result")
	}
	return out.Result.UnmarshalTo(output)
}
func (s *Service) snapshotLocked() *trainingv1.State {
	r := proto.Clone(s.state).(*trainingv1.State)
	if r.Offer != nil {
		r.Offer.ExpiresInMs = 0
		if d := s.offerUntil.Sub(s.now()); d > 0 {
			r.Offer.ExpiresInMs = uint64(d.Milliseconds())
		}
	}
	return r
}
func (s *Service) wallet(ctx context.Context) error {
	w := &tradev1.State{}
	if e := s.invoke(ctx, "buddy-trade", "state", &emptypb.Empty{}, w); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if w.SessionId != s.state.SessionId {
		return status.Error(codes.FailedPrecondition, "wallet belongs to another body session")
	}
	if w.CopperRevision >= s.state.CopperRevision {
		s.state.CopperKnown, s.state.Copper, s.state.CopperRevision = w.CopperKnown, w.Copper, w.CopperRevision
	}
	if p := s.state.LastPurchase; p != nil && w.CopperKnown && w.CopperRevision > p.BaselineCopperRevision && p.CostCopper <= p.BaselineCopper && w.Copper <= p.BaselineCopper-p.CostCopper {
		p.CopperDebitObserved = p.CostCopper > 0
	}
	return nil
}
func (s *Service) GetState(ctx context.Context, _ *emptypb.Empty) (*trainingv1.State, error) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if e := s.wallet(call); e != nil {
		return nil, e
	}
	_ = s.refreshProfessions(call)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(), nil
}
func (s *Service) observations(ctx context.Context) (*worldv1.SnapshotResponse, *actionsv1.State, error) {
	w, a := &worldv1.SnapshotResponse{}, &actionsv1.State{}
	if e := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w); e != nil {
		return nil, nil, e
	}
	if w.Self == nil || w.Self.Guid != s.selfGUID {
		return nil, nil, status.Error(codes.FailedPrecondition, "world belongs to another character")
	}
	s.applyProfessions(w)
	if e := s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a); e != nil {
		return nil, nil, e
	}
	return w, a, nil
}
func positionValid(p *actionsv1.ClientPosition) bool {
	return p != nil && p.Known && p.MapKnown && finite(p.X) && finite(p.Y) && finite(p.Z)
}
func finite(n float32) bool {
	return !math.IsNaN(float64(n)) && !math.IsInf(float64(n), 0) && math.Abs(float64(n)) <= 40000
}
func trainers(w *worldv1.SnapshotResponse) ([]*worldv1.CreatureState, error) {
	if len(w.NearbyCreatures) > 32 || len(w.QuestGivers) > 64 {
		return nil, status.Error(codes.FailedPrecondition, "creature observations exceed bound")
	}
	by := map[uint64]*worldv1.CreatureState{}
	for _, view := range [][]*worldv1.CreatureState{w.NearbyCreatures, w.QuestGivers, {w.OwnerTarget}} {
		for _, c := range view {
			if c == nil || c.NpcFlags&0x10 == 0 {
				continue
			}
			if old := by[c.Guid]; old != nil && !proto.Equal(old, c) {
				return nil, status.Error(codes.FailedPrecondition, "trainer observations disagree")
			}
			by[c.Guid] = c
		}
	}
	rows := make([]*worldv1.CreatureState, 0, len(by))
	for _, c := range by {
		rows = append(rows, c)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Guid < rows[j].Guid })
	return rows, nil
}
func trainerGate(w *worldv1.SnapshotResponse, a *actionsv1.State, c *worldv1.CreatureState) string {
	return typedTrainerGate(w, a, c, 0)
}
func typedTrainerGate(w *worldv1.SnapshotResponse, a *actionsv1.State, c *worldv1.CreatureState, kind uint32) string {
	p := w.Self
	if p == nil || !p.Present || !p.AliveKnown || !p.Alive || !p.HealthKnown || p.Health == 0 || !p.UnitFlagsKnown || p.UnitFlags&0x80000 != 0 || !p.ClassKnown || !p.LevelKnown {
		return "own_alive_out_of_combat_class_and_level_required"
	}
	if !p.MapKnown || !p.MovementFlagsKnown || p.MovementFlags&^uint32(0x1ff) != 0 || p.ExtraMovementFlags != 0 {
		return "ordinary_ground_observation_required"
	}
	if !positionValid(a.Position) || a.Position.MapId != p.MapId || a.MotionUnconfirmed || a.AutoAttackUnconfirmed || a.AutoAttacking || a.CastPending || a.Moving {
		return "confirmed_stopped_position_required"
	}
	flags := uint32(0x30)
	if kind == 2 {
		flags = 0x50
	}
	if c == nil || !creatureGUID(c.Guid) || uint32(c.Guid>>24&0xffffff) != c.Entry || !c.Present || !c.MapKnown || c.MapId != p.MapId || !c.PositionKnown || !finite(c.X) || !finite(c.Y) || !finite(c.Z) || !c.HealthKnown || c.Health == 0 || !c.AliveKnown || !c.Alive || c.NpcFlags&flags != flags || c.UnitFlags&0x80000 != 0 {
		if kind == 2 {
			return "observed_living_profession_trainer_required"
		}
		return "observed_living_class_trainer_required"
	}
	if c.MovementFlags&0x080000ff != 0 || c.Trajectory != nil && c.Trajectory.Known && (!c.Trajectory.Stopped || c.Trajectory.TransportRelative) {
		return "stationary_trainer_required"
	}
	dx, dy, dz := float64(c.X-a.Position.X), float64(c.Y-a.Position.Y), float64(c.Z-a.Position.Z)
	if dx*dx+dy*dy+dz*dz > 16 {
		return "trainer_outside_conservative_range"
	}
	return ""
}
func (s *Service) Discover(ctx context.Context, _ *emptypb.Empty) (*trainingv1.Discovery, error) {
	return s.discover(ctx, 0)
}
func (s *Service) discover(ctx context.Context, kind uint32) (*trainingv1.Discovery, error) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	w, a, e := s.observations(call)
	if e != nil {
		return nil, e
	}
	rows, e := trainers(w)
	if e != nil {
		return nil, e
	}
	if e = s.wallet(call); e != nil {
		return nil, e
	}
	s.mu.Lock()
	out := &trainingv1.Discovery{State: s.snapshotLocked()}
	s.mu.Unlock()
	for _, c := range rows {
		expected := uint32(0x20)
		if kind == 2 {
			expected = 0x40
		}
		if c.NpcFlags&expected == 0 {
			continue
		}
		r := &trainingv1.Trainer{Creature: proto.Clone(c).(*worldv1.CreatureState), BlockedReason: typedTrainerGate(w, a, c, kind)}
		r.Interactable = r.BlockedReason == ""
		if positionValid(a.Position) && c.PositionKnown && c.MapKnown && c.MapId == a.Position.MapId && finite(c.X) && finite(c.Y) && finite(c.Z) {
			r.DistanceKnown = true
			r.Distance = math.Sqrt(math.Pow(float64(c.X-a.Position.X), 2) + math.Pow(float64(c.Y-a.Position.Y), 2) + math.Pow(float64(c.Z-a.Position.Z), 2))
		}
		out.Nearby = append(out.Nearby, r)
	}
	return out, nil
}
func (s *Service) begin(session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session != s.state.SessionId || session == "" {
		return status.Error(codes.FailedPrecondition, "training body session changed")
	}
	if s.state.ActionInFlight {
		return status.Error(codes.FailedPrecondition, "training operation already active")
	}
	s.state.ActionInFlight = true
	return nil
}
func (s *Service) finish() {
	s.mu.Lock()
	s.state.ActionInFlight = false
	s.queryNPC = 0
	s.wait = nil
	s.waitKind = ""
	s.mu.Unlock()
}
func (s *Service) acquire(ctx context.Context) (string, error) {
	l := &actionsv1.InteractionLease{}
	if e := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "training", LeaseMs: 15000}, l); e != nil {
		return "", e
	}
	if l.Token == "" || l.State == nil || l.State.InteractionOwner != "training" || l.State.Moving || l.State.MotionUnconfirmed {
		if l.Token != "" && l.State != nil && l.State.InteractionOwner == "training" {
			s.release(l.Token)
		}
		return "", status.Error(codes.FailedPrecondition, "training lease and confirmed stop required")
	}
	return l.Token, nil
}
func (s *Service) release(token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	_ = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "training", Token: token}, &actionsv1.State{})
}
func (s *Service) gate(ctx context.Context, guid uint64) error {
	return s.typedGate(ctx, guid, 0)
}
func (s *Service) typedGate(ctx context.Context, guid uint64, kind uint32) error {
	w, a, e := s.observations(ctx)
	if e != nil {
		return e
	}
	rows, e := trainers(w)
	if e != nil {
		return e
	}
	var npc *worldv1.CreatureState
	for _, c := range rows {
		if c.Guid == guid {
			npc = c
			break
		}
	}
	if reason := typedTrainerGate(w, a, npc, kind); reason != "" {
		return status.Error(codes.FailedPrecondition, reason)
	}
	return nil
}
func (s *Service) send(ctx context.Context, op uint32, p []byte) error {
	if _, e := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: op, Payload: p}); e != nil {
		return e
	}
	s.mu.Lock()
	s.state.TransportWrites++
	s.mu.Unlock()
	return nil
}
func (s *Service) query(ctx context.Context, guid uint64) error {
	s.mu.Lock()
	s.queryNPC = guid
	s.queryUntil = s.now().Add(1500 * time.Millisecond)
	wait := make(chan struct{})
	s.wait = wait
	s.waitKind = "list"
	s.mu.Unlock()
	if e := s.send(ctx, opcode.CMSGTrainerList, listPacket(guid)); e != nil {
		return e
	}
	t := time.NewTimer(1500 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return status.Error(codes.DeadlineExceeded, "no matching server trainer list")
	case <-wait:
		return nil
	}
}
func (s *Service) List(ctx context.Context, r *trainingv1.ListRequest) (out *trainingv1.State, err error) {
	if r == nil || !creatureGUID(r.NpcGuid) || r.TrainerType != 0 && r.TrainerType != 2 {
		return nil, status.Error(codes.InvalidArgument, "observed trainer GUID required")
	}
	if e := s.begin(r.SessionId); e != nil {
		return nil, e
	}
	defer func() {
		s.finish()
		if out != nil {
			out.ActionInFlight = false
		}
	}()
	call, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	token, e := s.acquire(call)
	if e != nil {
		return nil, e
	}
	defer s.release(token)
	if e = s.typedGate(call, r.NpcGuid, r.TrainerType); e != nil {
		return nil, e
	}
	if e = s.query(call, r.NpcGuid); e != nil {
		return nil, e
	}
	if e = s.wallet(call); e != nil {
		return nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Offer == nil || s.state.Offer.TrainerType != r.TrainerType {
		return nil, status.Error(codes.FailedPrecondition, "server trainer type differs from requested training kind")
	}
	return s.snapshotLocked(), nil
}
func (s *Service) boundBuyLocked(r *trainingv1.BuyRequest) error {
	return s.boundPurchaseLocked(r, nil)
}
func (s *Service) boundPurchaseLocked(r *trainingv1.BuyRequest, profession *trainingv1.ProfessionBuyRequest) error {
	o := s.state.Offer
	kind, points := uint32(0), uint32(0)
	if profession != nil {
		kind = 2
		if primarySkill(profession.SkillId) {
			points = 1
		}
		if e := s.boundProfessionLocked(profession); e != nil {
			return e
		}
	}
	if r.SessionId != s.state.SessionId || o == nil || o.NpcGuid != r.NpcGuid || o.Revision != r.OfferRevision || !s.now().Before(s.offerUntil) || o.TrainerType != kind {
		return status.Error(codes.FailedPrecondition, "trainer offer changed or expired")
	}
	if p := s.state.LastPurchase; p != nil && (p.Status == trainingv1.PurchaseStatus_REQUESTED || p.Status == trainingv1.PurchaseStatus_UNCONFIRMED_TIMEOUT || p.Status == trainingv1.PurchaseStatus_TRAINER_SUCCEEDED) {
		return status.Error(codes.FailedPrecondition, "previous training purchase remains unconfirmed; no retry")
	}
	for _, spell := range o.Spells {
		if spell.SpellId == r.SpellId {
			if spell.Usable != 0 || spell.CostCopper != r.ExpectedCostCopper || spell.CostCopper > r.MaxSpendCopper || len(spell.PointCost) != 2 || spell.PointCost[0] != 0 || spell.PointCost[1] != points {
				return status.Error(codes.FailedPrecondition, "listed training availability, price or points changed")
			}
			if !s.state.CopperKnown || s.state.Copper < spell.CostCopper {
				return status.Error(codes.FailedPrecondition, "known wallet cannot afford listed training")
			}
			return nil
		}
	}
	return status.Error(codes.FailedPrecondition, "spell is absent from actual trainer offer")
}
func (s *Service) Buy(ctx context.Context, r *trainingv1.BuyRequest) (out *trainingv1.State, err error) {
	return s.buy(ctx, r, nil)
}
func (s *Service) buy(ctx context.Context, r *trainingv1.BuyRequest, profession *trainingv1.ProfessionBuyRequest) (out *trainingv1.State, err error) {
	if r == nil || !creatureGUID(r.NpcGuid) || r.SpellId == 0 || r.MaxSpendCopper == 0 || r.MaxSpendCopper > 10000 {
		return nil, status.Error(codes.InvalidArgument, "bound trainer spell and explicit1..10000copper budget required")
	}
	if e := s.begin(r.SessionId); e != nil {
		return nil, e
	}
	defer func() {
		s.finish()
		if out != nil {
			out.ActionInFlight = false
		}
	}()
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := s.wallet(call); e != nil {
		return nil, e
	}
	if profession != nil {
		if e := s.refreshProfessions(call); e != nil {
			return nil, e
		}
	}
	s.mu.Lock()
	e := s.boundPurchaseLocked(r, profession)
	s.mu.Unlock()
	if e != nil {
		return nil, e
	}
	token, e := s.acquire(call)
	if e != nil {
		return nil, e
	}
	defer s.release(token)
	kind := uint32(0)
	if profession != nil {
		kind = 2
	}
	if e = s.typedGate(call, r.NpcGuid, kind); e != nil {
		return nil, e
	}
	if e = s.wallet(call); e != nil {
		return nil, e
	}
	if profession != nil {
		if e = s.refreshProfessions(call); e != nil {
			return nil, e
		}
	}
	if e = call.Err(); e != nil {
		return nil, status.FromContextError(e).Err()
	}
	s.mu.Lock()
	if e = s.boundPurchaseLocked(r, profession); e != nil {
		s.mu.Unlock()
		return nil, e
	}
	id := uint64(1)
	if s.state.LastPurchase != nil {
		id = s.state.LastPurchase.Id + 1
	}
	s.state.LastPurchase = &trainingv1.Purchase{Id: id, NpcGuid: r.NpcGuid, SpellId: r.SpellId, CostCopper: r.ExpectedCostCopper, Status: trainingv1.PurchaseStatus_REQUESTED, BaselineCopper: s.state.Copper, BaselineCopperRevision: s.state.CopperRevision}
	if profession != nil {
		s.state.LastPurchase.ProfessionSkillId = profession.SkillId
		s.state.LastPurchase.BaselineSkillsRevision = s.state.ProfessionsRevision
	}
	s.state.Revision++
	wait := make(chan struct{})
	s.wait = wait
	s.waitKind = "buy"
	s.state.Reason = "training_purchase_requested"
	s.mu.Unlock()
	if e = s.send(call, opcode.CMSGTrainerBuySpell, buyPacket(r.NpcGuid, r.SpellId)); e != nil {
		s.mu.Lock()
		if p := s.state.LastPurchase; !p.ServerSuccessObserved && p.Status == trainingv1.PurchaseStatus_REQUESTED {
			p.Status = trainingv1.PurchaseStatus_UNCONFIRMED_TIMEOUT
			p.Failure = "ambiguous_transport_no_retry"
			s.state.Reason = p.Failure
		}
		out := s.snapshotLocked()
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Lock()
	s.state.LastPurchase.TransportWritten = true
	s.mu.Unlock()
	timer := time.NewTimer(1500 * time.Millisecond)
	select {
	case <-call.Done():
		e = call.Err()
	case <-timer.C:
		e = errors.New("no matching trainer purchase receipt")
	case <-wait:
	}
	timer.Stop()
	s.mu.Lock()
	p := s.state.LastPurchase
	if e != nil && p.Status == trainingv1.PurchaseStatus_REQUESTED {
		p.Status = trainingv1.PurchaseStatus_UNCONFIRMED_TIMEOUT
		p.Failure = "no_server_purchase_confirmation"
		s.state.Reason = p.Failure
	}
	success := p.ServerSuccessObserved
	s.mu.Unlock()
	if success {
		_ = s.query(call, r.NpcGuid)
	}
	_ = s.wallet(call)
	if profession != nil {
		_ = s.refreshProfessions(call)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(), nil
}
func (s *Service) wakeLocked(kind string) {
	if s.wait != nil && s.waitKind == kind {
		close(s.wait)
		s.wait = nil
		s.waitKind = ""
	}
}
func (s *Service) confirmLocked() {
	p := s.state.LastPurchase
	if p != nil && p.ServerSuccessObserved && (p.ProfessionSkillId == 0 && (p.LearnedSpellObserved || p.RefreshedTrainerKnows) || p.ProfessionSkillId != 0 && p.ProfessionSkillObserved && (p.LearnedSpellObserved || p.RefreshedTrainerKnows)) {
		p.Status = trainingv1.PurchaseStatus_TRAINING_CONFIRMED
		p.Failure = ""
		s.state.Reason = "training_confirmed_by_server"
	}
}
func (s *Service) OnPacket(_ context.Context, r *modv1.WorldPacket) (*emptypb.Empty, error) {
	if r == nil {
		return &emptypb.Empty{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	malformed := func() { s.state.MalformedPackets++ }
	switch r.Opcode {
	case opcode.SMSGTrainerList:
		o, e := decodeList(r.Payload)
		if e != nil {
			malformed()
			break
		}
		if o.NpcGuid != s.queryNPC || !s.now().Before(s.queryUntil) {
			s.state.IgnoredPackets++
			break
		}
		annotateProfessions(o)
		s.state.Revision++
		o.Revision = s.state.Revision
		s.state.Offer = o
		s.offerUntil = s.now().Add(20 * time.Second)
		s.queryNPC = 0
		if p := s.state.LastPurchase; p != nil && p.ServerSuccessObserved && p.NpcGuid == o.NpcGuid {
			for _, spell := range o.Spells {
				if spell.SpellId == p.SpellId && spell.Usable == 2 {
					p.RefreshedTrainerKnows = true
				}
			}
			s.confirmLocked()
		}
		s.wakeLocked("list")
	case opcode.SMSGTrainerBuySucceeded, opcode.SMSGTrainerBuyFailed:
		failed := r.Opcode == opcode.SMSGTrainerBuyFailed
		g, id, reason, e := decodeReceipt(r.Payload, failed)
		if e != nil {
			malformed()
			break
		}
		p := s.state.LastPurchase
		if p == nil || p.NpcGuid != g || p.SpellId != id || p.Status == trainingv1.PurchaseStatus_TRAINING_CONFIRMED || p.Status == trainingv1.PurchaseStatus_SERVER_REJECTED {
			s.state.IgnoredPackets++
			break
		}
		if failed {
			p.Status = trainingv1.PurchaseStatus_SERVER_REJECTED
			p.ServerFailure = reason
			p.Failure = "trainer_rejected_purchase"
			s.state.Reason = p.Failure
		} else {
			p.ServerSuccessObserved = true
			p.Status = trainingv1.PurchaseStatus_TRAINER_SUCCEEDED
			s.state.Reason = "trainer_success_learning_unconfirmed"
			s.confirmLocked()
		}
		s.state.Revision++
		s.wakeLocked("buy")
	case opcode.SMSGLearnedSpell, opcode.SMSGSupercededSpell:
		id, e := learnedPacket(r.Payload, r.Opcode == opcode.SMSGSupercededSpell)
		if e != nil {
			malformed()
			break
		}
		p := s.state.LastPurchase
		if p == nil || p.Status == trainingv1.PurchaseStatus_SERVER_REJECTED || p.Status == trainingv1.PurchaseStatus_TRAINING_CONFIRMED {
			break
		}
		found := false
		for _, known := range p.LearnedSpellIds {
			found = found || known == id
		}
		if !found && len(p.LearnedSpellIds) < 16 {
			s.state.Revision++
			p.LearnedSpellIds = append(p.LearnedSpellIds, id)
		}
		if p.SpellId == id {
			p.LearnedSpellObserved = true
		}
		if spec, ok := professionForSkill(p.ProfessionSkillId); ok && spec.learned == id {
			p.LearnedSpellObserved = true
		}
		s.confirmLocked()
	default:
		s.state.IgnoredPackets++
	}
	return &emptypb.Empty{}, nil
}
