package buddytrade

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func tradePickLockRig() (*Service, *fakeSession) {
	s, f := fixture()
	f.world.Self.ClassKnown, f.world.Self.ClassId = true, 4
	f.world.Self.UnitFlagsKnown, f.world.Owner.UnitFlagsKnown = true, true
	f.world.SelfSkillsKnown = true
	f.world.SelfSkills = []*worldv1.SkillState{{SkillId: 633, Value: 75}}
	f.toolkit = &actionsv1.SpellToolkitState{SessionId: "session-A", SelfGuid: 1, SpellbookKnown: true,
		Spells: []*actionsv1.KnownSpell{{SpellId: 1804, MetadataKnown: true, CastTimeMs: 5000}}}
	openIncoming(s)
	return s, f
}

func offeredLockbox(lockID uint32) []byte {
	data := extended(true, 4632, 1, 6, 0)
	binary.LittleEndian.PutUint32(data[21+6*73+61:], lockID)
	// A gift can coexist with the lockbox in a normal transferable slot.
	binary.LittleEndian.PutUint32(data[22:], 159)
	binary.LittleEndian.PutUint32(data[30:], 5)
	return data
}

func TestTradePickLockPreservesGiftAndRequiresServerCastAndFreshConsent(t *testing.T) {
	for _, goFirst := range []bool{false, true} {
		s, f := tradePickLockRig()
		ctx := context.Background()
		packet(s, opcode.SMSGTradeStatusExtended, offeredLockbox(5))
		statusPacket(s, 4)
		s.Tick(ctx)
		s.Tick(ctx)
		want := []byte{1, 0x0c, 0x07, 0, 0, 0, 0, 0x10, 0, 0, 1, 6}
		var casts [][]byte
		for _, p := range f.packets {
			if p.Opcode == opcode.CMSGCastSpell {
				casts = append(casts, p.Payload)
			}
		}
		if len(casts) != 1 || !bytes.Equal(casts[0], want) || f.count(opcode.CMSGAcceptTrade) != 0 {
			t.Fatalf("expected one slot-six Pick Lock before acceptance, casts %x", casts)
		}
		statusPacket(s, 7)
		packet(s, opcode.SMSGTradeStatusExtended, extended(false, 0, 0, 0, 1804))
		packet(s, opcode.SMSGCastFailed, []byte{1, 0x0c, 0x07, 0, 0, 27})
		s.Tick(ctx)
		if f.count(opcode.CMSGAcceptTrade) != 0 || s.snapshot().Reason != "trade_pick_lock_staged_waiting_for_joel" {
			t.Fatal("stage did not await Joel's new consent", s.snapshot())
		}
		statusPacket(s, 4)
		s.Tick(ctx)
		if f.count(opcode.CMSGAcceptTrade) != 1 || f.count(opcode.CMSGCancelTrade) != 0 || s.snapshot().Phase != "ACCEPT_SENT" {
			t.Fatal("verified service did not accept existing gift terms", s.snapshot())
		}
		goPacket := []byte{1, 1, 1, 1, 0, 0x0c, 0x07, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x10, 0, 0, 0x81, 0x2a, 0x40}
		if !goFirst {
			statusPacket(s, 8)
			if s.snapshot().Reason == "server_confirmed_trade_and_pick_lock_cast" {
				t.Fatal("trade complete alone confirmed the cast")
			}
		}
		packet(s, opcode.SMSGSpellGo, goPacket[:len(goPacket)-1])
		if s.pickLock.castGo {
			t.Fatal("accepted a truncated item GUID")
		}
		packet(s, opcode.SMSGSpellGo, goPacket)
		if goFirst {
			statusPacket(s, 8)
		}
		if s.snapshot().Reason != "server_confirmed_trade_and_pick_lock_cast" || s.snapshot().CompletedTrades != 1 {
			t.Fatal("did not correlate successful cast with completion", s.snapshot())
		}
	}
}

