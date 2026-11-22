package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func newFlightpathActivationRig(t *testing.T) *flightpathRig {
	t.Helper()
	x := newFlightpathRig(t)
	x.npc = 0xf13000012300002a
	x.f.world.NearbyCreatures[0].Guid, x.f.world.NearbyCreatures[0].Entry = x.npc, 0x123
	x.query(t, "status")
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
	x.query(t, "discover")
	x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 12, 2, 12, 448))
	if menu := x.observation(t).Operation; menu.GetPending() || !menu.GetMenuKnown() || menu.GetPhase() != "menu_observed" {
		t.Fatal("activation fixture lacks an actual terminal menu", menu)
	}
	x.f.packets = nil
	x.client.worldReads, x.client.sendCalls, x.client.order = 0, 0, nil
	return x
}

func flightpathActivationRequest(t *testing.T, x *flightpathRig) *actionsv1.FlightpathActivationRequest {
	t.Helper()
	view := x.observation(t)
	return &actionsv1.FlightpathActivationRequest{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID,
		VisitId: 1, Token: x.token, NpcGuid: x.npc, SourceNode: 12, DestinationNode: 2,
		MenuRevision: view.Revision, ExpectedRevision: view.ActivationRevision,
		ExpectedCopperRevision: x.f.world.SelfCopperRevision, ExpectedCopper: x.f.world.SelfCopper, AcceptUnknownFare: true}
}

func flightpathActivate(t *testing.T, x *flightpathRig, r *actionsv1.FlightpathActivationRequest) *actionsv1.FlightpathObservationResponse {
	t.Helper()
	view, err := x.s.ActivateFlightpath(context.Background(), r)
	if err != nil || view.GetActivation() == nil {
		t.Fatal("activation did not return its retained receipt", view, err)
	}
	return view
}

func assertFlightpathActivationNoInventedEffects(t *testing.T, op *actionsv1.FlightpathActivationOperation) {
	t.Helper()
	if op == nil || op.LiveFareKnown || op.LiveRouteKnown || op.FlightStartedKnown || op.ArrivalKnown {
		t.Fatal("activation invented a live quote, route, flight start or arrival", op)
	}
}

func assertFlightpathActivationNotDispatched(t *testing.T, view *actionsv1.FlightpathObservationResponse, r *actionsv1.FlightpathActivationRequest) {
	t.Helper()
	op := view.GetActivation()
	if op == nil || view.ActivationRevision != r.ExpectedRevision+1 || op.Revision != view.ActivationRevision ||
		op.SessionId != r.SessionId || op.SelfGuid != r.SelfGuid || op.VisitId != r.VisitId || op.NpcGuid != r.NpcGuid ||
		op.MenuRevision != r.MenuRevision || op.SourceNode != r.SourceNode || op.DestinationNode != r.DestinationNode ||
		op.Phase != "not_dispatched" || op.Pending || op.TransportWritten || op.TransportUnknown || op.ReplyKnown || op.ActivationAccepted ||
		op.Error == "" || len(op.Error) > 240 {
		t.Fatal("known refusal lost its exact zero-send activation receipt", view)
	}
	for _, b := range []byte(op.Error) {
		if b < 32 || b > 126 {
			t.Fatal("activation refusal retained non-ASCII diagnostic bytes", op.Error)
		}
	}
	assertFlightpathActivationNoInventedEffects(t, op)
}

