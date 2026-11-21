package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTargetProtectionObservesOwnAndForeignPolymorphAndReleasesOnWireEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caster uint64
		apply  uint32
		clear  uint32
		frame  []byte
	}{
		{"own delta, explicit removal", 1, opcode.SMSGAuraUpdate, opcode.SMSGAuraUpdate, buffAura(enemyGUID, 1, 2, 0, 0)},
		{"foreign full, same-slot replacement", 9, opcode.SMSGAuraUpdateAll, opcode.SMSGAuraUpdate, buffAura(enemyGUID, 9, 2, 17, 30000)},
		{"foreign delta, authoritative full omission", 9, opcode.SMSGAuraUpdate, opcode.SMSGAuraUpdateAll, packedGUID(enemyGUID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, request, _ := exactOffenseFixture(t)
			s.state.Assisting = true
			s.assistUntil = s.now().Add(time.Hour) // Keep this legacy intent live while testing independent mechanical protection.
			ctx := context.Background()
			before := state(t, s)
			emitPolymorph(s, tc.apply, sheepAura(enemyGUID, tc.caster, 118, 20000))
			got := state(t, s)
			if len(got.ProtectedTargets) != 1 || got.ProtectedTargetRevision <= before.ProtectedTargetRevision || got.TargetProtectionOverflow || got.LastSpellOperation != nil {
				t.Fatal("aura observation did not remain separate from the cast receipt", got)
			}
			aura := got.ProtectedTargets[0]
			if aura.TargetGuid != enemyGUID || aura.CasterGuid != tc.caster || aura.SpellId != 118 || aura.Slot != 2 || !aura.DurationKnown || aura.RemainingMs != 20000 || aura.Revision == 0 {
				t.Fatal("wrong protected target evidence", aura)
			}
			if _, err := s.CastOffense(ctx, request); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("protected exact cast admitted", err)
			}
			s.Tick(ctx)
			if got := state(t, s); !got.Assisting || got.AssistAttempts != 0 || len(f.packets) != 0 {
				t.Fatal("protected exact offense wrote or lost intent", got, f.packets)
			}
			emitPolymorph(s, tc.clear, tc.frame)
			if clear := state(t, s); len(clear.ProtectedTargets) != 0 || clear.ProtectedTargetRevision <= got.ProtectedTargetRevision {
				t.Fatal("valid removal evidence did not release the target", clear)
			}
			s.Tick(ctx)
			if countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("native Tick chose offense after aura release", f.packets)
			}
			if _, err := s.CastOffense(ctx, request); err != nil {
				t.Fatal(err)
			}
			if got := state(t, s); !got.Assisting || got.AssistAttempts != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 || lastCast(f) != smite {
				t.Fatal("fresh caller-selected cast failed after aura release", got, f.packets)
			}
		})
	}
}

func TestTargetProtectionRetainsUnknownDurationAndRejectsIncompleteRelease(t *testing.T) {
	s, f, request, advance := exactOffenseFixture(t)
	ctx := context.Background()
	aura := buffAura(enemyGUID, 9, 2, 118, 0)
	aura[len(packedGUID(enemyGUID))+5] = 0x83 // Negative effects, no duration fields.
	emitPolymorph(s, opcode.SMSGAuraUpdate, aura)
	before := state(t, s)
	if len(before.ProtectedTargets) != 1 || before.ProtectedTargets[0].DurationKnown || before.ProtectedTargets[0].RemainingMs != 0 {
		t.Fatal("unknown aura duration was fabricated", before)
	}
	advance(time.Minute)
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(enemyGUID, 9, 3, 0, 0))
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(enemyGUID+1, 9, 2, 0, 0))
	// A valid non-control replacement followed by a truncated record cannot
	// partially apply an otherwise invalid full snapshot.
	bad := append(buffAura(enemyGUID, 9, 2, 17, 30000), 5, 0)
	emitPolymorph(s, opcode.SMSGAuraUpdateAll, bad)
	if _, err := s.CastOffense(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("protected exact cast admitted", err)
	}
	s.Tick(ctx)
	got := state(t, s)
	if len(got.ProtectedTargets) != 1 || got.ProtectedTargetRevision != before.ProtectedTargetRevision || got.MalformedPackets != before.MalformedPackets+1 || got.AssistAttempts != 0 || len(f.packets) != 0 {
		t.Fatal("unknown duration, wrong slot, wrong target, or malformed full frame released protection", got, f.packets)
	}
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(enemyGUID, 9, 2, 0, 0))
	s.Tick(ctx)
	if countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("aura release automatically chose an attack")
	}
	if _, err := s.CastOffense(ctx, request); err != nil {
		t.Fatal(err)
	}
	if got := state(t, s); len(got.ProtectedTargets) != 0 || got.AssistAttempts != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("explicit matching removal did not release unknown-duration aura", got, f.packets)
	}
}

