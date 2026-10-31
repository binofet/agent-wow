package buddyactions

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// Synthetic framing of the actual reported failure code47, not a replay of
// an actual failure payload: that six-byte payload was not retained.
func roadTerminalFailurePacket(op uint32, caster uint64, count byte, spell uint32) []byte {
	var p []byte
	if op != opcode.SMSGCastFailed {
		p = packedGUID(caster)
	}
	p = append(p, count)
	p = binary.LittleEndian.AppendUint32(p, spell)
	return append(p, 47)
}

func roadTerminalFailureRig(t *testing.T) (*Service, *roadFake, *worldv1.CreatureState, *worldv1.CreatureState, *actionsv1.RoadClearRequest, func(time.Duration)) {
	t.Helper()
	s, f, h1, q, advance := roadRig(t)
	h2 := proto.Clone(h1).(*worldv1.CreatureState)
	h2.Guid = 0xf130000000000000 | 1718<<24 | 26630
	h2.Level, h2.Health, h2.MaxHealth = 3, 71, 71
	h2.X, h2.Y, h2.Z = -6167.99, 135.883, 423.193
	f.world.NearbyCreatures = append(f.world.NearbyCreatures, h2)
	f.lookupCreatures[h2.Guid] = h2
	roadStart(t, s, q)
	s.Tick(context.Background())
	if !s.road.waitingResult || !s.state.CastPending || !s.road.reserved || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 {
		t.Fatal("fixture has no single pending road cast", s.stateLocked())
	}
	return s, f, h1, h2, q, advance
}

// A fresh, stopped synthetic client estimate for testing a separately requested
// H2 pull. This is not an actor movement receipt. H1 remains alive, far from the
// new short route; no safety gate is bypassed to make the new source eligible.
func roadTerminalH2Request(s *Service, h2 *worldv1.CreatureState) *actionsv1.RoadClearRequest {
	s.pos = position{x: h2.X, y: h2.Y + 20, z: h2.Z}
	s.posAt = s.now()
	s.posRevision++
	return &actionsv1.RoadClearRequest{RequestId: "explicit-h2-after-terminal", TargetGuid: h2.Guid, SessionId: "road-session", ExpectedPosition: s.clientPositionLocked(), PositionRevision: s.positionRevision}
}

func TestRoadTerminalFailureExactResultRetiresWithoutInventingProgress(t *testing.T) {
	for _, op := range []uint32{opcode.SMSGCastFailed, opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther} {
		for _, stopped := range []bool{false, true} {
			t.Run(fmt.Sprintf("opcode_%d_stopped_%t", op, stopped), func(t *testing.T) {
				s, f, h1, _, q, advance := roadTerminalFailureRig(t)
				until := s.road.until
				if stopped {
					_, _ = s.Practice(context.Background(), &actionsv1.ToggleRequest{})
					if !s.state.CastPending || !s.road.waitingResult {
						t.Fatal("STOP claimed to resolve the pending cast")
					}
				}
				writes := len(f.packets)
				roadPacket(s, op, roadTerminalFailurePacket(op, 8, s.castCount, shadowBolt))
				a := s.roadSnapshotLocked()
				if s.road.waitingResult || s.state.CastPending || !s.roadCast.known || s.roadCast.busy || s.roadCast.poisoned || a.Active || !s.road.invalid || a.RemainingMs != 0 || !a.PullReserved || a.SourceGuid != h1.Guid || a.SourceDeathObserved || s.state.LastCastFailure != 47 || !s.road.until.Equal(until) || len(f.packets) != writes {
					t.Fatal("matching terminal result did not retire only its pending proof", a, s.roadCast)
				}
				if !stopped && a.Phase != "cast_failed_no_retry" {
					t.Fatal(a.Phase)
				}
				// The original request remains an inert read; a new ID cannot reuse
				// H1, and a consumed request ID cannot be redirected to another body.
				old, err := s.StartRoadClear(context.Background(), q)
				if err != nil || old.RoadClear.Active || old.RoadClear.RemainingMs != 0 {
					t.Fatal("request replay rearmed", old, err)
				}
				other := proto.Clone(q).(*actionsv1.RoadClearRequest)
				other.RequestId = "new-id-same-h1"
				if _, err := s.StartRoadClear(context.Background(), other); err == nil {
					t.Fatal("consumed H1 body rearmed")
				}
				other.RequestId, other.TargetGuid = q.RequestId, h1.Guid+1
				if _, err := s.StartRoadClear(context.Background(), other); err == nil {
					t.Fatal("consumed request changed target")
				}
				advance(time.Second)
				s.Tick(context.Background())
				if len(f.packets) != writes || len(s.roadRequests) != 1 || !s.roadBodies[h1.Guid] || s.road.goObserved || s.road.damageObserved || s.state.ServerAssistStarts != 0 || s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 || h1.Health != 86 || f.world.SelfQuestLog[3].ObjectiveCounts[0] != 0 {
					t.Fatal("failure fabricated a retry, hit, death or quest credit", s.stateLocked())
				}
			})
		}
	}
}

