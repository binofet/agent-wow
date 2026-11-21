package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Outgoing target serialization is tested by the generic caster integration.
// This fixture begins at its admitted operation and feeds real channel frames.
func pendingChannelFixture(t *testing.T, spell uint32, target uint64, channel bool) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := buffRig(t, 8, spell)
	activateEncounterWriter(t, s, f)
	s.castCount = 9
	s.pendingSpell, s.pendingTarget, s.state.CastPending = spell, target, true
	s.supportOp = &supportAttempt{view: actionsv1.SpellOperation{SpellId: spell, TargetGuid: target, Capability: "cast_spell", Phase: "waiting_server", TransportWritten: true}, count: s.castCount, until: s.now().Add(3 * time.Second), policySpellToken: "original-cast-token"}
	if err := s.initializeSpellLifecycleLocked(s.supportOp, channel, f.world.VisitRevision); err != nil {
		t.Fatal(err)
	}
	return s, f, advance
}
func channelFrame(actor uint64, spell, remaining uint32, start bool) []byte {
	p := packedGUID(actor)
	if start {
		p = binary.LittleEndian.AppendUint32(p, spell)
	}
	return binary.LittleEndian.AppendUint32(p, remaining)
}
func emitChannel(t *testing.T, s *Service, start bool, actor uint64, spell, remaining uint32) {
	t.Helper()
	code := uint32(opcode.MSGChannelUpdate)
	if start {
		code = opcode.MSGChannelStart
	}
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: code, Payload: channelFrame(actor, spell, remaining, start)}); err != nil {
		t.Fatal(err)
	}
}
func TestChannelLifecycleBlizzardGroundAndUnitTargetUseSameActualFrames(t *testing.T) {
	for _, test := range []struct {
		name   string
		spell  uint32
		target uint64
	}{{"ground_blizzard", 10, 0}, {"unit_drain", 689, enemyGUID}} {
		t.Run(test.name, func(t *testing.T) {
			s, f, _ := pendingChannelFixture(t, test.spell, test.target, true)
			emitChannel(t, s, true, s.selfGUID, test.spell, 8000)
			if !s.state.CastPending || !s.supportOp.view.ChannelStarted || s.supportOp.view.ChannelRemainingMs != 8000 || s.supportOp.view.Phase != "channeling" {
				t.Fatal("non-self target never entered channel lifecycle", s.stateLocked())
			}
			emitChannel(t, s, false, s.selfGUID, 0, 4500)
			if s.supportOp.view.ChannelRemainingMs != 4500 || s.supportOp.until != s.now().Add(7500*time.Millisecond) {
				t.Fatal("server channel update not retained", s.supportOp)
			}
			emitChannel(t, s, false, s.selfGUID, 0, 0)
			if s.state.CastPending || !s.supportOp.view.ChannelEnded || s.supportOp.view.ServerCastGo || s.supportOp.view.EffectObserved || s.supportOp.view.Phase != "channel_ended_effect_unverified" {
				t.Fatal("actual end lost custody or invented GO/effect", s.stateLocked())
			}
			if len(f.packets) != 0 {
				t.Fatal("observation authored a movement/cast packet", f.packets)
			}
		})
	}
}
func TestChannelLifecycleForeignMalformedDuplicateAndExpiryStayMechanical(t *testing.T) {
	s, f, advance := pendingChannelFixture(t, 10, 0, true)
	emitChannel(t, s, true, s.selfGUID+1, 10, 8000)
	emitChannel(t, s, true, s.selfGUID, 11, 8000)
	if s.supportOp.view.ChannelStarted {
		t.Fatal("foreign actor/spell started current channel")
	}
	malformed := channelFrame(s.selfGUID, 10, 8000, true)
	malformed = malformed[:len(malformed)-1]
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.MSGChannelStart, Payload: malformed})
	if s.supportOp.view.ChannelStarted || s.state.MalformedPackets == 0 {
		t.Fatal("partial channel frame established facts")
	}
	emitChannel(t, s, true, s.selfGUID, 10, 8000)
	until := s.supportOp.until
	advance(time.Second)
	emitChannel(t, s, true, s.selfGUID, 10, 900000)
	if s.supportOp.until != until || s.supportOp.view.ChannelRemainingMs != 8000 {
		t.Fatal("duplicate start extended owned channel")
	}
	advance(11 * time.Second)
	s.expireSelfChannelLocked()
	if s.state.CastPending || s.supportOp.view.Phase != "outcome_unconfirmed_no_retry" || s.supportOp.view.ChannelEnded || s.supportOp.view.EffectObserved {
		t.Fatal("expiry fabricated actual end/effect", s.stateLocked())
	}
	if len(f.packets) != 0 {
		t.Fatal("expiry replayed a spell or moved", f.packets)
	}
}
func cancelOperation(s *Service, channel bool) *actionsv1.CancelSpellRequest {
	op := s.supportOp
	return &actionsv1.CancelSpellRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, SpellId: op.view.SpellId, ExpectedOperationRevision: op.view.OperationRevision, VisitId: op.view.VisitId, Channel: channel}
}
func TestCancelSpellNormalGroundCastAndChannelPacketsRetainOriginalCustody(t *testing.T) {
	for _, channel := range []bool{false, true} {
		t.Run(map[bool]string{false: "flamestrike_cast", true: "blizzard_channel"}[channel], func(t *testing.T) {
			spell := uint32(2120)
			if channel {
				spell = 10
			}
			s, f, _ := pendingChannelFixture(t, spell, 0, channel)
			if channel {
				emitChannel(t, s, true, s.selfGUID, spell, 8000)
			}
			r := cancelOperation(s, channel)
			if _, err := s.CancelSpell(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			want := uint32(opcode.CMSGCancelCast)
			size := 5
			offset := 1
			if channel {
				want = opcode.CMSGCancelChannelling
				size = 4
				offset = 0
			}
			if len(f.packets) != 1 || f.packets[0].Opcode != want || len(f.packets[0].Payload) != size || binary.LittleEndian.Uint32(f.packets[0].Payload[offset:]) != spell {
				t.Fatal("wrong ordinary cancellation wire", f.packets)
			}
			if !s.state.CastPending || s.supportOp.view.ChannelEnded || !s.supportOp.view.CancelAttempted || !s.supportOp.view.CancelTransportWritten || s.supportOp.view.EffectObserved || s.supportOp.policySpellToken != "original-cast-token" {
				t.Fatal("cancel ACK erased original cast/effect ownership", s.stateLocked())
			}
			if _, err := s.CancelSpell(context.Background(), r); err != nil || len(f.packets) != 1 {
				t.Fatal("same operation cancellation replayed", err, f.packets)
			}
			if channel {
				emitChannel(t, s, false, s.selfGUID, 0, 0)
				if s.state.CastPending || !s.supportOp.view.ChannelEnded {
					t.Fatal("actual channel end did not settle original")
				}
			}
		})
	}
}

type refusedCancelSession struct{ *fakeSession }

func (f refusedCancelSession) SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	return nil, modcb.PacketNotDispatchedError("core refused before cancellation transport")
}
func TestCancelSpellTypedNoWriteUnknownAndStaleIdentityNoReplay(t *testing.T) {
	s, f, _ := pendingChannelFixture(t, 10, 0, true)
	emitChannel(t, s, true, s.selfGUID, 10, 8000)
	r := cancelOperation(s, true)
	s.session = refusedCancelSession{f}
	if _, err := s.CancelSpell(context.Background(), r); !modcb.PacketWasNotDispatched(err) || s.supportOp.view.CancelAttempted || len(f.packets) != 0 {
		t.Fatal("known no-write became unknown or consumed cancel", err, s.stateLocked())
	}
	s.session = f
	f.failSend = true
	if _, err := s.CancelSpell(context.Background(), r); err == nil || !s.supportOp.view.CancelOutcomeUnknown || !s.state.CastPending || len(f.packets) != 1 {
		t.Fatal("actual unknown cancellation not retained", err, s.stateLocked())
	}
	if _, err := s.CancelSpell(context.Background(), r); err != nil || len(f.packets) != 1 {
		t.Fatal("unknown cancel replayed", err, f.packets)
	}
	for _, mutate := range []func(*actionsv1.CancelSpellRequest){func(r *actionsv1.CancelSpellRequest) { r.SessionId = "old" }, func(r *actionsv1.CancelSpellRequest) { r.SelfGuid++ }, func(r *actionsv1.CancelSpellRequest) { r.SpellId++ }, func(r *actionsv1.CancelSpellRequest) { r.ExpectedOperationRevision++ }, func(r *actionsv1.CancelSpellRequest) { r.VisitId++ }} {
		bad := proto.Clone(r).(*actionsv1.CancelSpellRequest)
		mutate(bad)
		if _, err := s.CancelSpell(context.Background(), bad); err == nil || len(f.packets) != 1 {
			t.Fatal("foreign/stale cancellation admitted", bad, err)
		}
	}
	emitChannel(t, s, false, s.selfGUID, 0, 0)
	if s.state.CastPending || !s.supportOp.view.ChannelEnded || s.supportOp.view.EffectObserved {
		t.Fatal("real end failed to settle unknown cast honestly", s.stateLocked())
	}
}

