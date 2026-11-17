package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func exactOffenseFixture(t *testing.T) (*Service, *fakeSession, *actionsv1.CastOffenseRequest, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	s.applySpellbookLocked(map[uint32]bool{smite: true})
	f.world.Self.ChannelSpellKnown, f.world.Self.ChannelObjectKnown = true, true
	f.world.RequestedCreature = &worldv1.CreatureState{
		Guid: enemyGUID, Present: true, MapKnown: true, PositionKnown: true, X: 30,
		HealthKnown: true, Health: 100, MaxHealth: 100, AliveKnown: true, Alive: true, UnitFlagsKnown: true,
		AttackVictimKnown: true, AttackVictimGuid: ownerGUID,
	}
	return s, f, &actionsv1.CastOffenseRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: enemyGUID, SpellId: smite}, advance
}

func exactOffenseDamageLog(caster, target uint64, spell, damage uint32) []byte {
	p := append(packedGUID(target), packedGUID(caster)...)
	tail := make([]byte, 32)
	binary.LittleEndian.PutUint32(tail, spell)
	binary.LittleEndian.PutUint32(tail[4:], damage)
	tail[12] = 2
	return append(p, tail...)
}

func TestExactOffenseUsesSelectedAttackerAndVerifiesCountBoundDamage(t *testing.T) {
	s, f, request, _ := exactOffenseFixture(t)
	ctx := context.Background()
	st, err := s.CastOffense(ctx, request)
	if err != nil || !st.GetCastPending() || !st.GetLastSpellOperation().GetTransportWritten() ||
		st.GetLastSpellOperation().GetCapability() != offenseCapability || len(f.packets) != 1 ||
		!bytes.Equal(f.packets[0].Payload, spellPacket(s.castCount, request.SpellId, request.TargetGuid)) || f.packets[0].Opcode != opcode.CMSGCastSpell {
		t.Fatalf("exact dispatch = %v, %v, packets %v", st, err, f.packets)
	}
	log := exactOffenseDamageLog(s.selfGUID, enemyGUID, smite, 9)
	emitPolymorph(s, opcode.SMSGSpellnonmeleedamagelog, log)
	emitPolymorph(s, opcode.SMSGSpellGo, smiteEvent(true, s.castCount+1, enemyGUID))
	badActor := smiteEvent(true, s.castCount, enemyGUID)
	badActor[1] = 9
	emitPolymorph(s, opcode.SMSGSpellGo, badActor)
	emitPolymorph(s, opcode.SMSGSpellGo, smiteEvent(true, s.castCount, enemyGUID+1))
	st = state(t, s)
	if !st.CastPending || st.GetLastSpellOperation().GetServerCastGo() || st.GetLastSpellOperation().GetEffectObserved() {
		t.Fatal("early log or unrelated GO settled exact cast", st)
	}
	emitPolymorph(s, opcode.SMSGSpellStart, smiteEvent(false, s.castCount, enemyGUID))
	emitPolymorph(s, opcode.SMSGSpellGo, smiteEvent(true, s.castCount, enemyGUID))
	emitPolymorph(s, opcode.SMSGSpellGo, smiteEvent(true, s.castCount, enemyGUID))
	emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(enemyGUID, 9, 118, 20000))
	s.Tick(ctx)
	if countPackets(f, opcode.CMSGCancelCast) != 0 {
		t.Fatal("an effect receipt wait let the already-completed cast be canceled", f.packets)
	}
	for _, p := range [][]byte{
		exactOffenseDamageLog(9, enemyGUID, smite, 9), exactOffenseDamageLog(s.selfGUID, enemyGUID+1, smite, 9),
		exactOffenseDamageLog(s.selfGUID, enemyGUID, smite+1, 9), log[:len(log)-1],
	} {
		emitPolymorph(s, opcode.SMSGSpellnonmeleedamagelog, p)
	}
	st = state(t, s)
	if !st.CastPending || st.ServerAssistStarts != 1 || st.ServerAssistGo != 1 || st.GetLastSpellOperation().GetEffectObserved() {
		t.Fatal("cast GO or mismatched damage established an effect", st)
	}
	emitPolymorph(s, opcode.SMSGSpellnonmeleedamagelog, log)
	st = state(t, s)
	if st.CastPending || st.GetLastSpellOperation().GetPhase() != "confirmed_effect" ||
		!st.GetLastSpellOperation().GetEffectObserved() || st.LastCastTargetGuid != enemyGUID || len(f.packets) != 1 {
		t.Fatal("exact damage did not settle the same cast", st, f.packets)
	}
}

