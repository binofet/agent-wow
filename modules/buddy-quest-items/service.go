package buddyquestitems

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	qv1 "github.com/agent-wow/agent-wow/modules/buddy-quest-items/api"
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
type operation struct {
	request        *qv1.AttemptRequest
	receipt        *qv1.Operation
	baseline       view
	generation     uint64
	targetRevision uint64
	target         point
	token          string
	started        time.Time
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	reserved       bool
}
type Service struct {
	qv1.UnimplementedBuddyQuestItemsServer
	mu                     sync.Mutex
	observeGate            chan struct{}
	session                sessionAPI
	self                   uint64
	now                    func() time.Time
	timeout                time.Duration
	state                  *qv1.State
	view                   view
	viewKnown              bool
	generation             uint64
	active, last           *operation
	requests               map[string]*operation
	bindings               map[string]bool
	castCount              byte
	closed                 bool
	queryGO, queryItem     int
	queryGOAt, queryItemAt time.Time
}

func NewService(session sessionAPI, sessionID string, self uint64) *Service {
	return &Service{session: session, self: self, now: time.Now, timeout: 10 * time.Second, observeGate: make(chan struct{}, 1), requests: map[string]*operation{}, bindings: map[string]bool{}, state: &qv1.State{SessionId: sessionID, SelfGuid: self, Revision: 1, QuestId: questID, RequiredCount: requiredCount, Limitation: limitation}}
}
func (s *Service) invoke(ctx context.Context, module, method string, in, out proto.Message) error {
	if s.session == nil {
		return errors.New("session unavailable")
	}
	p, err := anypb.New(in)
	if err != nil {
		return err
	}
	r, err := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: module, Method: method, Request: p})
	if err != nil {
		return err
	}
	if r == nil || r.Result == nil {
		return errors.New("missing dependency result")
	}
	return r.Result.UnmarshalTo(out)
}
func (s *Service) send(ctx context.Context, op uint32, p []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed || s.session == nil {
		return errors.New("session closing or unavailable")
	}
	_, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: op, Payload: p})
	if err == nil {
		s.mu.Lock()
		s.state.TransportWrites++
		s.mu.Unlock()
	}
	return err
}
func (s *Service) snapshotLocked() *qv1.State {
	r := proto.Clone(s.state).(*qv1.State)
	r.Active = s.active != nil
	r.Closing = s.closed
	if s.last != nil {
		r.Operation = proto.Clone(s.last.receipt).(*qv1.Operation)
		age := s.now().Sub(s.last.started)
		if age > 0 {
			r.Operation.AgeMs = uint64(age.Milliseconds())
		}
	}
	return r
}
func (s *Service) observe(ctx context.Context) error {
	select {
	case s.observeGate <- struct{}{}:
		defer func() { <-s.observeGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	generation := s.generation
	s.mu.Unlock()
	w, a := &worldv1.SnapshotResponse{}, &actionsv1.State{}
	err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{CreatureEntry: fireEntry, GameObjectEntry: barrelEntry}, w)
	if err == nil {
		err = s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a)
	}
	var v view
	if err == nil {
		v, err = ownView(w, a, s.self)
	}
	s.mu.Lock()
	if generation != s.generation {
		err = errors.New("world changed during observation")
	}
	if err == nil && s.viewKnown && (w.InventoryRevision < s.state.InventoryRevision || w.SelfQuestLogRevision < s.state.QuestLogRevision) {
		err = errors.New("world observation revisions regressed")
	}
	s.viewKnown = err == nil
	s.state.Ready = false
	s.state.Barrels = nil
	s.state.Fires = nil
	s.state.Bucket = nil
	var queries []struct {
		op uint32
		p  []byte
	}
	if err != nil {
		s.state.QuestLogKnown = false
		s.state.InventoryKnown = false
		s.state.Reason = "complete_own_observations_unavailable"
	} else {
		s.view = v
		s.state.QuestLogKnown = true
		s.state.QuestLogRevision = w.SelfQuestLogRevision
		s.state.InventoryKnown = true
		s.state.InventoryRevision = w.InventoryRevision
		s.state.QuestActive = v.quest != nil
		s.state.QuestComplete = v.quest != nil && v.quest.Complete
		s.state.ObjectiveCount = v.count
		s.state.Bucket = v.bucket
		for _, g := range w.RequestedGameObjects {
			if g != nil && barrel(v, g.Guid) != nil {
				s.state.Barrels = append(s.state.Barrels, proto.Clone(g).(*worldv1.GameObjectState))
			}
		}
		for _, c := range w.RequestedCreatures {
			if c != nil && fire(v, c.Guid) != nil {
				s.state.Fires = append(s.state.Fires, proto.Clone(c).(*worldv1.CreatureState))
			}
		}
		s.state.Reason = safe(v)
		if s.state.Reason == "" {
			if v.quest == nil || v.quest.Failed {
				s.state.Reason = "own_active_quest_required"
			} else if v.quest.Complete || v.count >= requiredCount {
				s.state.Reason = "own_quest_complete"
			} else if s.active != nil {
				s.state.Reason = "quest_item_operation_active"
			} else if v.bucket == nil && !s.state.BarrelTemplateKnown || v.bucket != nil && !s.state.BucketTemplateKnown {
				s.state.Reason = "waiting_for_normal_server_template"
			} else {
				s.state.Ready = true
				s.state.Reason = "ready_for_bound_quest_item_attempt"
			}
		}
		if v.quest != nil && !s.closed {
			if !s.state.BarrelTemplateKnown && len(s.state.Barrels) > 0 && s.queryGO < 2 && (s.queryGO == 0 || s.now().Sub(s.queryGOAt) >= 5*time.Second) {
				s.queryGO++
				s.queryGOAt = s.now()
				queries = append(queries, struct {
					op uint32
					p  []byte
				}{opcode.CMSGGameobjectQuery, goQuery(s.state.Barrels[0].Guid)})
			}
			if v.bucket != nil && !s.state.BucketTemplateKnown && s.queryItem < 2 && (s.queryItem == 0 || s.now().Sub(s.queryItemAt) >= 5*time.Second) {
				s.queryItem++
				s.queryItemAt = s.now()
				queries = append(queries, struct {
					op uint32
					p  []byte
				}{opcode.CMSGItemQuerySingle, u32(bucketEntry)})
			}
		}
		s.evidenceLocked(v)
	}
	s.state.Revision++
	s.mu.Unlock()
	for _, q := range queries {
		_ = s.send(ctx, q.op, q.p)
	}
	return err
}
func (s *Service) GetState(ctx context.Context, _ *emptypb.Empty) (*qv1.State, error) {
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = s.observe(call)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(), nil
}
func (s *Service) evidenceLocked(v view) {
	op := s.active
	if op == nil || !op.reserved || op.generation != s.generation {
		return
	}
	r := op.receipt
	r.ObservedInventoryRevision = v.world.InventoryRevision
	r.ObservedQuestLogRevision = v.world.SelfQuestLogRevision
	r.AfterObjectiveCount = v.count
	if v.quest != nil && v.world.SelfQuestLogRevision > r.BaselineQuestLogRevision {
		r.QuestCreditObserved = v.count > r.BeforeObjectiveCount
		r.QuestCompleteObserved = v.quest.Complete
	}
	if v.world.InventoryRevision > r.BaselineInventoryRevision {
		if r.Action == qv1.Action_FILL_BUCKET {
			r.InventoryAddedObserved = v.bucket != nil && op.baseline.bucket == nil
		} else {
			old := r.Item
			current := v.items[old.ItemGuid]
			r.InventoryConsumedObserved = current == nil || current.Entry == old.Entry && current.Count < old.Count
		}
	}
	if terminal(r.Status) {
		return
	}
	if r.Action == qv1.Action_FILL_BUCKET && r.InventoryAddedObserved {
		r.Status = qv1.Status_COMPLETED
		r.Reason = "water_bucket_observed_in_own_inventory"
	}
	if r.Action == qv1.Action_THROW_BUCKET && r.OwnCastGoObserved && r.QuestCreditObserved {
		r.Status = qv1.Status_COMPLETED
		r.Reason = "individual_quest_credit_observed"
	}
}
func terminal(st qv1.Status) bool {
	return st != qv1.Status_RESERVED && st != qv1.Status_WAITING_SERVER
}

var requestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (s *Service) gateLocked(r *qv1.AttemptRequest) (point, uint64, *qv1.ItemBinding, error) {
	bad := func(reason string) (point, uint64, *qv1.ItemBinding, error) {
		return point{}, 0, nil, status.Error(codes.FailedPrecondition, reason)
	}
	if !s.viewKnown {
		return bad("complete own observations required")
	}
	v := s.view
	if why := safe(v); why != "" {
		return bad(why)
	}
	if v.quest == nil || v.quest.Failed || v.quest.Complete || v.count >= requiredCount {
		return bad("own incomplete Fire Brigade Practice required")
	}
	if r.ExpectedQuestLogRevision != v.world.SelfQuestLogRevision || r.ExpectedInventoryRevision != v.world.InventoryRevision {
		return bad("own quest or inventory revision changed")
	}
	switch r.Action {
	case qv1.Action_FILL_BUCKET:
		if r.TargetCreatureGuid != 0 || r.ExpectedItemGuid != 0 || v.bucket != nil || v.free == 0 || !s.state.BarrelTemplateKnown {
			return bad("known barrel template, empty inventory slot and no existing bucket required")
		}
		g := barrel(v, r.SourceObjectGuid)
		if g == nil || !rangeOK(v, point{g.X, g.Y, g.Z}, 4.5) {
			return bad("actual usable barrel within4.5yd required")
		}
		return point{g.X, g.Y, g.Z}, g.PositionRevision, nil, nil
	case qv1.Action_THROW_BUCKET:
		if r.SourceObjectGuid != 0 || !s.state.BucketTemplateKnown || v.bucket == nil || v.bucket.ItemGuid != r.ExpectedItemGuid {
			return bad("exact owned bucket and normal on-use template required")
		}
		c := fire(v, r.TargetCreatureGuid)
		if c == nil || !rangeOK(v, point{c.X, c.Y, c.Z}, 28) {
			return bad("actual stationary practice fire within28yd required")
		}
		return point{c.X, c.Y, c.Z}, c.PositionRevision, v.bucket, nil
	}
	return bad("unsupported quest-item action")
}
func (s *Service) Attempt(ctx context.Context, r *qv1.AttemptRequest) (*qv1.State, error) {
	if r == nil || r.SessionId == "" || !requestPattern.MatchString(r.RequestId) || r.QuestId != questID || r.Action != qv1.Action_FILL_BUCKET && r.Action != qv1.Action_THROW_BUCKET {
		return nil, status.Error(codes.InvalidArgument, "bound session, request ID and supported quest action required")
	}
	r = proto.Clone(r).(*qv1.AttemptRequest)
	s.mu.Lock()
	if r.SessionId != s.state.SessionId || s.closed {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "quest-item session changed or closing")
	}
	if old := s.requests[r.RequestId]; old != nil {
		if !proto.Equal(old.request, r) {
			s.mu.Unlock()
			return nil, status.Error(codes.InvalidArgument, "request ID already bound to different parameters")
		}
		out := s.snapshotLocked()
		out.Operation = proto.Clone(old.receipt).(*qv1.Operation)
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.observe(call); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "complete own observations unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || r.SessionId != s.state.SessionId || s.active != nil {
		return nil, status.Error(codes.FailedPrecondition, "quest-item operation unavailable or busy")
	}
	if old := s.requests[r.RequestId]; old != nil {
		return nil, status.Error(codes.FailedPrecondition, "request already reserved")
	}
	if len(s.requests) >= 128 {
		return nil, status.Error(codes.ResourceExhausted, "quest-item body request capacity reached")
	}
	target, revision, item, err := s.gateLocked(r)
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	opctx, stop := context.WithTimeout(context.Background(), s.timeout)
	receipt := &qv1.Operation{OperationId: hex.EncodeToString(nonce[:]), RequestId: r.RequestId, Action: r.Action, Status: qv1.Status_RESERVED, Reason: "reserving_quest_interaction", QuestId: questID, SourceObjectGuid: r.SourceObjectGuid, TargetCreatureGuid: r.TargetCreatureGuid, Item: item, BeforeObjectiveCount: s.view.count, AfterObjectiveCount: s.view.count, BaselineQuestLogRevision: r.ExpectedQuestLogRevision, BaselineInventoryRevision: r.ExpectedInventoryRevision}
	op := &operation{request: r, receipt: receipt, baseline: s.view, generation: s.generation, targetRevision: revision, target: target, started: s.now(), ctx: opctx, cancel: stop, done: make(chan struct{})}
	s.requests[r.RequestId] = op
	s.active, s.last = op, op
	s.state.Revision++
	go s.run(op)
	return s.snapshotLocked(), nil
}
func (s *Service) lease(ctx context.Context, token string) (string, error) {
	r := &actionsv1.InteractionLease{}
	err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "quests", Token: token, LeaseMs: 15000}, r)
	if err != nil {
		return "", err
	}
	if r.Token == "" || len(r.Token) > 128 || token != "" && r.Token != token || r.State == nil || r.State.InteractionOwner != "quests" || r.State.InteractionLeaseRemainingMs < 1000 || r.State.Moving || r.State.MotionUnconfirmed || r.State.CastPending || r.State.AutoAttackUnconfirmed || r.State.AutoAttacking {
		return r.Token, errors.New("quest interaction stop or lease unconfirmed")
	}
	return r.Token, nil
}
func (s *Service) block(op *operation, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !terminal(op.receipt.Status) {
		if op.receipt.CancelRequested {
			op.receipt.Status = qv1.Status_CANCELLED
			op.receipt.Reason = "cancelled_before_quest_item_write"
		} else {
			op.receipt.Status = qv1.Status_BLOCKED
			op.receipt.Reason = reason
		}
	}
}
func binding(r *qv1.AttemptRequest) string {
	return fmt.Sprintf("%d/%d/%d/%d/%d/%d", r.Action, r.ExpectedQuestLogRevision, r.ExpectedInventoryRevision, r.SourceObjectGuid, r.TargetCreatureGuid, r.ExpectedItemGuid)
}
func (s *Service) run(op *operation) {
	defer func() {
		op.cancel()
		released := false
		if op.token != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			err := s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "quests", Token: op.token}, &actionsv1.State{})
			cancel()
			released = err == nil
		}
		s.mu.Lock()
		op.receipt.LeaseReleased = released
		op.receipt.CleanupUnconfirmed = op.token != "" && !released
		if !terminal(op.receipt.Status) {
			if op.receipt.CancelRequested {
				op.receipt.Status = qv1.Status_CANCELLED
				op.receipt.Reason = "cancelled_no_more_quest_item_writes"
			} else if op.generation == s.generation && op.receipt.Action == qv1.Action_THROW_BUCKET && op.receipt.OwnCastGoObserved && op.receipt.InventoryConsumedObserved && s.viewKnown && s.view.quest != nil && !s.view.quest.Failed && s.view.world.SelfQuestLogRevision >= op.receipt.BaselineQuestLogRevision {
				op.receipt.Status = qv1.Status_NO_CREDIT_OBSERVED
				op.receipt.Reason = "bucket_cast_and_consumption_observed_without_individual_credit"
			} else {
				op.receipt.Status = qv1.Status_OUTCOME_UNCONFIRMED
				op.receipt.Reason = "quest_item_outcome_unconfirmed_no_blind_retry"
			}
		}
		if s.active == op {
			s.active = nil
		}
		s.state.Revision++
		close(op.done)
		s.mu.Unlock()
	}()
	token, err := s.lease(op.ctx, "")
	op.token = token
	if err != nil {
		s.block(op, "quest_interaction_unavailable")
		return
	}
	if err = s.observe(op.ctx); err != nil {
		s.block(op, "fresh_own_observations_unavailable")
		return
	}
	// Revalidate the opaque owned token before the final fresh state read. An
	// unrelated replacement interaction cannot authorize this reserved attempt.
	if _, err = s.lease(op.ctx, token); err != nil {
		s.block(op, "quest_interaction_lease_changed")
		return
	}
	if err = s.observe(op.ctx); err != nil {
		s.block(op, "fresh_own_observations_unavailable")
		return
	}
	s.mu.Lock()
	target, rev, item, err := s.gateLocked(op.request)
	if err == nil && (s.closed || s.active != op || op.generation != s.generation || s.view.actions.Moving || s.view.actions.InteractionOwner != "quests" || s.view.actions.InteractionLeaseRemainingMs < 1000 || s.view.world.Self.MapId != op.baseline.world.Self.MapId || s.view.world.Self.PositionRevision != op.baseline.world.Self.PositionRevision || !samePosition(s.view.actions.Position, op.baseline.actions.Position) || target != op.target || rev != op.targetRevision) {
		err = errors.New("body, position, lease or target observation changed")
	}
	if err == nil && s.bindings[binding(op.request)] {
		err = errors.New("quest-item binding already written; fresh evidence required")
	}
	if err == nil {
		err = op.ctx.Err()
	}
	if err != nil {
		if op.receipt.CancelRequested {
			op.receipt.Status = qv1.Status_CANCELLED
			op.receipt.Reason = "cancelled_before_quest_item_write"
		} else {
			op.receipt.Status = qv1.Status_BLOCKED
			op.receipt.Reason = "fresh_binding_rejected"
		}
		s.mu.Unlock()
		return
	}
	var payload []byte
	packet := uint32(opcode.CMSGGameobjUse)
	if op.request.Action == qv1.Action_FILL_BUCKET {
		payload = goUse(op.request.SourceObjectGuid)
		op.receipt.SpellId = createSpell
	} else {
		s.castCount++
		op.receipt.CastCount = uint32(s.castCount)
		op.receipt.SpellId = throwSpell
		op.receipt.Item = proto.Clone(item).(*qv1.ItemBinding)
		payload, err = useBucket(item.Bag, item.Slot, item.ItemGuid, s.castCount, target)
		packet = opcode.CMSGUseItem
	}
	if err != nil {
		op.receipt.Status = qv1.Status_BLOCKED
		op.receipt.Reason = "invalid_item_wire_binding"
		s.mu.Unlock()
		return
	}
	s.bindings[binding(op.request)] = true
	op.reserved = true
	op.receipt.Status = qv1.Status_WAITING_SERVER
	op.receipt.Reason = "waiting_for_normal_server_results"
	s.mu.Unlock()
	err = s.send(op.ctx, packet, payload)
	s.mu.Lock()
	if err == nil {
		op.receipt.TransportWritten = true
	} else {
		op.receipt.WriteUnconfirmed = true
	}
	s.mu.Unlock()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	settle := time.NewTimer(s.timeout * 3 / 4)
	defer settle.Stop()
	for {
		_ = s.observe(op.ctx)
		s.mu.Lock()
		done := terminal(op.receipt.Status)
		s.mu.Unlock()
		if done {
			return
		}
		select {
		case <-op.ctx.Done():
			return
		case <-settle.C:
			_ = s.observe(op.ctx)
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) Cancel(ctx context.Context, r *qv1.CancelRequest) (*qv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "bound operation required")
	}
	s.mu.Lock()
	if r.SessionId != s.state.SessionId || s.last == nil || r.OperationId != s.last.receipt.OperationId {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "operation changed")
	}
	op := s.last
	if s.active == op && !terminal(op.receipt.Status) {
		op.receipt.CancelRequested = true
		op.cancel()
	}
	s.mu.Unlock()
	select {
	case <-op.done:
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(), nil
}
func (s *Service) BeforeLogout(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.mu.Lock()
	s.closed = true
	s.state.Closing = true
	op := s.active
	if op != nil {
		op.receipt.CancelRequested = true
		op.cancel()
	}
	s.mu.Unlock()
	if op != nil {
		select {
		case <-op.done:
		case <-ctx.Done():
		}
	}
	return &emptypb.Empty{}, nil
}
func (s *Service) OnPacket(_ context.Context, p *modv1.WorldPacket) (*emptypb.Empty, error) {
	if p == nil {
		return &emptypb.Empty{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bad := func() { s.state.MalformedPackets++ }
	switch p.Opcode {
	case opcode.SMSGGameobjectQueryResponse:
		if len(p.Payload) >= 4 && (uint32(p.Payload[0])|uint32(p.Payload[1])<<8|uint32(p.Payload[2])<<16|uint32(p.Payload[3])<<24)&0x7fffffff != barrelEntry {
			break
		}
		if s.queryGO > 0 {
			known, err := barrelTemplate(p.Payload)
			if err != nil {
				bad()
			} else {
				s.state.BarrelTemplateKnown = known
			}
		}
	case opcode.SMSGItemQuerySingleResponse:
		if len(p.Payload) >= 4 && (uint32(p.Payload[0])|uint32(p.Payload[1])<<8|uint32(p.Payload[2])<<16|uint32(p.Payload[3])<<24)&0x7fffffff != bucketEntry {
			break
		}
		if s.queryItem > 0 {
			known, err := bucketTemplate(p.Payload)
			if err != nil {
				bad()
			} else {
				s.state.BucketTemplateKnown = known
			}
		}
	case opcode.SMSGNewWorld:
		if len(p.Payload) != 20 {
			bad()
			break
		}
		s.generation++
		s.viewKnown = false
		s.state.QuestLogKnown = false
		s.state.InventoryKnown = false
		if s.active != nil {
			s.active.cancel()
		}
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo, opcode.SMSGCastFailed, opcode.SMSGSpellFailure:
		probe := reader{data: p.Payload}
		if p.Opcode == opcode.SMSGSpellStart || p.Opcode == opcode.SMSGSpellGo {
			probe.guid()
			probe.guid()
		} else if p.Opcode == opcode.SMSGSpellFailure {
			probe.guid()
		}
		probe.u8()
		spell := probe.u32()
		if probe.err != nil {
			bad()
			break
		}
		if spell != throwSpell && spell != createSpell {
			break
		}
		e, err := decodeCast(p.Opcode, p.Payload)
		if err != nil {
			bad()
			break
		}
		op := s.active
		if op == nil || !op.reserved || op.generation != s.generation {
			break
		}
		r := op.receipt
		if e.spell != r.SpellId || e.caster != 0 && e.caster != s.self || r.Action == qv1.Action_THROW_BUCKET && e.count != byte(r.CastCount) {
			break
		}
		if e.kind == "failed" {
			if !terminal(r.Status) {
				r.Status = qv1.Status_SERVER_REJECTED
				r.Reason = "normal_server_cast_rejected"
				r.FailureCode = uint32(e.failure)
			}
			break
		}
		if e.caster != s.self {
			break
		}
		if r.Action == qv1.Action_THROW_BUCKET && (e.item != r.Item.ItemGuid || e.mask != 0x40 || dist(e.dst, op.target) > .05) {
			break
		}
		if e.kind == "start" {
			r.OwnCastStartObserved = true
		}
		if e.kind == "go" {
			r.OwnCastGoObserved = true
		}
		if s.viewKnown {
			s.evidenceLocked(s.view)
		}
	case opcode.SMSGItemPushResult:
		who, entry, err := itemPush(p.Payload)
		if err != nil {
			bad()
			break
		}
		if op := s.active; op != nil && op.reserved && who == s.self && entry == bucketEntry {
			op.receipt.OwnItemPushObserved = true
		}
	case opcode.SMSGInventoryChangeFailure:
		code, a, b, err := inventoryFailure(p.Payload)
		if err != nil {
			bad()
			break
		}
		if op := s.active; op != nil && op.reserved && !terminal(op.receipt.Status) && code != 0 && (op.receipt.Action == qv1.Action_FILL_BUCKET && (a == 0 && b == 0) || op.receipt.Item != nil && (a == op.receipt.Item.ItemGuid || b == op.receipt.Item.ItemGuid)) {
			op.receipt.Status = qv1.Status_SERVER_REJECTED
			op.receipt.FailureCode = uint32(code)
			op.receipt.Reason = "normal_server_inventory_rejected"
		}
	}
	return &emptypb.Empty{}, nil
}