func TestRoadTerminalFailureDifferentBodyStillNeedsExplicitSafeRequest(t *testing.T) {
	for _, mode := range []string{"valid", "wrong_session", "stale_pose", "raw_revision", "own_combat", "unknown_inventory", "claimed_h2", "nearby_live_h1"} {
		t.Run(mode, func(t *testing.T) {
			s, f, h1, h2, _, advance := roadTerminalFailureRig(t)
			roadPacket(s, opcode.SMSGCastFailed, roadTerminalFailurePacket(opcode.SMSGCastFailed, 8, s.castCount, shadowBolt))
			advance(5 * time.Second) // Preserve the existing failure cooldown.
			q2 := roadTerminalH2Request(s, h2)
			switch mode {
			case "wrong_session":
				q2.SessionId = "different-session"
			case "stale_pose":
				q2.ExpectedPosition.AgeMs = 2001
			case "raw_revision":
				f.world.Self.PositionRevision++
			case "own_combat":
				f.world.Self.UnitFlags |= 0x80000
			case "unknown_inventory":
				f.world.InventoryBags[4].ContentsKnown = false
			case "claimed_h2":
				h2.TargetGuid = 7
			case "nearby_live_h1":
				h1.X, h1.Y, h1.Z = s.pos.x+5, s.pos.y, s.pos.z
				h1.PositionRevision++
			}
			writes := len(f.packets)
			a, err := s.StartRoadClear(context.Background(), q2)
			if mode != "valid" {
				if err == nil || a != nil || len(f.packets) != writes || len(s.roadRequests) != 1 || s.roadBodies[h2.Guid] {
					t.Fatal("new body bypassed ordinary admission", a, err)
				}
				return
			}
			if err != nil || a.RoadClear == nil || !a.RoadClear.Active || a.RoadClear.SourceGuid != 0 || a.RoadClear.PullReserved || a.RoadClear.RemainingMs != 180000 || !s.roadBodies[h1.Guid] || !s.roadBodies[h2.Guid] || len(s.roadRequests) != 2 || len(f.packets) != writes {
				t.Fatal("different explicit body could not arm without writing", a, err)
			}
			s.Tick(context.Background())
			if countPackets(f.fakeSession, opcode.CMSGCastSpell) != 2 || !s.road.reserved || s.road.source != h2.Guid || !s.state.CastPending || s.road.death || s.road.goObserved || s.road.damageObserved || f.world.SelfQuestLog[3].ObjectiveCounts[0] != 0 {
				t.Fatal("new explicit pull reused H1 or fabricated progress", s.stateLocked())
			}
		})
	}
}

func TestRoadTerminalFailureUnknownMalformedAndMismatchedCannotResolve(t *testing.T) {
	for _, mode := range []string{"truncated", "trailing", "wrong_count", "wrong_spell", "foreign_caster", "malformed_caster", "prior_poison"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, h2, _, _ := roadTerminalFailureRig(t)
			op := uint32(opcode.SMSGCastFailed)
			p := roadTerminalFailurePacket(op, 8, s.castCount, shadowBolt)
			switch mode {
			case "truncated":
				p = p[:5]
			case "trailing":
				p = append(p, 0)
			case "wrong_count":
				p[0]++
			case "wrong_spell":
				binary.LittleEndian.PutUint32(p[1:], 836)
			case "foreign_caster":
				op = opcode.SMSGSpellFailure
				p = roadTerminalFailurePacket(op, 7, s.castCount, shadowBolt)
			case "malformed_caster":
				op, p = opcode.SMSGSpellFailure, []byte{0xff}
			case "prior_poison":
				roadPacket(s, opcode.SMSGSpellGo, []byte{0})
			}
			writes := len(f.packets)
			roadPacket(s, op, p)
			if !s.road.waitingResult || s.roadCast.known || s.road.death || s.road.goObserved || s.road.damageObserved || len(f.packets) != writes {
				t.Fatal("unproven result settled pending road proof", s.stateLocked())
			}
			q2 := roadTerminalH2Request(s, h2)
			if a, err := s.StartRoadClear(context.Background(), q2); err == nil || a != nil || len(s.roadRequests) != 1 || s.roadBodies[h2.Guid] {
				t.Fatal("unproven terminal event admitted another source", a, err)
			}
		})
	}
}

func TestRoadTerminalFailureUnrelatedCastCannotSettlePostGoDamage(t *testing.T) {
	s, f, h1, h2, _, _ := roadTerminalFailureRig(t)
	roadPacket(s, opcode.SMSGSpellStart, roadEvent(false, s.castCount, h1.Guid))
	roadPacket(s, opcode.SMSGSpellGo, roadEvent(true, s.castCount, h1.Guid))
	_, _ = s.Practice(context.Background(), &actionsv1.ToggleRequest{})
	if !s.road.waitingResult || !s.road.resultGo || s.state.CastPending || !s.roadCast.known || s.road.active {
		t.Fatal("fixture lacks retained post-GO damage uncertainty")
	}
	// Exercise existing ordinary cast bookkeeping on a different target after
	// STOP; this internal fake is not a newly admitted road operation.
	if !s.castOffenseLocked(context.Background(), shadowBolt, h2.Guid, 3*time.Second) {
		t.Fatal("ordinary cast fixture failed")
	}
	roadPacket(s, opcode.SMSGCastFailed, roadTerminalFailurePacket(opcode.SMSGCastFailed, 8, s.castCount, shadowBolt))
	if !s.road.waitingResult || !s.road.resultGo || s.state.CastPending || !s.roadCast.known || s.road.damageObserved || s.road.death {
		t.Fatal("unrelated failure resolved the old unknown damage result", s.stateLocked())
	}
	q2 := roadTerminalH2Request(s, h2)
	writes := len(f.packets)
	if a, err := s.StartRoadClear(context.Background(), q2); err == nil || a != nil || len(s.roadRequests) != 1 || len(f.packets) != writes {
		t.Fatal("unrelated result allowed new road publication", a, err)
	}
}