func TestChannelLifecycleActualStartPromotesFutureLearnedSpellWithoutCatalogHandler(t *testing.T) {
	const future = uint32(999999)
	s, _, _ := pendingChannelFixture(t, future, 0, false)
	emitChannel(t, s, true, s.selfGUID, future, 8000)
	if !s.supportOp.selfChannel || !s.supportOp.view.ChannelStarted || !s.state.CastPending {
		t.Fatal("actual server channel was rejected without a catalog handler", s.stateLocked())
	}
	emitChannel(t, s, false, s.selfGUID, 0, 0)
	if s.state.CastPending || !s.supportOp.view.ChannelEnded || s.supportOp.view.EffectObserved {
		t.Fatal("actual unknown-spell channel end not settled honestly", s.stateLocked())
	}
}

func TestChannelLifecycleUnknownMetadataGOThenStartRetainsExactLearnedOperation(t *testing.T) {
	const future = uint32(4000000000)
	s, f, advance := genericCastRig(t)
	s.applySpellbookLocked(map[uint32]bool{future: true})
	r := genericRequest(s, future)
	r.Targets = &actionsv1.SpellCastTargets{Mask: 64, Destination: &actionsv1.SpellCastLocation{X: 1, Y: 2, Z: 3}}
	if _, err := s.CastSpell(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	original := s.supportOp
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: genericGo(s, future, r.Targets)})
	if !s.state.CastPending || !original.view.ServerCastGo || original.view.Phase != "waiting_channel_observation" || original.view.EffectObserved {
		t.Fatal("GO ended unknown channel or invented effect", s.stateLocked())
	}
	emitChannel(t, s, true, s.selfGUID, future, 8000)
	if s.supportOp != original || !original.selfChannel || !original.view.ChannelStarted || !s.state.CastPending {
		t.Fatal("real GO-before-START lost operation custody", s.stateLocked())
	}
	// Missing catalog metadata never disables cancellation of a real channel.
	if _, err := s.CancelSpell(context.Background(), cancelOperation(s, true)); err != nil {
		t.Fatal(err)
	}
	if len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGCancelChannelling || !s.state.CastPending {
		t.Fatal("channel cancel missing or fabricated completion", f.packets, s.stateLocked())
	}
	emitChannel(t, s, false, s.selfGUID, 0, 0)
	if s.state.CastPending || !original.view.ChannelEnded || !original.view.ServerCastGo || original.view.EffectObserved {
		t.Fatal("actual end lost execution evidence or invented effect", s.stateLocked())
	}
	advance(time.Hour)
	if len(f.packets) != 2 {
		t.Fatal("settled channel replayed")
	}
}