func TestTradePickLockRejectsUnverifiedTargetsAndChangedTrade(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lockID uint32
		change func(*Service, *fakeSession)
	}{
		{"unknown lock", 0, nil},
		{"insufficient skill", 61, nil},
		{"unlearned spell", 5, func(_ *Service, f *fakeSession) { f.toolkit.Spells = nil }},
		{"combat", 5, func(_ *Service, f *fakeSession) { f.world.Owner.UnitFlags = 0x80000 }},
		{"removed while checking", 5, func(s *Service, f *fakeSession) {
			f.toolkitChange = func() { packet(s, opcode.SMSGTradeStatusExtended, extended(true, 159, 5, 0, 0)) }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := tradePickLockRig()
			packet(s, opcode.SMSGTradeStatusExtended, offeredLockbox(tc.lockID))
			if tc.change != nil {
				tc.change(s, f)
			}
			s.Tick(context.Background())
			if f.count(opcode.CMSGCastSpell) != 0 {
				t.Fatal("cast on an unverified offer")
			}
		})
	}
}

func TestTradePickLockRetriesOnlyCorrelatedTryAgain(t *testing.T) {
	for _, tt := range []struct {
		name      string
		failure   []byte
		writeFail bool
		removeBox bool
		want      int
	}{
		{name: "try again", failure: []byte{1, 0x0c, 0x07, 0, 0, 132}, want: 2},
		{name: "other error", failure: []byte{1, 0x0c, 0x07, 0, 0, 8}, want: 1},
		{name: "other cast", failure: []byte{2, 0x0c, 0x07, 0, 0, 132}, want: 1},
		{name: "malformed", failure: []byte{1, 0x0c, 0x07, 0, 0, 132, 0}, want: 1},
		{name: "box removed", failure: []byte{1, 0x0c, 0x07, 0, 0, 132}, removeBox: true, want: 1},
		{name: "ambiguous write", writeFail: true, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, f := tradePickLockRig()
			ctx := context.Background()
			now := s.now()
			s.now = func() time.Time { return now }
			if tt.writeFail {
				f.fail = opcode.CMSGCastSpell
			}
			packet(s, opcode.SMSGTradeStatusExtended, offeredLockbox(5))
			s.Tick(ctx)
			if tt.failure != nil {
				packet(s, opcode.SMSGCastFailed, tt.failure)
			}
			if tt.removeBox {
				packet(s, opcode.SMSGTradeStatusExtended, extended(true, 4632, 1, 0, 0))
			}
			now = now.Add(15 * time.Second)
			s.Tick(ctx)
			// A duplicate result for cast one cannot re-arm cast two.
			if tt.failure != nil {
				packet(s, opcode.SMSGCastFailed, tt.failure)
			}
			s.Tick(ctx)
			if got := f.count(opcode.CMSGCastSpell); got != tt.want || f.count(opcode.CMSGAcceptTrade) != 0 {
				t.Fatalf("got %d casts, want %d, %v", got, tt.want, s.snapshot())
			}
		})
	}
}

func TestTradePickLockFinalRetryWaitsForReopenedTrade(t *testing.T) {
	s, f := tradePickLockRig()
	ctx := context.Background()
	packet(s, opcode.SMSGTradeStatusExtended, offeredLockbox(5))
	s.Tick(ctx)
	packet(s, opcode.SMSGTradeStatusExtended, extended(false, 0, 0, 0, 1804))
	statusPacket(s, 4)
	s.Tick(ctx)
	packet(s, opcode.SMSGCastFailed, []byte{0, 0x0c, 0x07, 0, 0, 132})
	s.Tick(ctx)
	if f.count(opcode.CMSGCastSpell) != 1 || s.snapshot().Phase != "ACCEPT_SENT" {
		t.Fatal("retried before the server reopened trade", s.snapshot())
	}
	statusPacket(s, 7)
	packet(s, opcode.SMSGTradeStatusExtended, extended(false, 0, 0, 0, 0))
	s.Tick(ctx)
	if f.count(opcode.CMSGCastSpell) != 2 || f.count(opcode.CMSGAcceptTrade) != 1 {
		t.Fatal("did not retry on the reopened trade with fresh acceptance still required", s.snapshot())
	}
}
