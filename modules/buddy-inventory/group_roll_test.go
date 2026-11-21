package buddyinventory

import (
	"context"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const rollTestGUID = uint64(0x400000000000aa01)
const rollTestEntry = uint32(2962)

// The ordinary inventory fixture remains responsible for World observations.
// These vote tests do not synthesize metadata replies; explicit choices remain
// independent of whether a full template response has actually arrived.
type rollTestSession struct{ *fakeInventory }

func rollTestOffer(guid uint64, entry, suffix, property, countdown uint32, mask byte) []byte {
	body := binary.LittleEndian.AppendUint64(nil, guid)
	for _, value := range []uint32{389, 2, entry, suffix, property, 1, countdown} {
		body = binary.LittleEndian.AppendUint32(body, value)
	}
	return append(body, mask)
}

func rollTestEcho(actor uint64, entry, suffix, property uint32, number, choice, automatic byte) []byte {
	body := binary.LittleEndian.AppendUint64(nil, 0)
	body = binary.LittleEndian.AppendUint32(body, 2)
	body = binary.LittleEndian.AppendUint64(body, actor)
	for _, value := range []uint32{entry, suffix, property} {
		body = binary.LittleEndian.AppendUint32(body, value)
	}
	return append(body, number, choice, automatic)
}

func rollTestPacket(t *testing.T, service *Service, code uint32, body []byte) {
	t.Helper()
	if _, err := service.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: code, Payload: body}); err != nil {
		t.Fatal(err)
	}
}

func rollTestEntryPacket() []byte {
	body := make([]byte, 20)
	binary.LittleEndian.PutUint32(body, 389)
	return body
}

func newRollTest(t *testing.T) (*Service, *fakeInventory, func(time.Duration)) {
	t.Helper()
	service, fixture := fixtureGear(t, true)
	service.session = &rollTestSession{fixture}
	fixture.w.Self.MapId, fixture.w.VisitRevision = 389, 1
	var elapsed atomic.Int64
	service.now = func() time.Time { return time.Unix(100, 0).Add(time.Duration(elapsed.Load())) }
	rollTestPacket(t, service, opcode.SMSGLoginVerifyWorld, rollTestEntryPacket())
	return service, fixture, func(duration time.Duration) { elapsed.Add(int64(duration)) }
}