func TestCancelSpellSameSIDModuleRecreationCannotReuseOperationRevision(t *testing.T) {
	s, _, _ := genericCastRig(t)
	seed := func(n uint64) *bytes.Reader { return bytes.NewReader(binary.LittleEndian.AppendUint64(nil, n)) }
	s.spellOperationEntropy = seed(1000)
	r := genericRequest(s, 133)
	if _, err := s.CastSpell(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	stale := cancelOperation(s, false)
	// A freshly recreated module can use the exact same live body, visit and spell.
	next, f, _ := genericCastRig(t)
	next.spellOperationEntropy = seed(2000)
	if _, err := next.CastSpell(context.Background(), genericRequest(next, 133)); err != nil {
		t.Fatal(err)
	}
	if next.supportOp.view.OperationRevision == stale.ExpectedOperationRevision {
		t.Fatal("module recreation reused cancellation identity")
	}
	if _, err := next.CancelSpell(context.Background(), stale); err == nil || len(f.packets) != 1 {
		t.Fatal("old module cancellation touched new cast", err, f.packets)
	}
	if _, err := next.CancelSpell(context.Background(), cancelOperation(next, false)); err != nil || len(f.packets) != 2 {
		t.Fatal("current exact cancellation unavailable", err, f.packets)
	}
}
func TestChannelLifecycleIdentityFailureAndExhaustionNeverWrite(t *testing.T) {
	for _, mode := range []string{"entropy_unavailable", "zero_entropy", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := genericCastRig(t)
			switch mode {
			case "entropy_unavailable":
				s.spellOperationEntropy = bytes.NewReader(nil)
			case "zero_entropy":
				s.spellOperationEntropy = bytes.NewReader(make([]byte, 8))
			case "exhausted":
				s.spellOperationEpoch = math.MaxUint64 - 1
				s.spellOperationSequence = 1
			}
			if _, err := s.CastSpell(context.Background(), genericRequest(s, 133)); err == nil || len(f.packets) != 0 || s.state.CastPending {
				t.Fatal("identity failure wrote/reserved cast", err, f.packets, s.stateLocked())
			}
		})
	}
}