func TestTargetProtectionExpiresAtObservedRemainingDuration(t *testing.T) {
	s, f, request, advance := exactOffenseFixture(t)
	ctx := context.Background()
	p := sheepAura(enemyGUID, 9, 118, 20000)
	binary.LittleEndian.PutUint32(p[len(p)-4:], 1000) // Current remaining time differs from maximum.
	emitPolymorph(s, opcode.SMSGAuraUpdate, p)
	before := state(t, s).ProtectedTargetRevision
	advance(999 * time.Millisecond)
	if _, err := s.CastOffense(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("aura expired before observed duration", err)
	}
	s.Tick(ctx)
	if got := state(t, s); len(got.ProtectedTargets) != 1 || got.ProtectedTargets[0].RemainingMs != 1 || got.ProtectedTargetRevision != before || len(f.packets) != 0 {
		t.Fatal("protection expired early", got, f.packets)
	}
	advance(time.Millisecond)
	s.Tick(ctx)
	if countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("duration expiry automatically chose an attack")
	}
	if _, err := s.CastOffense(ctx, request); err != nil {
		t.Fatal(err)
	}
	if got := state(t, s); len(got.ProtectedTargets) != 0 || got.ProtectedTargetRevision <= before || got.AssistAttempts != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("observed duration did not permit a fresh caller-selected cast", got, f.packets)
	}
}

func TestTargetProtectionStopsExistingSwingThroughServerStopFence(t *testing.T) {
	s, f, request, _ := exactMeleeFixture(t)
	s.state.Assisting = true
	s.assistUntil = s.now().Add(time.Hour) // Keep this legacy intent live while testing independent mechanical protection.
	ctx := exactMeleeContext()
	if _, err := s.Melee(ctx, request); err != nil {
		t.Fatal(err)
	}
	start := binary.LittleEndian.AppendUint64(nil, s.selfGUID)
	start = binary.LittleEndian.AppendUint64(start, enemyGUID)
	emitPolymorph(s, opcode.SMSGAttackstart, start)
	if got := state(t, s); !got.AutoAttacking || countPackets(f, opcode.CMSGAttackswing) != 1 {
		t.Fatal("fixture did not enter a server-confirmed swing", got, f.packets)
	}
	emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(enemyGUID, 9, 118, 20000))
	s.Tick(ctx)
	s.Tick(ctx)
	if got := state(t, s); !got.AutoAttackUnconfirmed || !got.Assisting || countPackets(f, opcode.CMSGAttackstop) != 1 || countPackets(f, opcode.CMSGAttackswing) != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("protected swing did not issue exactly one stop", got, f.packets)
	}
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(enemyGUID, 9, 2, 0, 0))
	stop := func(caster uint64) []byte {
		return binary.LittleEndian.AppendUint32(append(packedGUID(caster), packedGUID(enemyGUID)...), 0)
	}
	emitPolymorph(s, opcode.SMSGAttackstop, stop(9))
	s.Tick(ctx)
	if got := state(t, s); !got.AutoAttackUnconfirmed || countPackets(f, opcode.CMSGAttackstop) != 1 || countPackets(f, opcode.CMSGAttackswing) != 1 {
		t.Fatal("aura removal or foreign stop bypassed the own stop fence", got, f.packets)
	}
	emitPolymorph(s, opcode.SMSGAttackstop, stop(s.selfGUID))
	s.Tick(ctx)
	if got := state(t, s); got.AutoAttackUnconfirmed || countPackets(f, opcode.CMSGAttackswing) != 1 {
		t.Fatal("own STOP did not settle without native restart", got, f.packets)
	}
	if _, err := s.Melee(ctx, request); err != nil {
		t.Fatal("fresh exact melee selection failed after STOP", err)
	}
	if got := state(t, s); countPackets(f, opcode.CMSGAttackswing) != 2 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("confirmed own stop did not allow fresh exact melee", got, f.packets)
	}
}

func TestTargetProtectionDoesNotBlockDifferentOrdinaryAssistTarget(t *testing.T) {
	s, f, request, _ := exactOffenseFixture(t)
	s.state.Assisting = true
	s.assistUntil = s.now().Add(time.Hour) // Keep this legacy intent live while testing independent mechanical protection.
	const other = enemyGUID + 1
	emitPolymorph(s, opcode.SMSGAuraUpdateAll, sheepAura(other, 9, 118, 20000))
	if _, err := s.CastOffense(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	got := state(t, s)
	if len(got.ProtectedTargets) != 1 || got.ProtectedTargets[0].TargetGuid != other || got.AssistAttempts != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 || lastCast(f) != smite {
		t.Fatal("protection leaked to a different target", got, f.packets)
	}
	packet := f.packets[len(f.packets)-1]
	if target, _, err := readGUID(packet.Payload[10:]); err != nil || target != enemyGUID {
		t.Fatal("ordinary assist selected the wrong enemy", packet, err)
	}
}