func readRollTest(t *testing.T, service *Service) *inventoryv1.RollState {
	t.Helper()
	state, err := service.Rolls(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func addRollTest(t *testing.T, service *Service, guid uint64, mask byte) *inventoryv1.RollRequest {
	t.Helper()
	rollTestPacket(t, service, opcode.SMSGLootStartRoll, rollTestOffer(guid, rollTestEntry, 17, 23, 60000, mask))
	state := readRollTest(t, service)
	for _, offer := range state.Offers {
		if offer.RollGuid == guid {
			return &inventoryv1.RollRequest{SessionId: "body", RequestId: "roll-one", RollGuid: guid,
				ExpectedOfferRevision: offer.Revision, ExpectedLootSlot: offer.LootSlot, VisitId: state.VisitId, Choice: "need"}
		}
	}
	t.Fatal("actual start packet did not expose its offer", state)
	return nil
}

func receiptRollTest(t *testing.T, state *inventoryv1.RollState, request string) *inventoryv1.RollReceipt {
	t.Helper()
	for _, receipt := range state.Receipts {
		if receipt.RequestId == request {
			return receipt
		}
	}
	t.Fatal("exact request receipt missing", request, state)
	return nil
}

func rollTestWrites(fixture *fakeInventory) []*modv1.SendPacketRequest {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	var out []*modv1.SendPacketRequest
	for _, packet := range fixture.packets {
		if packet.Opcode != opcode.CMSGItemQuerySingle {
			out = append(out, proto.Clone(packet).(*modv1.SendPacketRequest))
		}
	}
	return out
}

func TestGroupRollOffersRemainFactsUntilExplicitVote(t *testing.T) {
	service, fixture, advance := newRollTest(t)
	request := addRollTest(t, service, rollTestGUID, 15)
	first := readRollTest(t, service)
	if first.SessionId != "body" || first.SelfGuid != 6 || first.VisitId != 1 || len(first.Offers) != 1 {
		t.Fatal(first)
	}
	offer := first.Offers[0]
	if offer.RollGuid != rollTestGUID || offer.ItemEntry != rollTestEntry || offer.LootSlot != 2 || offer.MapId != 389 ||
		offer.RandomSuffix != 17 || offer.RandomProperty != 23 || offer.ItemCount != 1 || offer.OfferedMask != 15 ||
		!offer.Available || offer.RemainingMs == 0 || offer.RemainingMs > 60000 || offer.Revision != request.ExpectedOfferRevision {
		t.Fatal("incomplete actual offer", offer)
	}
	advance(time.Second)
	rollTestPacket(t, service, opcode.SMSGLootStartRoll, rollTestOffer(rollTestGUID, rollTestEntry, 17, 23, 60000, 15))
	service.Tick(context.Background())
	second := readRollTest(t, service)
	if len(second.Offers) != 1 || second.Offers[0].Revision != offer.Revision || second.Offers[0].RemainingMs > 59000 || len(rollTestWrites(fixture)) != 0 {
		t.Fatal("duplicate announcement changed custody or voted automatically", second, rollTestWrites(fixture))
	}
}

func TestGroupRollExactChoiceWritesOnceAndNeedsOwnEcho(t *testing.T) {
	for _, test := range []struct {
		choice               string
		vote, number, echoed byte
	}{{"pass", 0, 128, 0}, {"need", 1, 0, 0}, {"greed", 2, 128, 2}, {"disenchant", 3, 128, 3}} {
		t.Run(test.choice, func(t *testing.T) {
			service, fixture, _ := newRollTest(t)
			request := addRollTest(t, service, rollTestGUID, 15)
			request.Choice = test.choice
			state, err := service.Roll(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			receipt := receiptRollTest(t, state, request.RequestId)
			if !receipt.TransportAttempted || !receipt.TransportWritten || receipt.ServerVoteObserved || receipt.Choice != test.choice ||
				receipt.RollGuid != request.RollGuid || receipt.OfferRevision != request.ExpectedOfferRevision || receipt.LootSlot != request.ExpectedLootSlot || receipt.VisitId != 1 {
				t.Fatal(receipt)
			}
			packets := rollTestWrites(fixture)
			if len(packets) != 1 || packets[0].Opcode != opcode.CMSGLootRoll || len(packets[0].Payload) != 13 ||
				binary.LittleEndian.Uint64(packets[0].Payload) != rollTestGUID || binary.LittleEndian.Uint32(packets[0].Payload[8:]) != 2 || packets[0].Payload[12] != test.vote {
				t.Fatal("explicit vote did not match the exact offered identifier", packets)
			}
			for _, echo := range [][]byte{
				rollTestEcho(7, rollTestEntry, 17, 23, test.number, test.echoed, 0),
				rollTestEcho(6, rollTestEntry+1, 17, 23, test.number, test.echoed, 0),
				rollTestEcho(6, rollTestEntry, 18, 23, test.number, test.echoed, 0),
				rollTestEcho(6, rollTestEntry, 17, 24, test.number, test.echoed, 0),
				rollTestEcho(6, rollTestEntry, 17, 23, test.number, test.echoed, 1),
				rollTestEcho(6, rollTestEntry, 17, 23, 127, test.echoed, 0),
			} {
				rollTestPacket(t, service, opcode.SMSGLootRoll, echo)
				if receiptRollTest(t, readRollTest(t, service), request.RequestId).ServerVoteObserved {
					t.Fatal("unrelated or automatic echo confirmed an explicit vote", echo)
				}
			}
			rollTestPacket(t, service, opcode.SMSGLootRoll, rollTestEcho(6, rollTestEntry, 17, 23, test.number, test.echoed, 0))
			if !receiptRollTest(t, readRollTest(t, service), request.RequestId).ServerVoteObserved {
				t.Fatal("exact own vote echo did not confirm")
			}
			if _, err := service.Roll(context.Background(), request); err != nil || len(rollTestWrites(fixture)) != 1 {
				t.Fatal("exact retry resent a completed vote", err)
			}
			changed := proto.Clone(request).(*inventoryv1.RollRequest)
			changed.Choice = "pass"
			if test.choice == "pass" {
				changed.Choice = "need"
			}
			if _, err := service.Roll(context.Background(), changed); err == nil || len(rollTestWrites(fixture)) != 1 {
				t.Fatal("changed request was accepted", err)
			}
			changed = proto.Clone(request).(*inventoryv1.RollRequest)
			changed.RequestId = "second-request"
			if _, err := service.Roll(context.Background(), changed); err == nil || len(rollTestWrites(fixture)) != 1 {
				t.Fatal("new request resent the same offer", err)
			}
		})
	}
}

func TestGroupRollUnknownMaskBodyVisitAndExpiryNeverWrite(t *testing.T) {
	for _, name := range []string{"unknown offer", "session", "revision", "loot slot", "request visit", "world visit", "world map", "world body", "choice", "not offered", "expired", "closed"} {
		t.Run(name, func(t *testing.T) {
			service, fixture, advance := newRollTest(t)
			request := addRollTest(t, service, rollTestGUID, 1)
			request.Choice = "pass"
			switch name {
			case "unknown offer":
				request.RollGuid++
			case "session":
				request.SessionId = "another-body"
			case "revision":
				request.ExpectedOfferRevision++
			case "loot slot":
				request.ExpectedLootSlot++
			case "request visit":
				request.VisitId++
			case "world visit":
				fixture.w.VisitRevision++
			case "world map":
				fixture.w.Self.MapId++
			case "world body":
				fixture.w.Self.Guid++
			case "choice":
				request.Choice = "arbitrary"
			case "not offered":
				request.Choice = "disenchant"
			case "expired":
				advance(time.Minute)
			case "closed":
				_, _ = service.BeforeLogout(context.Background(), &emptypb.Empty{})
			}
			state, err := service.Roll(context.Background(), request)
			if len(rollTestWrites(fixture)) != 0 {
				t.Fatal("unbound explicit vote reached transport", err, rollTestWrites(fixture))
			}
			if name == "world visit" || name == "world map" || name == "world body" {
				if err != nil || receiptRollTest(t, state, request.RequestId).Phase != "not_dispatched" {
					t.Fatal("changed final observation needs an explicit local abort receipt", state, err)
				}
			} else if err == nil {
				t.Fatal("invalid admission was accepted", state)
			}
		})
	}
}

func TestGroupRollAmbiguousEchoConsidersUnattemptedLiveOffer(t *testing.T) {
	service, fixture, _ := newRollTest(t)
	request := addRollTest(t, service, rollTestGUID, 15)
	addRollTest(t, service, rollTestGUID+1, 15)
	state, err := service.Roll(context.Background(), request)
	if err == nil {
		if len(rollTestWrites(fixture)) != 1 {
			t.Fatal(rollTestWrites(fixture))
		}
		rollTestPacket(t, service, opcode.SMSGLootRoll, rollTestEcho(6, rollTestEntry, 17, 23, 0, 0, 0))
		if receiptRollTest(t, readRollTest(t, service), request.RequestId).ServerVoteObserved {
			t.Fatal("offer with no attempted request was ignored when correlating an ambiguous echo", state)
		}
	} else if len(rollTestWrites(fixture)) != 0 {
		t.Fatal("ambiguous admission both rejected and wrote", err, rollTestWrites(fixture))
	}
}

func TestGroupRollUncertainWriteCannotReplayAndExactEchoCanResolve(t *testing.T) {
	service, fixture, advance := newRollTest(t)
	request := addRollTest(t, service, rollTestGUID, 15)
	fixture.sendError = true
	state, err := service.Roll(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	receipt := receiptRollTest(t, state, request.RequestId)
	if !receipt.TransportAttempted || receipt.TransportWritten || receipt.ServerVoteObserved || len(rollTestWrites(fixture)) != 1 {
		t.Fatal(receipt, rollTestWrites(fixture))
	}
	fixture.sendError = false
	another := proto.Clone(request).(*inventoryv1.RollRequest)
	another.RequestId = "ambiguous-second-request"
	if _, err := service.Roll(context.Background(), another); err == nil || len(rollTestWrites(fixture)) != 1 {
		t.Fatal("a different request bypassed uncertain offer custody", err, rollTestWrites(fixture))
	}
	for range 2 {
		if _, err := service.Roll(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	advance(time.Second)
	rollTestPacket(t, service, opcode.SMSGLootRoll, rollTestEcho(6, rollTestEntry, 17, 23, 0, 0, 0))
	receipt = receiptRollTest(t, readRollTest(t, service), request.RequestId)
	if !receipt.ServerVoteObserved || receipt.TransportWritten || len(rollTestWrites(fixture)) != 1 {
		t.Fatal("later evidence changed transport history or repeated the vote", receipt, rollTestWrites(fixture))
	}
}

func TestGroupRollSynchronousEchoSurvivesAmbiguousTransportReply(t *testing.T) {
	service, fixture, _ := newRollTest(t)
	request := addRollTest(t, service, rollTestGUID, 15)
	fixture.sendError = true
	fixture.afterSend = func(packet *modv1.SendPacketRequest) {
		if packet.Opcode == opcode.CMSGLootRoll {
			rollTestPacket(t, service, opcode.SMSGLootRoll, rollTestEcho(6, rollTestEntry, 17, 23, 0, 0, 0))
		}
	}
	state, err := service.Roll(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	receipt := receiptRollTest(t, state, request.RequestId)
	if !receipt.ServerVoteObserved || receipt.TransportWritten || len(rollTestWrites(fixture)) != 1 {
		t.Fatal(receipt, rollTestWrites(fixture))
	}
}

func TestGroupRollConcurrentDifferentRequestsReserveOneWrite(t *testing.T) {
	service, fixture, _ := newRollTest(t)
	request := addRollTest(t, service, rollTestGUID, 15)
	other := proto.Clone(request).(*inventoryv1.RollRequest)
	other.RequestId = "other-vote"
	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, value := range []*inventoryv1.RollRequest{request, other} {
		wait.Add(1)
		go func(value *inventoryv1.RollRequest) {
			defer wait.Done()
			<-start
			_, _ = service.Roll(context.Background(), value)
		}(value)
	}
	close(start)
	wait.Wait()
	if packets := rollTestWrites(fixture); len(packets) != 1 || packets[0].Opcode != opcode.CMSGLootRoll {
		t.Fatal("concurrent votes duplicated transport", packets)
	}
}

func TestGroupRollWorldTransferRetainsUncertainVoteWithoutRebinding(t *testing.T) {
	service, fixture, _ := newRollTest(t)
	request := addRollTest(t, service, rollTestGUID, 15)
	if _, err := service.Roll(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	rollTestPacket(t, service, opcode.SMSGTransferPending, make([]byte, 4))
	rollTestPacket(t, service, opcode.SMSGNewWorld, rollTestEntryPacket())
	fixture.w.VisitRevision = 2
	rollTestPacket(t, service, opcode.SMSGLootRoll, rollTestEcho(6, rollTestEntry, 17, 23, 0, 0, 0))
	state := readRollTest(t, service)
	if receiptRollTest(t, state, request.RequestId).ServerVoteObserved {
		t.Fatal("new visit rebound old vote to an echo", state)
	}
	for _, offer := range state.Offers {
		if offer.RollGuid == rollTestGUID && offer.Available {
			t.Fatal("prior-visit roll remains available", offer)
		}
	}
	if _, err := service.Roll(context.Background(), request); err != nil {
		t.Fatal("retained receipt unavailable after visit replacement", err)
	}
	if len(rollTestWrites(fixture)) != 1 {
		t.Fatal("transfer replayed old vote", rollTestWrites(fixture))
	}
}

func TestGroupRollMalformedOfferCannotCreateVotingAuthority(t *testing.T) {
	for _, name := range []string{"short", "trailing", "empty guid", "wrong guid kind", "invalid slot", "empty entry", "empty count", "empty timer", "timer bound", "empty mask", "unknown mask bit"} {
		t.Run(name, func(t *testing.T) {
			service, fixture, _ := newRollTest(t)
			body := rollTestOffer(rollTestGUID, rollTestEntry, 17, 23, 60000, 15)
			switch name {
			case "short":
				body = body[:len(body)-1]
			case "trailing":
				body = append(body, 0)
			case "empty guid":
				binary.LittleEndian.PutUint64(body, 0)
			case "wrong guid kind":
				binary.LittleEndian.PutUint64(body, 6)
			case "invalid slot":
				binary.LittleEndian.PutUint32(body[12:], 256)
			case "empty entry":
				binary.LittleEndian.PutUint32(body[16:], 0)
			case "empty count":
				binary.LittleEndian.PutUint32(body[28:], 0)
			case "empty timer":
				binary.LittleEndian.PutUint32(body[32:], 0)
			case "timer bound":
				binary.LittleEndian.PutUint32(body[32:], 60001)
			case "empty mask":
				body[36] = 0
			case "unknown mask bit":
				body[36] = 31
			}
			rollTestPacket(t, service, opcode.SMSGLootStartRoll, body)
			if state := readRollTest(t, service); len(state.Offers) != 0 || len(state.Receipts) != 0 || len(rollTestWrites(fixture)) != 0 {
				t.Fatal("malformed offer admitted a vote", state, rollTestWrites(fixture))
			}
		})
	}
}

func TestGroupRollMalformedEchoAndDeadlineNeverManufactureReceipt(t *testing.T) {
	service, fixture, advance := newRollTest(t)
	request := addRollTest(t, service, rollTestGUID, 15)
	if _, err := service.Roll(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{
		rollTestEcho(6, rollTestEntry, 17, 23, 0, 0, 0)[:34],
		append(rollTestEcho(6, rollTestEntry, 17, 23, 0, 0, 0), 0),
	} {
		rollTestPacket(t, service, opcode.SMSGLootRoll, body)
		if receiptRollTest(t, readRollTest(t, service), request.RequestId).ServerVoteObserved {
			t.Fatal("incomplete or extended echo confirmed vote")
		}
	}
	advance(time.Minute)
	service.Tick(context.Background())
	if receiptRollTest(t, readRollTest(t, service), request.RequestId).ServerVoteObserved {
		t.Fatal("deadline invented vote confirmation")
	}
	if _, err := service.Roll(context.Background(), request); err != nil || len(rollTestWrites(fixture)) != 1 {
		t.Fatal("timed-out vote was retried or its receipt lost", err, rollTestWrites(fixture))
	}
	other := proto.Clone(request).(*inventoryv1.RollRequest)
	other.RequestId = "after-deadline"
	if _, err := service.Roll(context.Background(), other); err == nil || len(rollTestWrites(fixture)) != 1 {
		t.Fatal("new request bypassed exact-offer custody after deadline", err, rollTestWrites(fixture))
	}
}

func TestGroupRollConflictingOfferAndCapacityKeepEchoUnconfirmed(t *testing.T) {
	for _, name := range []string{"conflicting tuple", "unobserved overflow tuple"} {
		t.Run(name, func(t *testing.T) {
			service, fixture, _ := newRollTest(t)
			request := addRollTest(t, service, rollTestGUID, 15)
			if _, err := service.Roll(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if name == "conflicting tuple" {
				rollTestPacket(t, service, opcode.SMSGLootStartRoll, rollTestOffer(rollTestGUID, rollTestEntry+1, 17, 23, 60000, 15))
			} else {
				for index := uint64(1); index <= 16; index++ {
					rollTestPacket(t, service, opcode.SMSGLootStartRoll, rollTestOffer(rollTestGUID+index, rollTestEntry+uint32(index), 17, 23, 60000, 15))
				}
			}
			rollTestPacket(t, service, opcode.SMSGLootRoll, rollTestEcho(6, rollTestEntry, 17, 23, 0, 0, 0))
			state := readRollTest(t, service)
			if receiptRollTest(t, state, request.RequestId).ServerVoteObserved {
				t.Fatal("incomplete offer set or contradicted tuple confirmed the vote", state)
			}
			for _, offer := range state.Offers {
				if name == "unobserved overflow tuple" && offer.Available || offer.RollGuid == rollTestGUID && offer.Available {
					t.Fatal("retired offer remained actionable", offer)
				}
			}
			if len(rollTestWrites(fixture)) != 1 {
				t.Fatal("retirement wrote another vote", rollTestWrites(fixture))
			}
		})
	}
}

func TestGroupRollContradictedIdentifierCannotAliasAnotherVoteEcho(t *testing.T) {
	service, fixture, _ := newRollTest(t)
	first := addRollTest(t, service, rollTestGUID, 15)
	rollTestPacket(t, service, opcode.SMSGLootStartRoll, rollTestOffer(rollTestGUID+1, rollTestEntry+1, 17, 23, 60000, 15))
	var second *inventoryv1.RollRequest
	for _, offer := range readRollTest(t, service).Offers {
		if offer.RollGuid == rollTestGUID+1 {
			second = &inventoryv1.RollRequest{SessionId: "body", RequestId: "another-tuple", RollGuid: offer.RollGuid,
				ExpectedOfferRevision: offer.Revision, ExpectedLootSlot: offer.LootSlot, VisitId: offer.VisitId, Choice: "need"}
		}
	}
	if second == nil {
		t.Fatal("second distinct offer was not observed")
	}
	for _, request := range []*inventoryv1.RollRequest{first, second} {
		if _, err := service.Roll(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	// The same source identifier now claims the other offer's echo tuple.
	// Retaining only the old tuple would make that other vote falsely unique.
	rollTestPacket(t, service, opcode.SMSGLootStartRoll, rollTestOffer(rollTestGUID, rollTestEntry+1, 17, 23, 60000, 15))
	rollTestPacket(t, service, opcode.SMSGLootRoll, rollTestEcho(6, rollTestEntry+1, 17, 23, 0, 0, 0))
	state := readRollTest(t, service)
	for _, request := range []*inventoryv1.RollRequest{first, second} {
		if receiptRollTest(t, state, request.RequestId).ServerVoteObserved {
			t.Fatal("a contradicted offer manufactured an unambiguous echo", state)
		}
	}
	for _, offer := range state.Offers {
		if offer.Available {
			t.Fatal("contradicted visit retained voting authority", offer)
		}
	}
	if len(rollTestWrites(fixture)) != 2 {
		t.Fatal("offer contradiction repeated a vote", rollTestWrites(fixture))
	}
}