func TestFlightpathActivationNormalWireReservationAndAcceptedIsStillPending(t *testing.T) {
	x := newFlightpathActivationRig(t)
	r := flightpathActivationRequest(t, x)
	beforeMenu := proto.Clone(x.s.flightpath.operation)
	x.client.beforeWorld = func() {
		op := x.s.flightpathActivation.operation
		if op == nil || op.Revision != 1 || !op.Pending || op.Phase != "reserved" || op.MoneyBeforeKnown ||
			x.s.flightpathActivation.request == nil || !proto.Equal(x.s.flightpathActivation.request, r) {
			t.Fatal("activation intent was not reserved before World", op)
		}
	}
	x.client.beforeSend = func(p *modv1.SendPacketRequest) {
		op := x.s.flightpathActivation.operation
		if op.GetPhase() != "requested" || !op.GetPending() || !op.MoneyBeforeKnown || op.MoneyBeforeCopper != 12345 ||
			op.MoneyBeforeRevision != 7 || op.TransportWritten || op.TransportUnknown {
			t.Fatal("send lacked its exact admitted receipt and observed wallet", op, p)
		}
	}
	view := flightpathActivate(t, x, r)
	wire, err := hex.DecodeString("2a000023010030f10c00000002000000")
	if err != nil {
		t.Fatal(err)
	}
	if x.client.worldReads != 1 || x.client.sendCalls != 1 || len(x.f.packets) != 1 ||
		x.f.packets[0].Opcode != opcode.CMSGActivatetaxi || !bytes.Equal(x.f.packets[0].Payload, wire) ||
		view.Activation.GetPhase() != "requested" || !view.Activation.Pending || !view.Activation.TransportWritten ||
		x.f.world.SelfCopper != 12345 || x.f.world.SelfCopperRevision != 7 || x.s.state.Moving || x.s.state.Traveling ||
		!proto.Equal(x.s.flightpath.operation, beforeMenu) {
		t.Fatal("activation wrote a nonnormal packet or fabricated an effect", view, x.f.packets)
	}
	if len(x.client.order) != 2 || x.client.order[0] != "world" || x.client.order[1] != "send" {
		t.Fatal("activation performed another dependency lookup after admission", x.client.order)
	}
	x.packet(t, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0})
	op := x.observation(t).Activation
	if !op.Pending || op.Phase != "accepted_unsettled" || !op.ReplyKnown || op.ReplyCode != 0 || !op.ActivationAccepted {
		t.Fatal("OK reply was treated as start/arrival or safe retry", op)
	}
	assertFlightpathActivationNoInventedEffects(t, op)
	view = x.observation(t)
	view.Activation.Phase, view.Activation.DestinationNode = "caller-mutation", 448
	if current := x.observation(t).Activation; current.Phase != "accepted_unsettled" || current.DestinationNode != 2 {
		t.Fatal("returned activation aliases native receipt custody", current)
	}
}

func TestFlightpathActivationRequiresCurrentMenuAndExplicitUnknownFareConsent(t *testing.T) {
	for _, change := range []string{"no fare consent", "source zero", "destination449", "same node", "wallet revision zero", "stale menu", "wrong source",
		"unknown destination", "unknown source bit", "menu pending", "no menu proof", "menu wrong mode", "menu no send history", "menu foreign NPC", "wrong body", "wrong token"} {
		t.Run(change, func(t *testing.T) {
			x := newFlightpathActivationRig(t)
			r := flightpathActivationRequest(t, x)
			want := codes.FailedPrecondition
			switch change {
			case "no fare consent":
				r.AcceptUnknownFare, want = false, codes.InvalidArgument
			case "source zero":
				r.SourceNode, want = 0, codes.InvalidArgument
			case "destination449":
				r.DestinationNode, want = 449, codes.InvalidArgument
			case "same node":
				r.DestinationNode, want = r.SourceNode, codes.InvalidArgument
			case "wallet revision zero":
				r.ExpectedCopperRevision, want = 0, codes.InvalidArgument
			case "stale menu":
				r.MenuRevision++
			case "wrong source":
				r.SourceNode = 448
			case "unknown destination":
				r.DestinationNode = 13
			case "unknown source bit":
				x.s.flightpath.operation.KnownNodes = []uint32{2, 448}
			case "menu pending":
				x.s.flightpath.operation.Pending = true
			case "no menu proof":
				x.s.flightpath.operation.MenuKnown = false
			case "menu wrong mode":
				x.s.flightpath.operation.Mode = "status"
			case "menu no send history":
				x.s.flightpath.operation.TransportWritten = false
			case "menu foreign NPC":
				x.s.flightpath.operation.NpcGuid++
			case "wrong body":
				r.SelfGuid++
			case "wrong token":
				r.Token = "unowned-token"
			}
			if _, err := x.s.ActivateFlightpath(context.Background(), r); status.Code(err) != want || x.client.worldReads != 0 ||
				x.client.sendCalls != 0 || x.s.flightpathActivation.revision != 0 {
				t.Fatal("invalid activation reached callbacks or reservation", change, err)
			}
		})
	}
}