func TestChannelLifecycleValidVisitBoundaryRetiresOldCastWithoutInventingEnd(t *testing.T) {
	for _, channel := range []bool{false, true} {
		t.Run(map[bool]string{false: "ground_cast", true: "ground_channel"}[channel], func(t *testing.T) {
			s, f, _ := genericCastRig(t)
			id := uint32(2120)
			if channel {
				id = 10
			}
			s.applySpellbookLocked(map[uint32]bool{id: true})
			r := genericRequest(s, id)
			r.Targets = &actionsv1.SpellCastTargets{Mask: 64, Destination: &actionsv1.SpellCastLocation{X: 1, Y: 2, Z: 3}}
			if _, err := s.CastSpell(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if channel {
				emitChannel(t, s, true, s.selfGUID, id, 8000)
			}
			stale := cancelOperation(s, channel)
			for _, bad := range []*modv1.WorldPacket{{Opcode: opcode.SMSGTransferPending, Payload: make([]byte, 3)}, {Opcode: opcode.SMSGNewWorld, Payload: make([]byte, 19)}} {
				s.OnPacket(context.Background(), bad)
				if !s.state.CastPending || s.supportOp.view.Phase == "world_transfer_outcome_unconfirmed" {
					t.Fatal("malformed boundary retired actual cast", s.stateLocked())
				}
			}
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGTransferPending, Payload: make([]byte, 4)})
			if s.state.CastPending || s.supportOp.view.Phase != "world_transfer_outcome_unconfirmed" || s.supportOp.view.ServerCastGo || s.supportOp.view.EffectObserved || s.supportOp.view.ChannelEnded {
				t.Fatal("valid transfer fabricated cast/end/effect", s.stateLocked())
			}
			emitChannel(t, s, true, s.selfGUID, id, 8000)
			if s.state.CastPending {
				t.Fatal("old visit channel packet regained custody")
			}
			entry := binary.LittleEndian.AppendUint32(nil, f.world.Self.MapId)
			for range 4 {
				entry = binary.LittleEndian.AppendUint32(entry, 0)
			}
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGNewWorld, Payload: entry})
			f.world.VisitRevision = s.encounterVisit.revision
			r.VisitId = f.world.VisitRevision
			if _, err := s.CastSpell(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CancelSpell(context.Background(), stale); err == nil || len(f.packets) != 2 || !s.state.CastPending {
				t.Fatal("prior visit cancel touched new cast", err, f.packets, s.stateLocked())
			}
		})
	}
}
