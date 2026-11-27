package buddyactions

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func idleAfterOffenseFixture(t *testing.T, spell uint32, observeGo, ambiguous bool) (*Service, *fakeSession, *actionsv1.IdleInteractionRequest, func(time.Duration)) {
	t.Helper()
	s, f, advance := portalEntryRig(t)
	front, ok := areaTriggerReference(286)
	if !ok {
		t.Fatal("missing public entry fixture")
	}
	self := f.world.Self
	putAtPortal(self, front.SourceMap, position{x: float32(front.X), y: float32(front.Y), z: float32(front.Z)})
	self.Power, self.MaxPower = 3200, 3200
	self.ClassId = 8
	if spell == 11659 {
		self.ClassId = 9
	}
	f.world.RequestedCreature = &worldv1.CreatureState{Guid: enemyGUID, Present: true, MapKnown: true, MapId: self.MapId,
		PositionKnown: true, X: self.X + 12, Y: self.Y, Z: self.Z, AliveKnown: true, Alive: true, HealthKnown: true,
		Health: 100, MaxHealth: 100, UnitFlagsKnown: true}
	s.applySpellbookLocked(map[uint32]bool{spell: true})
	f.failSend, f.failSendOpcode = ambiguous, opcode.CMSGCastSpell
	cast, err := s.CastOffense(context.Background(), &actionsv1.CastOffenseRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, SpellId: spell, TargetGuid: enemyGUID})
	if err != nil || !cast.GetCastPending() || cast.GetLastSpellOperation().GetTransportWritten() == ambiguous {
		t.Fatal("ordinary offense did not reserve the expected cast", cast, err)
	}
	if observeGo {
		goEvent := smiteEvent(true, s.castCount, enemyGUID)
		_, _, _, off, err := castEvent(goEvent)
		if err != nil {
			t.Fatal(err)
		}
		binary.LittleEndian.PutUint32(goEvent[off-4:], spell)
		deliverHealReceiptPacket(t, s, opcode.SMSGSpellGo, goEvent)
	}
	return s, f, &actionsv1.IdleInteractionRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: f.world.VisitRevision, Owner: "exploration", LeaseMs: 30000}, advance
}

func TestIdleExplorationAfterLaunchedOffenseKeepsUnknownDamageThroughEntry(t *testing.T) {
	for _, spell := range []uint32{10148, 11659} {
		t.Run(strconv.FormatUint(uint64(spell), 10), func(t *testing.T) {
			s, f, acquire, advance := idleAfterOffenseFixture(t, spell, true, false)
			advance(time.Minute)
			before := state(t, s)
			op := before.LastSpellOperation
			if before.CastPending || op.GetPhase() != "outcome_unconfirmed_no_retry" || !op.TransportWritten || !op.ServerCastGo || op.EffectObserved {
				t.Fatal("missing original unconfirmed damage receipt", before)
			}
			ctx := context.Background()
			lease, err := s.AcquireIdleInteraction(ctx, acquire)
			if err != nil || lease.GetToken() == "" {
				t.Fatal("current idle body could not take ordinary travel custody", err)
			}
			notify := &actionsv1.NotifyAreaTriggerRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: acquire.VisitId,
				Token: lease.Token, TriggerId: 286, ExpectedPositionRevision: s.posRevision, ClientRequestId: "ordinary-front-entry"}
			entry, err := s.NotifyAreaTrigger(ctx, notify)
			if err != nil || !entry.GetTransportWritten() || entry.DestinationObserved || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGAreatrigger) != 1 {
				t.Fatal("expected the ordinary scoped STOP and one trigger", entry, err)
			}
			if !proto.Equal(op, state(t, s).LastSpellOperation) || countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.CMSGCancelCast) != 0 {
				t.Fatal("entry changed the old spell or wrote another spell action")
			}
		})
	}
}

func TestIdleExplorationAfterOffenseRequiresKnownLaunchAndCurrentIdleBody(t *testing.T) {
	for _, fault := range []string{"no GO", "ambiguous write", "still casting", "current channel", "unknown channel", "channeled operation", "later cast", "unexpired casting clock", "combat", "moving", "pending attack"} {
		t.Run(fault, func(t *testing.T) {
			s, f, acquire, advance := idleAfterOffenseFixture(t, 11659, fault != "no GO", fault == "ambiguous write")
			if fault != "still casting" {
				advance(time.Minute)
			}
			before := state(t, s).LastSpellOperation
			switch fault {
			case "current channel":
				f.world.Self.ChannelSpellId = 10
			case "unknown channel":
				f.world.Self.ChannelObjectKnown = false
			case "channeled operation":
				s.supportOp.selfChannel = true
			case "later cast":
				s.castCount++
			case "unexpired casting clock":
				s.castUntil = s.now().Add(time.Second)
			case "combat":
				f.world.Self.UnitFlags |= 0x80000
			case "moving":
				f.world.Self.MovementFlags = 1
			case "pending attack":
				s.state.AutoAttackRequested = true
			}
			writes := len(f.packets)
			if _, err := s.AcquireIdleInteraction(context.Background(), acquire); err == nil || len(f.packets) != writes ||
				s.interactionToken != "" || !proto.Equal(before, state(t, s).LastSpellOperation) {
				t.Fatal("unproven idle admission changed custody or the receipt", fault, err)
			}
		})
	}
}
