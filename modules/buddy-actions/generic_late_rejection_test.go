package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestGenericCastLateFailureRemainsUnattributedWithoutReplay(t *testing.T) {
	for _, wire := range []uint32{opcode.SMSGCastFailed, opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther} {
		t.Run(opcode.WorldName(wire), func(t *testing.T) {
			s, f, advance := genericCastRig(t)
			s.applySpellbookLocked(map[uint32]bool{3018: true})
			f.world.NearbyPlayers = []*worldv1.PlayerState{{Guid: 4, Present: true, MapKnown: true, MapId: f.world.Self.MapId}}
			r := genericRequest(s, 3018)
			r.Targets = &actionsv1.SpellCastTargets{Mask: 2, ObjectGuid: 4}
			out, err := s.CastSpell(context.Background(), r)
			if err != nil || !out.CastPending || !out.LastSpellOperation.TransportWritten {
				t.Fatal(out, err)
			}
			count := s.castCount
			revision := s.supportOp.view.OperationRevision
			advance(s.supportOp.until.Sub(s.now()) + time.Millisecond)
			s.supportSnapshotLocked(nil) // Same expiration called by normal snapshot/tick.
			if s.state.CastPending || s.supportOp.view.Phase != "outcome_unconfirmed_no_retry" {
				t.Fatal(state(t, s))
			}
			before := len(f.packets)
			_, err = s.OnPacket(context.Background(), healingRejectionPacket(wire, s.selfGUID, count, 3018))
			out = state(t, s)
			if err != nil || out.CastPending || out.LastSpellOperation.Phase != "outcome_unconfirmed_no_retry" || out.LastCastFailure != 0 || out.LastSpellOperation.OperationRevision != revision || out.LastSpellOperation.TargetGuid != 4 || out.LastSpellOperation.ServerCastGo || out.LastSpellOperation.EffectObserved || len(f.packets) != before {
				t.Fatal("late failure fabricated operation correlation or replay", out, err, len(f.packets), before)
			}
		})
	}
}

func TestGenericCastLateRejectionCannotBorrowDifferentCustody(t *testing.T) {
	for _, kind := range []string{"count", "spell", "caster", "truncated", "visit", "map", "transfer", "session", "self", "closed", "go", "newer_cast", "newer_pending", "launched", "effect", "intervening_write", "reused_count"} {
		t.Run(kind, func(t *testing.T) {
			s, f, advance := genericCastRig(t)
			s.applySpellbookLocked(map[uint32]bool{3018: true, 133: true})
			if _, err := s.CastSpell(context.Background(), genericRequest(s, 3018)); err != nil {
				t.Fatal(err)
			}
			count := s.castCount
			advance(s.supportOp.until.Sub(s.now()) + time.Millisecond)
			s.supportSnapshotLocked(nil)
			packet := healingRejectionPacket(opcode.SMSGSpellFailure, s.selfGUID, count, 3018)
			switch kind {
			case "count":
				packet = healingRejectionPacket(opcode.SMSGSpellFailure, s.selfGUID, count+1, 3018)
			case "spell":
				packet = healingRejectionPacket(opcode.SMSGSpellFailure, s.selfGUID, count, 133)
			case "caster":
				packet = healingRejectionPacket(opcode.SMSGSpellFailure, 4, count, 3018)
			case "truncated":
				packet.Payload = packet.Payload[:len(packet.Payload)-1]
			case "visit":
				s.encounterVisit.revision++
			case "map":
				s.encounterVisit.mapID++
			case "transfer":
				s.encounterVisit.bound = false
			case "session":
				s.sessionID = "replacement"
			case "self":
				s.selfGUID = 4
				packet = healingRejectionPacket(opcode.SMSGSpellFailure, 4, count, 3018)
			case "closed":
				s.closed = true
			case "go":
				packet = &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: genericGo(s, 3018, nil)}
			case "newer_cast":
				if _, err := s.CastSpell(context.Background(), genericRequest(s, 133)); err != nil {
					t.Fatal(err)
				}
			case "newer_pending":
				s.state.CastPending = true
				s.pendingSpell = 133
				s.pendingTarget = 4
				s.castCount++
			case "launched":
				s.supportOp.view.ServerCastGo = true
			case "effect":
				s.supportOp.view.EffectObserved = true
			case "intervening_write":
				if err := s.sendPacketLocked(context.Background(), opcode.CMSGCastSpell, spellPacket(count+1, 133, s.selfGUID)); err != nil {
					t.Fatal(err)
				}
			case "reused_count":
				if err := s.sendPacketLocked(context.Background(), opcode.CMSGCastSpell, spellPacket(count, 3018, s.selfGUID)); err != nil {
					t.Fatal(err)
				}
			}
			original := proto.Clone(&s.supportOp.view)
			pending, spell, target := s.state.CastPending, s.pendingSpell, s.pendingTarget
			failure := s.state.LastCastFailure
			before := len(f.packets)
			_, err := s.OnPacket(context.Background(), packet)
			if err != nil || !proto.Equal(original, &s.supportOp.view) || s.state.CastPending != pending || s.pendingSpell != spell || s.pendingTarget != target || s.state.LastCastFailure != failure || len(f.packets) != before {
				t.Fatal("mismatched evidence changed custody", kind, s.stateLocked(), err)
			}
		})
	}
}

func TestGenericCastActiveRejectionKeepsExactRealmResult(t *testing.T) {
	for _, wire := range []uint32{opcode.SMSGCastFailed, opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther} {
		t.Run(opcode.WorldName(wire), func(t *testing.T) {
			s, f, _ := genericCastRig(t)
			s.applySpellbookLocked(map[uint32]bool{3018: true})
			if _, err := s.CastSpell(context.Background(), genericRequest(s, 3018)); err != nil {
				t.Fatal(err)
			}
			before := len(f.packets)
			if _, err := s.OnPacket(context.Background(), healingRejectionPacket(wire, s.selfGUID, s.castCount, 3018)); err != nil {
				t.Fatal(err)
			}
			out := state(t, s)
			if out.CastPending || out.LastCastFailure != 47 || out.LastSpellOperation.Phase != "server_rejected_no_replay" || out.LastSpellOperation.ServerCastGo || out.LastSpellOperation.EffectObserved || len(f.packets) != before {
				t.Fatal(out, len(f.packets), before)
			}
		})
	}
}