func TestFlightpathActivationExactCASReplayAndConflictNeverReadsWorldOrWrites(t *testing.T) {
	x := newFlightpathActivationRig(t)
	r := flightpathActivationRequest(t, x)
	want := flightpathActivate(t, x, r)
	reads, sends := x.client.worldReads, x.client.sendCalls
	for _, settled := range []bool{false, true} {
		if settled {
			x.packet(t, opcode.SMSGActivatetaxireply, []byte{5, 0, 0, 0})
			want = x.observation(t)
		}
		again, err := x.s.ActivateFlightpath(context.Background(), proto.Clone(r).(*actionsv1.FlightpathActivationRequest))
		if err != nil || !proto.Equal(again, want) || x.client.worldReads != reads || x.client.sendCalls != sends {
			t.Fatal("exact retry repeated or lost its retained operation", again, err)
		}
		conflict := proto.Clone(r).(*actionsv1.FlightpathActivationRequest)
		conflict.DestinationNode = 448
		if _, err := x.s.ActivateFlightpath(context.Background(), conflict); status.Code(err) != codes.Aborted ||
			x.client.worldReads != reads || x.client.sendCalls != sends {
			t.Fatal("conflicting payload reused a consumed activation CAS", err)
		}
	}
	// A definitive rejection permits a new explicitly selected CAS, never an
	// automatic retry of the original request.
	next := flightpathActivationRequest(t, x)
	next.DestinationNode = 448
	view := flightpathActivate(t, x, next)
	if view.ActivationRevision != 2 || x.client.sendCalls != sends+1 || !view.Activation.Pending {
		t.Fatal("new explicit CAS after rejection did not retain its own attempt", view)
	}
	if _, err := x.s.ActivateFlightpath(context.Background(), r); status.Code(err) != codes.Aborted {
		t.Fatal("an older replaced activation CAS became replayable", err)
	}
}

func TestFlightpathActivationLateWorldAndClockChangesRetainZeroSendReceipts(t *testing.T) {
	for _, seam := range []string{"world", "clock"} {
		for _, change := range []string{"lease", "session", "body", "visit", "control", "cast", "moving", "menu pointer", "menu contents", "menu revision", "NaN", "Inf", "expiry"} {
			t.Run(seam+"/"+change, func(t *testing.T) {
				x := newFlightpathActivationRig(t)
				r := flightpathActivationRequest(t, x)
				injected, armed := false, false
				var replacement *actionsv1.FlightpathOperation
				mutate := func() {
					injected = true
					switch change {
					case "lease":
						x.s.interactionToken = "replacement-lease"
					case "session":
						x.s.sessionID = "replacement-session"
					case "body":
						x.s.selfGUID++
					case "visit":
						x.s.encounterVisit.revision++
					case "control":
						x.s.clientControlLost = true
					case "cast":
						x.s.state.CastPending = true
					case "moving":
						x.s.state.Moving = true
					case "menu pointer":
						replacement = proto.Clone(x.s.flightpath.operation).(*actionsv1.FlightpathOperation)
						x.s.flightpath.operation = replacement
					case "menu contents":
						x.s.flightpath.operation.KnownNodes = []uint32{12, 448}
					case "menu revision":
						x.s.flightpath.revision++
					case "NaN":
						x.s.pos.x = float32(math.NaN())
					case "Inf":
						x.s.pos.x = float32(math.Inf(1))
					case "expiry":
						x.advance(15 * time.Second)
					}
				}
				clock := x.s.now
				x.client.beforeWorld = func() {
					if op := x.s.flightpathActivation.operation; op.GetPhase() != "reserved" || !op.GetPending() {
						t.Fatal("late callback preceded reservation", op)
					}
					armed = true
					if seam == "world" {
						mutate()
					}
				}
				x.s.now = func() time.Time {
					if seam == "clock" && armed && !injected {
						mutate()
					}
					return clock()
				}
				view := flightpathActivate(t, x, r)
				if !injected || x.client.worldReads != 1 || x.client.sendCalls != 0 || len(x.f.packets) != 0 {
					t.Fatal("late mutation reached activation transport", view, x.client.order)
				}
				assertFlightpathActivationNotDispatched(t, view, r)
				if change == "lease" && x.s.interactionToken != "replacement-lease" ||
					replacement != nil && x.s.flightpath.operation != replacement {
					t.Fatal("refusal erased replacement custody")
				}
			})
		}
	}
}

