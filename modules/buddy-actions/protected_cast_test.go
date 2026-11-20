package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestProtectedTargetInterruptsOwnPendingDamageCastOnce(t *testing.T) {
	for _, scenario := range []string{"written", "transport unconfirmed"} {
		t.Run(scenario, func(t *testing.T) {
			s, f, _ := assistRig(t)
			ctx := context.Background()
			s.castCount = 9 // The cancel counter is independent of the normal cast counter.
			s.Tick(ctx)
			if got := state(t, s); !got.CastPending || got.LastCastTargetGuid != enemyGUID || lastCast(f) != smite {
				t.Fatal("expected pending Smite", got)
			}
			castCount := s.castCount
			emitPolymorph(s, opcode.SMSGSpellStart, smiteEvent(false, castCount, enemyGUID))
			emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(enemyGUID+1, 9, 118, 20000))
			s.Tick(ctx)
			if countPackets(f, opcode.CMSGCancelCast) != 0 {
				t.Fatal("a different target's aura canceled this cast")
			}
			emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(enemyGUID, 9, 118, 20000))
			f.failSend = scenario == "transport unconfirmed"
			s.Tick(ctx)
			f.failSend = false
			s.Tick(ctx)
			s.Tick(ctx)
			if countPackets(f, opcode.CMSGCancelCast) != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("cancel must be attempted once without another cast", f.packets)
			}
			last := f.packets[len(f.packets)-1]
			if last.Opcode != opcode.CMSGCancelCast || !bytes.Equal(last.Payload, []byte{1, 0x49, 0x02, 0x00, 0x00}) {
				t.Fatal("wrong cancel opcode, counter, or spell ID", last)
			}
			if got := state(t, s); !got.CastPending || got.ServerAssistGo != 0 || s.castCount != castCount {
				t.Fatal("cancel transport cannot settle the spell or change its cast counter", got)
			}
			failure := func(count byte) {
				body := binary.LittleEndian.AppendUint32([]byte{count}, smite)
				emitPolymorph(s, opcode.SMSGCastFailed, append(body, 40)) // SPELL_FAILED_INTERRUPTED
			}
			failure(castCount + 1)
			if !state(t, s).CastPending {
				t.Fatal("unrelated cast failure settled the pending spell")
			}
			failure(castCount)
			if got := state(t, s); got.CastPending || got.LastCastFailure != 40 || got.ServerAssistGo != 0 {
				t.Fatal("expected the matching server interruption to settle the pending spell", got)
			}
		})
	}
}

func TestProtectedTargetDoesNotCancelDamageAfterServerSpellGo(t *testing.T) {
	s, f, _ := assistRig(t)
	ctx := context.Background()
	s.Tick(ctx)
	emitPolymorph(s, opcode.SMSGSpellGo, smiteEvent(true, s.castCount, enemyGUID))
	emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(enemyGUID, 9, 118, 20000))
	s.Tick(ctx)
	if got := state(t, s); got.CastPending || got.ServerAssistGo != 1 || countPackets(f, opcode.CMSGCancelCast) != 0 {
		t.Fatal("a dispatched spell cannot be recalled after server SPELL_GO", got, f.packets)
	}
}
