package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func selfCastChannelPacket(actor uint64, values ...uint32) []byte {
	payload := packedGUID(actor)
	for _, value := range values {
		payload = binary.LittleEndian.AppendUint32(payload, value)
	}
	return payload
}

func TestCastSelfLearnedEvocationChannel(t *testing.T) {
	const evocation = 12051
	ctx := context.Background()
	s, f, advance := rig(t)
	s.sessionID = "mage-body"
	s.applySpellbookLocked(map[uint32]bool{evocation: true, 116: true})
	f.world.Self.ClassId, f.world.Self.Level, f.world.Self.Power = 8, 20, 150
	book, err := s.SpellToolkit(ctx, &emptypb.Empty{})
	if err != nil || len(book.GetSpells()) != 2 || !book.Spells[1].SelfCastSupported || !book.Spells[1].Channeled || book.Spells[0].SelfCastSupported {
		t.Fatal("generic self capability not reflected", book, err)
	}
	req := &actionsv1.CastSelfRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, SpellId: evocation}
	st, err := s.CastSelf(ctx, req)
	if err != nil || !st.GetCastPending() || !st.GetLastSpellOperation().GetTransportWritten() || len(f.packets) != 1 {
		t.Fatal(st, err, f.packets)
	}
	write := f.packets[0]
	guid, n, guidErr := readGUID(write.Payload[10:])
	if write.Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(write.Payload[1:]) != evocation ||
		binary.LittleEndian.Uint32(write.Payload[6:]) != 2 || guid != s.selfGUID || guidErr != nil || len(write.Payload) != 10+n {
		t.Fatal("expected exact spell and owned unit target", write)
	}
	emit := func(op uint32, p []byte) {
		t.Helper()
		if _, err := s.OnPacket(ctx, &modv1.WorldPacket{Opcode: op, Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	emit(opcode.SMSGSpellGo, creationGo(s, evocation))
	emit(opcode.MSGChannelStart, selfCastChannelPacket(3, evocation, 8000))
	emit(opcode.MSGChannelStart, selfCastChannelPacket(s.selfGUID, 118, 8000))
	emit(opcode.MSGChannelUpdate, selfCastChannelPacket(s.selfGUID, 0))
	st = state(t, s)
	if !st.CastPending || !st.LastSpellOperation.ServerCastGo || st.LastSpellOperation.ChannelStarted || st.LastSpellOperation.ChannelEnded {
		t.Fatal("foreign, wrong rank, or unmatched end affected receipt", st)
	}
	emit(opcode.MSGChannelStart, selfCastChannelPacket(s.selfGUID, evocation, 8000))
	advance(5 * time.Second)
	s.Tick(ctx)
	emit(opcode.MSGChannelUpdate, selfCastChannelPacket(s.selfGUID, 2000))
	emit(opcode.MSGChannelUpdate, selfCastChannelPacket(3, 0))
	st = state(t, s)
	if !st.CastPending || st.LastSpellOperation.Phase != "channeling" || !st.LastSpellOperation.ChannelStarted || st.LastSpellOperation.ChannelRemainingMs != 2000 {
		t.Fatal("channel lost cast custody", st)
	}
	emit(opcode.MSGChannelUpdate, selfCastChannelPacket(s.selfGUID, 0))
	st = state(t, s)
	if st.CastPending || st.LastSpellOperation.Phase != "channel_ended_effect_unverified" || !st.LastSpellOperation.ChannelEnded || st.LastSpellOperation.EffectObserved || len(f.packets) != 1 {
		t.Fatal("channel settlement invented effect or repeated write", st, f.packets)
	}
}

func TestCastSelfRejectsUnlearnedAndForeignTargets(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = "mage-body"
	s.applySpellbookLocked(map[uint32]bool{116: true})
	for _, req := range []*actionsv1.CastSelfRequest{
		{SessionId: s.sessionID, SelfGuid: s.selfGUID, SpellId: 12051},
		{SessionId: s.sessionID, SelfGuid: s.selfGUID, SpellId: 116},
		{SessionId: s.sessionID, SelfGuid: 3, SpellId: 116},
		{SessionId: "previous", SelfGuid: s.selfGUID, SpellId: 116},
	} {
		if _, err := s.CastSelf(context.Background(), req); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
			t.Fatal("inadmissible cast was not fenced", req, err, f.packets)
		}
	}
}

func TestCastSelfObservesAnOrdinaryNonChannelCast(t *testing.T) {
	const frostArmor = 168
	ctx := context.Background()
	s, f, _ := rig(t)
	s.sessionID = "mage-body"
	s.applySpellbookLocked(map[uint32]bool{frostArmor: true})
	f.world.Self.ClassId, f.world.Self.Power = 8, 150
	if _, err := s.CastSelf(ctx, &actionsv1.CastSelfRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, SpellId: frostArmor}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: creationGo(s, frostArmor)}); err != nil {
		t.Fatal(err)
	}
	st := state(t, s)
	if st.CastPending || !st.LastSpellOperation.ServerCastGo || st.LastSpellOperation.ChannelStarted || st.LastSpellOperation.EffectObserved || len(f.packets) != 1 {
		t.Fatal("ordinary self cast did not settle", st, f.packets)
	}
}