func TestFlightpathActivationActualWalletAndWorldAdmissionBeforeSoleSend(t *testing.T) {
	for _, change := range []string{"wallet unknown", "wallet revision", "wallet value", "inferred position", "world body", "world visit", "NPC missing", "NPC range", "NPC moving", "combat", "channel", "world failure"} {
		t.Run(change, func(t *testing.T) {
			x := newFlightpathActivationRig(t)
			r := flightpathActivationRequest(t, x)
			x.client.beforeWorld = func() {
				switch change {
				case "wallet unknown":
					x.f.world.SelfCopperKnown = false
				case "wallet revision":
					x.f.world.SelfCopperRevision++
				case "wallet value":
					x.f.world.SelfCopper--
				case "inferred position":
					x.f.world.Self.PositionInferred = true
				case "world body":
					x.f.world.Self.Guid++
				case "world visit":
					x.f.world.VisitRevision++
				case "NPC missing":
					x.f.world.NearbyCreatures = nil
				case "NPC range":
					x.f.world.NearbyCreatures[0].X = 6
				case "NPC moving":
					x.f.world.NearbyCreatures[0].MovementFlags = 1
				case "combat":
					x.f.world.Self.UnitFlags = 0x80000
				case "channel":
					x.f.world.Self.ChannelSpellKnown = false
				case "world failure":
					x.f.failSnapshot = true
				}
			}
			view := flightpathActivate(t, x, r)
			assertFlightpathActivationNotDispatched(t, view, r)
			if x.client.worldReads != 1 || x.client.sendCalls != 0 || len(x.f.packets) != 0 {
				t.Fatal("failed final World proof reached transport", change, view)
			}
			reads := x.client.worldReads
			again, err := x.s.ActivateFlightpath(context.Background(), r)
			if err != nil || !proto.Equal(again, view) || x.client.worldReads != reads {
				t.Fatal("retained zero-send receipt retried its old CAS", again, err)
			}
		})
	}
	t.Run("known_zero_is_not_an_invented_fare_check", func(t *testing.T) {
		x := newFlightpathActivationRig(t)
		x.f.world.SelfCopper = 0
		r := flightpathActivationRequest(t, x)
		view := flightpathActivate(t, x, r)
		if x.client.sendCalls != 1 || !view.Activation.MoneyBeforeKnown || view.Activation.MoneyBeforeCopper != 0 ||
			view.Activation.MoneyBeforeRevision != 7 || !view.Activation.Pending {
			t.Fatal("native activation invented an affordability decision from unknown fare", view)
		}
		assertFlightpathActivationNoInventedEffects(t, view.Activation)
	})
}

func TestFlightpathActivationClockWalletReceiptUsesFinalGuardedObservation(t *testing.T) {
	for _, change := range []string{"known", "value", "revision", "becomes expected"} {
		t.Run(change, func(t *testing.T) {
			x := newFlightpathActivationRig(t)
			r := flightpathActivationRequest(t, x)
			if change == "becomes expected" {
				x.f.world.SelfCopper--
			}
			priorWorld, clock, injected := x.s.lastWorld, x.s.now, false
			x.s.now = func() time.Time {
				if !injected && x.s.lastWorld != priorWorld && x.s.flightpathActivation.operation.GetPhase() == "reserved" {
					injected = true
					w := x.s.lastWorld
					switch change {
					case "known":
						w.SelfCopperKnown = false
					case "value":
						w.SelfCopper--
					case "revision":
						w.SelfCopperRevision++
					case "becomes expected":
						w.SelfCopper = r.ExpectedCopper
					}
				}
				return clock()
			}
			view := flightpathActivate(t, x, r)
			if !injected || x.client.worldReads != 1 {
				t.Fatal("wallet regression did not inject after the actual decoded World read", view)
			}
			if change == "becomes expected" {
				if x.client.sendCalls != 1 || !view.Activation.MoneyBeforeKnown || view.Activation.MoneyBeforeCopper != r.ExpectedCopper ||
					view.Activation.MoneyBeforeRevision != r.ExpectedCopperRevision {
					t.Fatal("admitted receipt retained wallet facts from before its final callback", view)
				}
				return
			}
			assertFlightpathActivationNotDispatched(t, view, r)
			if x.client.sendCalls != 0 || view.Activation.MoneyBeforeKnown != x.s.lastWorld.SelfCopperKnown {
				t.Fatal("changed wallet reached transport or retained stale knownness", view)
			}
			if view.Activation.MoneyBeforeKnown && (view.Activation.MoneyBeforeCopper != x.s.lastWorld.SelfCopper ||
				view.Activation.MoneyBeforeRevision != x.s.lastWorld.SelfCopperRevision) {
				t.Fatal("refusal receipt does not describe its final observed wallet", view)
			}
		})
	}
}

