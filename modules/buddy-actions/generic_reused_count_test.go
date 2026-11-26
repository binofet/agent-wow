package buddyactions

import (
	"bytes"
	"context"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
	"time"
)

func TestRetainedGenericRejectionCannotAttributeOldFailureAfterCountReuse(t *testing.T) {
	s, f, advance := genericCastRig(t)
	s.applySpellbookLocked(map[uint32]bool{3018: true})
	req := genericRequest(s, 3018)
	if _, err := s.CastSpell(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	oldCount := s.castCount
	oldFailure := healingRejectionPacket(opcode.SMSGSpellFailure, s.selfGUID, oldCount, 3018)
	// A different definitive packet settles each actual admitted cast. Retain
	// the old broadcast bytes to model a delayed companion notification.
	if _, err := s.OnPacket(context.Background(), healingRejectionPacket(opcode.SMSGSpellFailedOther, s.selfGUID, s.castCount, 3018)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 255; i++ {
		if _, err := s.CastSpell(context.Background(), req); err != nil {
			t.Fatal(i, err)
		}
		if _, err := s.OnPacket(context.Background(), healingRejectionPacket(opcode.SMSGCastFailed, s.selfGUID, s.castCount, 3018)); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.CastSpell(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if s.castCount != oldCount {
		t.Fatal("fixture did not actually reuse byte count", s.castCount, oldCount)
	}
	revision := s.supportOp.view.OperationRevision
	advance(s.supportOp.until.Sub(s.now()) + time.Millisecond)
	s.supportSnapshotLocked(nil)
	before := len(f.packets)
	if _, err := s.OnPacket(context.Background(), oldFailure); err != nil {
		t.Fatal(err)
	}
	out := state(t, s)
	if out.LastSpellOperation.Phase != "outcome_unconfirmed_no_retry" || out.LastCastFailure != 0 || out.LastSpellOperation.OperationRevision != revision || out.CastPending || len(f.packets) != before {
		t.Fatal("old failure was attributed to a different latest operation after actual count reuse", out, len(f.packets), before)
	}
}

func TestGenericCastLateFailureCannotCrossSameSessionModuleRecreation(t *testing.T) {
	old, _, _ := genericCastRig(t)
	old.spellOperationEntropy = bytes.NewReader([]byte{1, 0, 0, 0, 0, 0, 0, 0})
	old.applySpellbookLocked(map[uint32]bool{3018: true})
	if _, err := old.CastSpell(context.Background(), genericRequest(old, 3018)); err != nil {
		t.Fatal(err)
	}
	oldPacket := healingRejectionPacket(opcode.SMSGSpellFailure, old.selfGUID, old.castCount, 3018)
	oldRevision := old.supportOp.view.OperationRevision
	s, f, advance := genericCastRig(t)
	s.spellOperationEntropy = bytes.NewReader([]byte{2, 0, 0, 0, 0, 0, 0, 0})
	s.applySpellbookLocked(map[uint32]bool{3018: true})
	if _, err := s.CastSpell(context.Background(), genericRequest(s, 3018)); err != nil {
		t.Fatal(err)
	}
	if s.sessionID != old.sessionID || s.selfGUID != old.selfGUID || s.castCount != old.castCount || s.encounterVisit != old.encounterVisit || s.supportOp.view.OperationRevision == oldRevision {
		t.Fatal("fixture failed to recreate same bound body with distinct operation")
	}
	advance(s.supportOp.until.Sub(s.now()) + time.Millisecond)
	s.supportSnapshotLocked(nil)
	before := len(f.packets)
	if _, err := s.OnPacket(context.Background(), oldPacket); err != nil {
		t.Fatal(err)
	}
	out := state(t, s)
	if out.CastPending || out.LastSpellOperation.Phase != "outcome_unconfirmed_no_retry" || out.LastCastFailure != 0 || len(f.packets) != before {
		t.Fatal("old module packet acquired a new operation identity", out)
	}
}
