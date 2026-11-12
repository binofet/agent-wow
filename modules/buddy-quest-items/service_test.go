package buddyquestitems

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	qv1 "github.com/agent-wow/agent-wow/modules/buddy-quest-items/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const barrelGUID = uint64(0xf11002d77a000001)
const fireGUID = uint64(0xf130005bf1000001)
const itemGUID = uint64(0x4000000000000011)

type fixture struct {
	mu                 sync.Mutex
	s                  *Service
	w                  *worldv1.SnapshotResponse
	a                  *actionsv1.State
	packets            []*modv1.SendPacketRequest
	hook               func(context.Context, *modv1.SendPacketRequest) error
	acquireHook        func(context.Context, int) error
	acquires, releases int
	releaseFail        bool
}

func (f *fixture) update(fn func()) { f.mu.Lock(); defer f.mu.Unlock(); fn() }
func (f *fixture) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	f.packets = append(f.packets, proto.Clone(r).(*modv1.SendPacketRequest))
	hook := f.hook
	f.mu.Unlock()
	switch r.Opcode {
	case opcode.CMSGGameobjectQuery:
		f.emit(opcode.SMSGGameobjectQueryResponse, barrelFixture())
	case opcode.CMSGItemQuerySingle:
		f.emit(opcode.SMSGItemQuerySingleResponse, bucketFixture())
	}
	if hook != nil {
		if err := hook(ctx, r); err != nil {
			return nil, err
		}
	}
	return &emptypb.Empty{}, nil
}
func (f *fixture) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	if r.Method == "acquire_interaction" {
		f.acquires++
		n, hook := f.acquires, f.acquireHook
		f.mu.Unlock()
		if hook != nil {
			if err := hook(ctx, n); err != nil {
				return nil, err
			}
		}
		f.mu.Lock()
	}
	defer f.mu.Unlock()
	var out proto.Message
	switch r.Method {
	case "snapshot":
		q := &worldv1.SnapshotRequest{}
		_ = r.Request.UnmarshalTo(q)
		if q.CreatureEntry != fireEntry || q.GameObjectEntry != barrelEntry {
			return nil, errors.New("missing exact observed-entry query")
		}
		out = proto.Clone(f.w)
	case "status":
		out = proto.Clone(f.a)
	case "acquire_interaction":
		q := &actionsv1.InteractionRequest{}
		_ = r.Request.UnmarshalTo(q)
		if q.Owner != "quests" || q.Token != "" && q.Token != "owned" {
			return nil, errors.New("wrong lease")
		}
		f.a.Moving = false
		f.a.InteractionOwner = "quests"
		f.a.InteractionLeaseRemainingMs = 15000
		out = &actionsv1.InteractionLease{Token: "owned", State: proto.Clone(f.a).(*actionsv1.State)}
	case "release_interaction":
		q := &actionsv1.InteractionRelease{}
		_ = r.Request.UnmarshalTo(q)
		if q.Owner != "quests" || q.Token != "owned" {
			return nil, errors.New("wrong release token")
		}
		f.releases++
		if f.releaseFail {
			return nil, errors.New("release unavailable")
		}
		f.a.InteractionOwner = ""
		out = proto.Clone(f.a)
	default:
		return nil, errors.New("unexpected dependency")
	}
	p, err := anypb.New(out)
	return &modv1.InvokeModuleResponse{Result: p}, err
}
func (f *fixture) emit(op uint32, p []byte) {
	_, _ = f.s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: p})
}
func (f *fixture) count(op uint32) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.packets {
		if p.Opcode == op {
			n++
		}
	}
	return n
}
func rig(t *testing.T) (*Service, *fixture) {
	t.Helper()
	w := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 4, Present: true, MapKnown: true, MapId: 1, PositionKnown: true, PositionRevision: 1, X: 1, Y: 1, Z: 1, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100, UnitFlagsKnown: true, UnitFlags: 8, MovementFlagsKnown: true}, SelfQuestLogKnown: true, SelfQuestLogRevision: 1, InventoryKnown: true, InventoryRevision: 1}
	for i := range 25 {
		w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i), ObjectiveCounts: make([]uint32, 4)})
	}
	w.SelfQuestLog[0].QuestId = questID
	for i := range 5 {
		b := &worldv1.InventoryBag{Bag: uint32(i), SlotKnown: true}
		if i == 0 {
			b.ContentsKnown = true
			b.SlotCount = 16
			for j := range 16 {
				b.Slots = append(b.Slots, &worldv1.BackpackSlot{Slot: uint32(j)})
			}
		}
		w.InventoryBags = append(w.InventoryBags, b)
	}
	w.RequestedGameObjects = []*worldv1.GameObjectState{{Guid: barrelGUID, Entry: barrelEntry, Present: true, MapKnown: true, MapId: 1, PositionKnown: true, X: 3, Y: 1, Z: 1, PositionRevision: 1, TypeKnown: true, GameObjectType: 10, FlagsKnown: true}}
	w.RequestedCreatures = []*worldv1.CreatureState{{Guid: fireGUID, Entry: fireEntry, Present: true, MapKnown: true, MapId: 1, PositionKnown: true, X: 10, Y: 1, Z: 1, PositionRevision: 1, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100}}
	f := &fixture{w: w, a: &actionsv1.State{Position: &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: 1, X: 1, Y: 1, Z: 1, Revision: 1}}}
	s := NewService(f, "body", 4)
	s.timeout = 80 * time.Millisecond
	f.s = s
	ready(t, s)
	return s, f
}
func ready(t *testing.T, s *Service) *qv1.State {
	t.Helper()
	r, err := s.GetState(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.GetState(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func request(t *testing.T, s *Service, id string, action qv1.Action) *qv1.AttemptRequest {
	t.Helper()
	r := ready(t, s)
	q := &qv1.AttemptRequest{SessionId: r.SessionId, RequestId: id, QuestId: questID, Action: action, ExpectedQuestLogRevision: r.QuestLogRevision, ExpectedInventoryRevision: r.InventoryRevision}
	if action == qv1.Action_FILL_BUCKET {
		q.SourceObjectGuid = barrelGUID
	} else {
		q.TargetCreatureGuid = fireGUID
		if r.Bucket != nil {
			q.ExpectedItemGuid = r.Bucket.ItemGuid
		}
	}
	return q
}
func wait(t *testing.T, s *Service) *qv1.State {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		r := ready(t, s)
		if !r.Active {
			return r
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("operation did not finish")
	return nil
}
func giveBucket(f *fixture, bag, slot uint32) {
	b := f.w.InventoryBags[bag]
	if bag > 0 && b.ContainerGuid == 0 {
		b.ContainerGuid = 0x4000000000000050
		b.Entry = 4496
		b.MetadataKnown = true
		b.ContentsKnown = true
		b.SlotCount = 6
		for i := range 6 {
			b.Slots = append(b.Slots, &worldv1.BackpackSlot{Bag: bag, Slot: uint32(i)})
		}
	}
	b.Slots[slot] = &worldv1.BackpackSlot{Bag: bag, Slot: slot, ItemGuid: itemGUID, Entry: bucketEntry, StackCount: 1, MetadataKnown: true}
	f.w.InventoryRevision++
}
func consume(f *fixture) {
	for _, b := range f.w.InventoryBags {
		for j, i := range b.Slots {
			if i.ItemGuid == itemGUID {
				b.Slots[j] = &worldv1.BackpackSlot{Bag: b.Bag, Slot: uint32(j)}
			}
		}
	}
	f.w.InventoryRevision++
}

func TestFillThenThrowRequiresActualOwnInventoryAndIndividualCredit(t *testing.T) {
	s, f := rig(t)
	f.hook = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGGameobjUse {
			f.update(func() { giveBucket(f, 2, 5) })
			f.emit(opcode.SMSGItemPushResult, pushFixture(4))
		}
		return nil
	}
	q := request(t, s, "fill", qv1.Action_FILL_BUCKET)
	if _, err := s.Attempt(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	r := wait(t, s)
	if r.Operation.Status != qv1.Status_COMPLETED || !r.Operation.InventoryAddedObserved || !r.Operation.OwnItemPushObserved || r.Operation.QuestCreditObserved || !r.Operation.LeaseReleased || r.Bucket.Bag != 2 || r.Bucket.Slot != 5 {
		t.Fatal(r)
	}
	f.hook = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGUseItem {
			if p.Payload[0] != 20 || p.Payload[1] != 5 {
				t.Fatalf("bag mapping %x", p.Payload)
			}
			dst := point{10, 1, 1}
			f.emit(opcode.SMSGSpellGo, castFixture(true, itemGUID, 4, p.Payload[2], throwSpell, &dst))
			f.update(func() { consume(f); f.w.SelfQuestLog[0].ObjectiveCounts[0]++; f.w.SelfQuestLogRevision++ })
		}
		return nil
	}
	q = request(t, s, "throw", qv1.Action_THROW_BUCKET)
	if _, err := s.Attempt(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	r = wait(t, s)
	if r.Operation.Status != qv1.Status_COMPLETED || !r.Operation.OwnCastGoObserved || !r.Operation.InventoryConsumedObserved || !r.Operation.QuestCreditObserved || r.ObjectiveCount != 1 || r.QuestComplete || f.count(opcode.CMSGCastSpell) != 0 || f.count(opcode.CMSGUseItem) != 1 {
		t.Fatal(r)
	}
}
func TestCastAloneNeverClaimsQuestCreditAndConfirmedNoCreditCanBeDistinguished(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cast_only", true: "consumed_no_credit"}[consumed], func(t *testing.T) {
			s, f := rig(t)
			f.update(func() { giveBucket(f, 0, 0) })
			f.hook = func(_ context.Context, p *modv1.SendPacketRequest) error {
				if p.Opcode == opcode.CMSGUseItem {
					dst := point{10, 1, 1}
					f.emit(opcode.SMSGSpellGo, castFixture(true, itemGUID, 4, p.Payload[2], throwSpell, &dst))
					if consumed {
						f.update(func() { consume(f) })
					}
				}
				return nil
			}
			q := request(t, s, "throw", qv1.Action_THROW_BUCKET)
			_, err := s.Attempt(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			r := wait(t, s)
			want := qv1.Status_OUTCOME_UNCONFIRMED
			if consumed {
				want = qv1.Status_NO_CREDIT_OBSERVED
			}
			if r.Operation.Status != want || r.Operation.QuestCreditObserved || r.ObjectiveCount != 0 || r.QuestComplete {
				t.Fatal(r)
			}
		})
	}
}
func TestRequestAndAmbiguousBindingAreNeverBlindlyReplayed(t *testing.T) {
	s, f := rig(t)
	q := request(t, s, "once", qv1.Action_FILL_BUCKET)
	if _, err := s.Attempt(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	r := wait(t, s)
	if r.Operation.Status != qv1.Status_OUTCOME_UNCONFIRMED {
		t.Fatal(r)
	}
	if _, err := s.Attempt(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if f.count(opcode.CMSGGameobjUse) != 1 {
		t.Fatal("same request replayed")
	}
	q = proto.Clone(q).(*qv1.AttemptRequest)
	q.RequestId = "new_id_same_evidence"
	if _, err := s.Attempt(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	r = wait(t, s)
	if r.Operation.Status != qv1.Status_BLOCKED || f.count(opcode.CMSGGameobjUse) != 1 {
		t.Fatal(r)
	}
	q.RequestId = "once"
	q.SourceObjectGuid++
	if _, err := s.Attempt(context.Background(), q); err == nil {
		t.Fatal("rebound request ID")
	}
}
func TestUnsafeAndStaleInputsCannotSendQuestItemPackets(t *testing.T) {
	for _, mode := range []string{"self_identity", "own_combat", "own_dead", "unknown_map", "actions_map", "unknown_inventory", "unknown_bag", "duplicate_slot", "foreign_item", "barrel_map", "barrel_flags", "barrel_range", "stale_inventory", "wrong_session", "quest_complete"} {
		t.Run(mode, func(t *testing.T) {
			s, f := rig(t)
			q := request(t, s, "reject", qv1.Action_FILL_BUCKET)
			f.update(func() {
				switch mode {
				case "self_identity":
					f.w.Self.Guid = 5
				case "own_combat":
					f.w.Self.UnitFlags |= 0x80000
				case "own_dead":
					f.w.Self.Alive = false
				case "unknown_map":
					f.w.Self.MapKnown = false
				case "actions_map":
					f.a.Position.MapId = 0
				case "unknown_inventory":
					f.w.InventoryKnown = false
				case "unknown_bag":
					f.w.InventoryBags[4].SlotKnown = false
				case "duplicate_slot":
					f.w.InventoryBags[0].Slots[1].Slot = 0
				case "foreign_item":
					f.w.InventoryBags[0].Slots[0] = &worldv1.BackpackSlot{ItemGuid: 3, Entry: bucketEntry, StackCount: 1, MetadataKnown: true}
				case "barrel_map":
					f.w.RequestedGameObjects[0].MapId = 0
				case "barrel_flags":
					f.w.RequestedGameObjects[0].Flags = 0x10
				case "barrel_range":
					f.w.RequestedGameObjects[0].X = 10
				case "stale_inventory":
					f.w.InventoryRevision++
				case "wrong_session":
					q.SessionId = "foreign"
				case "quest_complete":
					f.w.SelfQuestLog[0].Complete = true
				}
			})
			if _, err := s.Attempt(context.Background(), q); err == nil {
				t.Fatal("unsafe request accepted")
			}
			if f.count(opcode.CMSGGameobjUse) != 0 || f.count(opcode.CMSGUseItem) != 0 {
				t.Fatal("unsafe write")
			}
		})
	}
}
func TestFreshLeaseReadRejectsBodyTargetOrInventoryChangeBeforeWrite(t *testing.T) {
	for _, mode := range []string{"position", "map", "barrel", "inventory"} {
		t.Run(mode, func(t *testing.T) {
			s, f := rig(t)
			q := request(t, s, "fresh", qv1.Action_FILL_BUCKET)
			f.acquireHook = func(_ context.Context, n int) error {
				if n == 1 {
					f.update(func() {
						switch mode {
						case "position":
							f.w.Self.PositionRevision++
						case "map":
							f.w.Self.MapId = 530
						case "barrel":
							f.w.RequestedGameObjects[0].PositionRevision++
						case "inventory":
							f.w.InventoryRevision++
						}
					})
				}
				return nil
			}
			if _, err := s.Attempt(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			r := wait(t, s)
			if r.Operation.Status != qv1.Status_BLOCKED || !r.Operation.LeaseReleased || f.count(opcode.CMSGGameobjUse) != 0 {
				t.Fatal(r)
			}
		})
	}
}
func TestMatchedFailureAndAmbiguousWritePreserveAuthoritativeEvidence(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "confirmed_before_error"}[confirmed], func(t *testing.T) {
			s, f := rig(t)
			f.update(func() { giveBucket(f, 0, 0) })
			f.hook = func(_ context.Context, p *modv1.SendPacketRequest) error {
				if p.Opcode == opcode.CMSGUseItem {
					if confirmed {
						dst := point{10, 1, 1}
						f.emit(opcode.SMSGSpellGo, castFixture(true, itemGUID, 4, p.Payload[2], throwSpell, &dst))
						f.update(func() {
							consume(f)
							f.w.SelfQuestLog[0].ObjectiveCounts[0] = 5
							f.w.SelfQuestLog[0].Complete = true
							f.w.SelfQuestLogRevision++
						})
					} else {
						p2 := []byte{p.Payload[2]}
						put32(&p2, throwSpell)
						p2 = append(p2, 67)
						f.emit(opcode.SMSGCastFailed, p2)
					}
					return errors.New("ambiguous transport")
				}
				return nil
			}
			_, err := s.Attempt(context.Background(), request(t, s, "result", qv1.Action_THROW_BUCKET))
			if err != nil {
				t.Fatal(err)
			}
			r := wait(t, s)
			want := qv1.Status_SERVER_REJECTED
			if confirmed {
				want = qv1.Status_COMPLETED
			}
			if r.Operation.Status != want || !r.Operation.WriteUnconfirmed || r.Operation.TransportWritten || r.QuestComplete != confirmed {
				t.Fatal(r)
			}
		})
	}
}
func TestForeignCastItemCountCasterOrDestinationCannotConfirm(t *testing.T) {
	for _, mode := range []string{"item", "count", "caster", "destination", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			s, f := rig(t)
			f.update(func() { giveBucket(f, 0, 0) })
			f.hook = func(_ context.Context, p *modv1.SendPacketRequest) error {
				if p.Opcode == opcode.CMSGUseItem {
					item, caster, count, dst := itemGUID, uint64(4), p.Payload[2], point{10, 1, 1}
					switch mode {
					case "item":
						item++
					case "count":
						count++
					case "caster":
						caster = 5
					case "destination":
						dst.x++
					}
					wire := castFixture(true, item, caster, count, throwSpell, &dst)
					if mode == "truncated" {
						wire = wire[:len(wire)-1]
					}
					f.emit(opcode.SMSGSpellGo, wire)
					f.update(func() { consume(f) })
				}
				return nil
			}
			_, err := s.Attempt(context.Background(), request(t, s, "foreign", qv1.Action_THROW_BUCKET))
			if err != nil {
				t.Fatal(err)
			}
			r := wait(t, s)
			if r.Operation.Status != qv1.Status_OUTCOME_UNCONFIRMED || r.Operation.OwnCastGoObserved || r.Operation.QuestCreditObserved {
				t.Fatal(r)
			}
		})
	}
}
func TestCancelAndLogoutReleaseOwnedLeaseWithoutAdditionalUses(t *testing.T) {
	for _, logout := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "logout"}[logout], func(t *testing.T) {
			s, f := rig(t)
			written := make(chan struct{})
			f.hook = func(_ context.Context, p *modv1.SendPacketRequest) error {
				if p.Opcode == opcode.CMSGGameobjUse {
					close(written)
				}
				return nil
			}
			r, err := s.Attempt(context.Background(), request(t, s, "cancel", qv1.Action_FILL_BUCKET))
			if err != nil {
				t.Fatal(err)
			}
			<-written
			if logout {
				_, err = s.BeforeLogout(context.Background(), &emptypb.Empty{})
			} else {
				_, err = s.Cancel(context.Background(), &qv1.CancelRequest{SessionId: "body", OperationId: r.Operation.OperationId})
			}
			if err != nil {
				t.Fatal(err)
			}
			r = wait(t, s)
			if r.Operation.Status != qv1.Status_CANCELLED || !r.Operation.LeaseReleased || r.Active || f.count(opcode.CMSGGameobjUse) != 1 {
				t.Fatal(r)
			}
		})
	}
}
func TestUnrelatedTemplateAndSpellPacketsAreIgnoredAndReleaseFailureIsExplicit(t *testing.T) {
	s, f := rig(t)
	p := bucketFixture()
	binary.LittleEndian.PutUint32(p, 111)
	f.emit(opcode.SMSGItemQuerySingleResponse, p)
	f.emit(opcode.SMSGSpellGo, castFixture(true, 4, 4, 0, 585, nil))
	if ready(t, s).MalformedPackets != 0 {
		t.Fatal("unrelated normal traffic counted malformed")
	}
	f.update(func() { f.releaseFail = true })
	f.hook = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGGameobjUse {
			f.update(func() { giveBucket(f, 0, 0) })
		}
		return nil
	}
	_, err := s.Attempt(context.Background(), request(t, s, "cleanup", qv1.Action_FILL_BUCKET))
	if err != nil {
		t.Fatal(err)
	}
	r := wait(t, s)
	if r.Operation.Status != qv1.Status_COMPLETED || r.Operation.LeaseReleased || !r.Operation.CleanupUnconfirmed {
		t.Fatal(r)
	}
}