func TestFlightpathActivationUnknownAndAcceptedCustodySurviveExpiryReloadTickAndVisitChange(t *testing.T) {
	for _, outcome := range []string{"lost write", "accepted", "unknown reply"} {
		t.Run(outcome, func(t *testing.T) {
			x := newFlightpathActivationRig(t)
			r := flightpathActivationRequest(t, x)
			x.f.failSend = outcome == "lost write"
			flightpathActivate(t, x, r)
			x.f.failSend = false
			if outcome == "accepted" {
				x.packet(t, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0})
			} else if outcome == "unknown reply" {
				x.packet(t, opcode.SMSGActivatetaxireply, []byte{0xff, 0xff, 0xff, 0xff})
			}
			before := x.observation(t).Activation
			if !before.Pending || outcome == "lost write" && (!before.TransportUnknown || before.TransportWritten) {
				t.Fatal("uncertain activation lost pending custody", before)
			}
			// Prove the pending guards while the exact lease is still valid;
			// expired-token rejection alone would not test mutual exclusion.
			readsBefore, sendsBefore := x.client.worldReads, x.client.sendCalls
			if _, err := x.s.ActivateFlightpath(context.Background(), flightpathActivationRequest(t, x)); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("new activation bypassed pending custody under a valid lease", err)
			}
			if _, err := x.s.QueryFlightpath(context.Background(), x.request(t, "status")); status.Code(err) != codes.FailedPrecondition ||
				x.client.worldReads != readsBefore || x.client.sendCalls != sendsBefore {
				t.Fatal("new query bypassed pending activation under a valid lease", err)
			}
			x.advance(time.Hour)
			if _, err := x.s.Status(context.Background(), &emptypb.Empty{}); err != nil || x.s.state.InteractionOwner != "" {
				t.Fatal("fixture lease did not expire", err)
			}
			x.s.Tick(context.Background())
			var revision string
			for _, source := range []string{"return {decide=function(input) return {} end}", "return {decide=function(input) return {} end} -- edit"} {
				view, err := x.s.ReloadBehavior(context.Background(), &actionsv1.BehaviorReloadRequest{SessionId: x.s.sessionID,
					SelfGuid: x.s.selfGUID, ExpectedRevision: revision, Source: source})
				if err != nil || view.Revision == revision {
					t.Fatal("fixture did not reload actual Lua source", view, err)
				}
				revision = view.Revision
			}
			if !proto.Equal(x.observation(t).Activation, before) || x.client.sendCalls != 1 {
				t.Fatal("time, Tick, lease expiry or source edit erased/replayed activation", x.observation(t))
			}
			reads := x.client.worldReads
			again, err := x.s.ActivateFlightpath(context.Background(), r)
			if err != nil || !proto.Equal(again.Activation, before) || x.client.worldReads != reads || x.client.sendCalls != 1 {
				t.Fatal("unresolved exact retry reached physical dependencies", again, err)
			}
			if _, err := x.s.ActivateFlightpath(context.Background(), flightpathActivationRequest(t, x)); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("new activation bypassed pending custody", err)
			}
			if _, err := x.s.QueryFlightpath(context.Background(), x.request(t, "status")); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("new query bypassed pending activation custody", err)
			}
			x.packet(t, opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
			x.packet(t, opcode.SMSGActivatetaxireply, []byte{1, 0, 0, 0})
			entry := binary.LittleEndian.AppendUint32(nil, x.f.world.Self.MapId)
			for _, v := range []float32{x.f.world.Self.X, x.f.world.Self.Y, x.f.world.Self.Z, x.f.world.Self.Orientation} {
				entry = binary.LittleEndian.AppendUint32(entry, math.Float32bits(v))
			}
			x.packet(t, opcode.SMSGNewWorld, entry)
			x.packet(t, opcode.SMSGActivatetaxireply, []byte{1, 0, 0, 0})
			if !proto.Equal(x.observation(t).Activation, before) || x.client.sendCalls != 1 {
				t.Fatal("old-visit reply settled an unresolved activation", x.observation(t))
			}
		})
	}
}