func TestExactOffenseRejectsUnqualifiedRequestWithoutWriting(t *testing.T) {
	for _, name := range []string{"session", "body", "unlearned", "different target", "no attack", "foreign party", "out of range", "behind caster", "unknown channel", "insufficient mana", "protected target"} {
		t.Run(name, func(t *testing.T) {
			s, f, r, _ := exactOffenseFixture(t)
			switch name {
			case "session":
				r.SessionId += "-old"
			case "body":
				r.SelfGuid++
			case "unlearned":
				r.SpellId = 591
			case "different target":
				f.world.NearbyCreatures = []*worldv1.CreatureState{f.world.RequestedCreature}
				f.world.RequestedCreature = nil
			case "no attack":
				f.world.RequestedCreature.AttackVictimKnown = false
			case "foreign party":
				f.party.SessionId += "-old"
			case "out of range":
				f.world.RequestedCreature.X = 30.01
			case "behind caster":
				f.world.RequestedCreature.X = -10
			case "unknown channel":
				f.world.Self.ChannelObjectKnown = false
			case "insufficient mana":
				f.world.Self.Power = 0
			case "protected target":
				emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(enemyGUID, 9, 118, 20000))
			}
			if _, err := s.CastOffense(context.Background(), r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
				t.Fatalf("unqualified cast was admitted: %v, %v", err, f.packets)
			}
		})
	}
}

func TestExactOffenseSettlesNoEffectWithoutReplay(t *testing.T) {
	for _, name := range []string{"zero", "expired", "rejected", "ambiguous with effect"} {
		t.Run(name, func(t *testing.T) {
			s, f, r, advance := exactOffenseFixture(t)
			f.world.RequestedCreature.AttackVictimGuid = s.selfGUID
			f.party = nil // A current direct attack on self needs no group.
			f.failSend = name == "ambiguous with effect"
			if _, err := s.CastOffense(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			phase := "server_cast_go_effect_unverified"
			if name == "rejected" {
				failure := binary.LittleEndian.AppendUint32([]byte{s.castCount}, smite)
				emitPolymorph(s, opcode.SMSGCastFailed, append(failure, 47))
				phase = "server_rejected_no_replay"
			} else {
				emitPolymorph(s, opcode.SMSGSpellGo, smiteEvent(true, s.castCount, enemyGUID))
				switch name {
				case "zero":
					emitPolymorph(s, opcode.SMSGSpellnonmeleedamagelog, exactOffenseDamageLog(s.selfGUID, enemyGUID, smite, 0))
				case "expired":
					advance(5 * time.Second)
					phase = "outcome_unconfirmed_no_retry"
				case "ambiguous with effect":
					phase = "confirmed_effect"
				}
			}
			emitPolymorph(s, opcode.SMSGSpellnonmeleedamagelog, exactOffenseDamageLog(s.selfGUID, enemyGUID, smite, 9))
			st := state(t, s)
			op := st.GetLastSpellOperation()
			if st.CastPending || op.GetPhase() != phase || op.GetEffectObserved() != f.failSend ||
				op.GetTransportWritten() == f.failSend || len(f.packets) != 1 {
				t.Fatal("wrong settlement or automatic replay", st, f.packets)
			}
		})
	}
}