func TestCancellationDuringLeaseReservationNeverWritesAndStillReleases(t *testing.T) {
	s, f := rig(t)
	entered := make(chan struct{})
	f.acquireHook = func(ctx context.Context, n int) error {
		if n == 2 {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	r, err := s.Attempt(context.Background(), request(t, s, "cancel_lease", qv1.Action_FILL_BUCKET))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	r, err = s.Cancel(context.Background(), &qv1.CancelRequest{SessionId: "body", OperationId: r.Operation.OperationId})
	if err != nil || r.Active || r.Operation.Status != qv1.Status_CANCELLED || !r.Operation.LeaseReleased || f.count(opcode.CMSGGameobjUse) != 0 {
		t.Fatal(r, err)
	}
}

func TestKnownWorldTransferRetiresPendingAttemptWithoutClaimOrRetry(t *testing.T) {
	s, f := rig(t)
	f.hook = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGGameobjUse {
			f.emit(opcode.SMSGNewWorld, make([]byte, 20))
			f.update(func() { f.w.Self.MapId = 530; f.a.Position.MapId = 530; f.w.Self.PositionRevision++ })
		}
		return nil
	}
	_, err := s.Attempt(context.Background(), request(t, s, "transfer", qv1.Action_FILL_BUCKET))
	if err != nil {
		t.Fatal(err)
	}
	r := wait(t, s)
	if r.Active || r.Operation.Status != qv1.Status_OUTCOME_UNCONFIRMED || r.Operation.InventoryAddedObserved || r.Operation.QuestCreditObserved || f.count(opcode.CMSGGameobjUse) != 1 || !r.Operation.LeaseReleased {
		t.Fatal(r)
	}
}

func TestFiveIndividualCreditsCompleteOnlyThisCharactersActualQuestLog(t *testing.T) {
	s, f := rig(t)
	s.timeout = time.Second
	f.hook = func(_ context.Context, p *modv1.SendPacketRequest) error {
		switch p.Opcode {
		case opcode.CMSGGameobjUse:
			f.update(func() { giveBucket(f, 0, 0) })
		case opcode.CMSGUseItem:
			dst := point{10, 1, 1}
			// A different companion's valid cast is never this character's hit.
			f.emit(opcode.SMSGSpellGo, castFixture(true, itemGUID, 5, p.Payload[2], throwSpell, &dst))
			f.emit(opcode.SMSGSpellGo, castFixture(true, itemGUID, 4, p.Payload[2], throwSpell, &dst))
			f.update(func() {
				consume(f)
				f.w.SelfQuestLog[0].ObjectiveCounts[0]++
				f.w.SelfQuestLogRevision++
				f.w.SelfQuestLog[0].Complete = f.w.SelfQuestLog[0].ObjectiveCounts[0] == 5
			})
		}
		return nil
	}
	for i := 0; i < 5; i++ {
		_, err := s.Attempt(context.Background(), request(t, s, fmt.Sprintf("fill_%d", i), qv1.Action_FILL_BUCKET))
		if err != nil {
			t.Fatal(err)
		}
		wait(t, s)
		_, err = s.Attempt(context.Background(), request(t, s, fmt.Sprintf("throw_%d", i), qv1.Action_THROW_BUCKET))
		if err != nil {
			t.Fatal(err)
		}
		r := wait(t, s)
		if r.Operation.Status != qv1.Status_COMPLETED || r.ObjectiveCount != uint32(i+1) || r.QuestComplete != (i == 4) {
			t.Fatal(r)
		}
	}
	if f.count(opcode.CMSGUseItem) != 5 || f.count(opcode.CMSGGameobjUse) != 5 || ready(t, s).Ready {
		t.Fatal("quest completion did not stop further attempts")
	}
}