func TestFlightpathActivationReplyRequiresOwnSendAndKeepsFirstExactResult(t *testing.T) {
	for _, code := range []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, math.MaxUint32} {
		t.Run(fmt.Sprintf("code%d", code), func(t *testing.T) {
			x := newFlightpathActivationRig(t)
			r := flightpathActivationRequest(t, x)
			x.f.failSend = true // an ambiguous send can still have an actual reply
			x.client.beforeWorld = func() {
				if handled, err := x.s.flightpathActivationPacketLocked(opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0}); !handled || err != nil || x.s.flightpathActivation.operation.ReplyKnown {
					t.Fatal("pre-send incidental reply acquired receipt authority", handled, err)
				}
			}
			x.client.beforeSend = func(_ *modv1.SendPacketRequest) {
				if handled, err := x.s.flightpathActivationPacketLocked(opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0}); !handled || err != nil || x.s.flightpathActivation.operation.ReplyKnown {
					t.Fatal("send-entry reply without written/unknown transport acquired authority", handled, err)
				}
			}
			flightpathActivate(t, x, r)
			before := x.observation(t).Activation
			for _, malformed := range [][]byte{nil, {0}, {0, 0, 0}, {0, 0, 0, 0, 0}} {
				x.packet(t, opcode.SMSGActivatetaxireply, malformed)
				if !proto.Equal(x.observation(t).Activation, before) {
					t.Fatal("malformed reply settled activation", malformed)
				}
			}
			x.packet(t, opcode.SMSGActivatetaxireply, binary.LittleEndian.AppendUint32(nil, code))
			op := x.observation(t).Activation
			if !op.ReplyKnown || op.ReplyCode != code || !op.TransportUnknown || op.TransportWritten || op.ActivationAccepted != (code == 0) ||
				op.Pending != (code == 0 || code > 12) || code == 0 && op.Phase != "accepted_unsettled" ||
				code >= 1 && code <= 12 && op.Phase != "server_rejected" || code > 12 && op.Phase != "reply_unknown" {
				t.Fatal("actual activation result was misclassified", code, op)
			}
			assertFlightpathActivationNoInventedEffects(t, op)
			x.packet(t, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0})
			if !proto.Equal(x.observation(t).Activation, op) || x.client.sendCalls != 1 {
				t.Fatal("duplicate result changed or replayed the first activation receipt", x.observation(t))
			}
		})
	}
}

func TestFlightpathActivationReplyCannotCrossCurrentSessionBodyOrVisit(t *testing.T) {
	for _, change := range []string{"session", "body", "visit", "unbound"} {
		t.Run(change, func(t *testing.T) {
			x := newFlightpathActivationRig(t)
			before := flightpathActivate(t, x, flightpathActivationRequest(t, x)).Activation
			switch change {
			case "session":
				x.s.sessionID = "new-session"
			case "body":
				x.s.selfGUID++
			case "visit":
				x.s.encounterVisit.revision++
			case "unbound":
				x.s.encounterVisit.bound = false
			}
			x.packet(t, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0})
			if !proto.Equal(x.observation(t).Activation, before) || x.client.sendCalls != 1 {
				t.Fatal("uncorrelated reply crossed body/session/visit custody", x.observation(t))
			}
		})
	}
}

func TestFlightpathActivationTypedRefusalAndCanceledRequestNeverReplay(t *testing.T) {
	x := newFlightpathActivationRig(t)
	r := flightpathActivationRequest(t, x)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.s.ActivateFlightpath(ctx, r); status.Code(err) != codes.Canceled || x.client.worldReads != 0 || x.s.flightpathActivation.revision != 0 {
		t.Fatal("canceled input reached reservation or World", err)
	}
	x.client.rejectSend = true
	view := flightpathActivate(t, x, r)
	assertFlightpathActivationNotDispatched(t, view, r)
	if x.client.sendCalls != 1 || len(x.f.packets) != 0 {
		t.Fatal("typed refusal reached game transport", view)
	}
	reads := x.client.worldReads
	if again, err := x.s.ActivateFlightpath(context.Background(), r); err != nil || !proto.Equal(again, view) ||
		x.client.worldReads != reads || x.client.sendCalls != 1 {
		t.Fatal("typed refusal retried instead of returning its consumed CAS", again, err)
	}
	x.packet(t, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0})
	if !proto.Equal(x.observation(t).Activation, view.Activation) {
		t.Fatal("unsent activation consumed an unrelated acceptance reply")
	}
}
