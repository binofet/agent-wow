package buddyactions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Exact actual GO bytes retained by the public-client diagnostics, not a
// reconstructed login packet. The remaining INITIAL/combat/world fixtures in
// these tests are synthetic, as in the existing road controller tests.
func actualLogin836(t *testing.T) []byte {
	t.Helper()
	p, err := hex.DecodeString("010801080044030000010104009710ef05010800000000000000000200000000")
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(p)
	if len(p) != 32 || hex.EncodeToString(h[:]) != "a8163d30687a0405ebf97d502662e992c7ac21588299c2747d11adc87a9ffb76" {
		t.Fatal("actual fixture identity changed")
	}
	return p
}

func TestRoadLogin836ActualPacketPreservesOnlyEligibleIdle(t *testing.T) {
	s, f, _, q, _ := roadRig(t)
	p := actualLogin836(t)
	before := s.roadCast
	roadPacket(s, opcode.SMSGSpellGo, p)
	a := s.roadCastObserverSnapshotLocked()
	if s.roadCast != before || !a.Known || a.Busy || a.Poisoned || !a.Login_836Accepted || !a.OwnGoSeen || a.OwnStartSeen || a.AnyWriteAttempted || a.OutgoingSeen || a.InitialCount != 1 || a.FirstRejected != nil || a.FirstOwnGo == nil || !bytes.Equal(a.FirstOwnGo.Payload, p) || a.FirstOwnGo.TargetGuid != 0 || !a.FirstOwnGo.TargetGuidKnown || a.FirstOwnGo.BodySupported || s.road.selected || len(s.roadRequests) != 0 || len(f.packets) != 0 {
		t.Fatal("login recognition changed state or lost exact proof", a)
	}
	roadStart(t, s, q)
	s.Tick(context.Background())
	if countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 || !s.state.CastPending || !s.road.reserved {
		t.Fatal("eligible normal road pull remained blocked", s.roadSnapshotLocked())
	}
}

func TestRoadLogin836ActualPacketThenSyntheticSevenCastLifecycle(t *testing.T) {
	roadAgingLifecycle(t, "fresh_server_stop", actualLogin836(t))
}

func TestRoadLogin836ExactObservedShapeAndOpaqueTimestamp(t *testing.T) {
	for i := 0; i < 32; i++ {
		if i >= 13 && i <= 16 {
			continue
		}
		t.Run(fmt.Sprintf("changed_byte_%d", i), func(t *testing.T) {
			s, f, _, _, _ := roadRig(t)
			p := actualLogin836(t)
			p[i] ^= 1
			roadPacket(s, opcode.SMSGSpellGo, p)
			if s.roadLogin836Accepted || len(f.packets) != 0 || s.road.selected {
				t.Fatal("a changed structural byte gained login authority", i)
			}
		})
	}
	for n := 0; n < 32; n++ {
		t.Run(fmt.Sprintf("truncated_%d", n), func(t *testing.T) {
			s, _, _, _, _ := roadRig(t)
			roadPacket(s, opcode.SMSGSpellGo, actualLogin836(t)[:n])
			if s.roadLogin836Accepted {
				t.Fatal("partial packet accepted")
			}
		})
	}
	t.Run("noncanonical_zero_target", func(t *testing.T) {
		s, _, _, _, _ := roadRig(t)
		p := actualLogin836(t)
		p[31] = 1
		p = append(p, 0)
		roadPacket(s, opcode.SMSGSpellGo, p)
		if s.roadLogin836Accepted {
			t.Fatal("expanded packed-zero target accepted")
		}
	})
	t.Run("trailing_byte", func(t *testing.T) {
		s, _, _, _, _ := roadRig(t)
		roadPacket(s, opcode.SMSGSpellGo, append(actualLogin836(t), 0))
		if s.roadLogin836Accepted {
			t.Fatal("unconsumed byte accepted")
		}
	})
	for _, stamp := range []uint32{0, ^uint32(0)} {
		t.Run(fmt.Sprintf("opaque_timestamp_%d", stamp), func(t *testing.T) {
			s, _, _, _, _ := roadRig(t)
			p := actualLogin836(t)
			binary.LittleEndian.PutUint32(p[13:], stamp)
			roadPacket(s, opcode.SMSGSpellGo, p)
			if !s.roadLogin836Accepted || !s.roadCast.known || !bytes.Equal(s.roadFirstOwnGo.Payload, p) {
				t.Fatal("opaque timestamp treated as freshness or lost")
			}
		})
	}
}

func TestRoadLogin836NeverHealsHistoryOrActiveBehavior(t *testing.T) {
	cases := map[string]func(*Service){
		"companion":     func(s *Service) { s.independent = false },
		"other_self":    func(s *Service) { s.selfGUID = 7 },
		"closed":        func(s *Service) { s.closed = true },
		"no_initial":    func(s *Service) { s.roadInitialSeen = false },
		"no_epoch":      func(s *Service) { s.roadEpochKnown = false },
		"invalid_book":  func(s *Service) { s.roadSpellbookKnown = false },
		"unknown686":    func(s *Service) { delete(s.knownSpells, shadowBolt) },
		"unknown_cast":  func(s *Service) { s.roadCast.known = false },
		"busy_cast":     func(s *Service) { s.roadCast.busy = true },
		"started_cast":  func(s *Service) { s.roadCast.started = true },
		"poisoned_cast": func(s *Service) { s.roadCast.poisoned = true },
		"prior_rejection_record": func(s *Service) {
			s.roadCaptureRejectedLocked(opcode.SMSGSpellGo, []byte{0}, "cast_header_invalid")
		},
		"residual_spell":     func(s *Service) { s.roadCast.spell = shadowBolt },
		"prior_start":        func(s *Service) { s.roadOwnStartSeen = true },
		"prior_go":           func(s *Service) { s.roadOwnGoSeen = true },
		"prior_outgoing":     func(s *Service) { s.roadOutgoingSeen = true },
		"prior_any_attempt":  func(s *Service) { s.roadAnyWriteAttempted = true },
		"prior_write_count":  func(s *Service) { s.state.TransportWrites = 1 },
		"module_pending":     func(s *Service) { s.state.CastPending = true },
		"pending_spell":      func(s *Service) { s.pendingSpell = shadowBolt },
		"pending_target":     func(s *Service) { s.pendingTarget = 99 },
		"prior_road":         func(s *Service) { s.road.selected = true },
		"prior_road_request": func(s *Service) { s.roadRequests = map[string]uint64{"old": 99} },
		"prior_road_body":    func(s *Service) { s.roadBodies = map[uint64]bool{99: true} },
		"following":          func(s *Service) { s.state.Following = true },
		"healing":            func(s *Service) { s.state.Healing = true },
		"assisting":          func(s *Service) { s.state.Assisting = true },
		"practicing":         func(s *Service) { s.state.Practicing = true },
		"traveling":          func(s *Service) { s.state.Traveling = true },
		"quest_approach":     func(s *Service) { s.questApproaching = true },
		"interaction":        func(s *Service) { s.state.InteractionOwner = "other" },
		"moving":             func(s *Service) { s.state.Moving = true },
		"motion_unknown":     func(s *Service) { s.state.MotionUnconfirmed = true },
		"autoattacking":      func(s *Service) { s.state.AutoAttacking = true },
		"autorequest":        func(s *Service) { s.state.AutoAttackRequested = true },
		"autounknown":        func(s *Service) { s.state.AutoAttackUnconfirmed = true },
		"autotarget":         func(s *Service) { s.autoTarget = 99 },
		"recovery":           func(s *Service) { s.recovery.state.Enabled = true },
		"fargodeep":          func(s *Service) { s.fargodeep.active = true },
		"quest_goal":         func(s *Service) { s.coldridgeGoal.questID = 179 },
		"level_goal":         func(s *Service) { s.coldridgeGoal.leveling = true },
		"level_request":      func(s *Service) { s.coldridgeLevelingRequests = map[string]struct{}{"old": {}} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, _, _, _ := roadRig(t)
			change(s)
			roadPacket(s, opcode.SMSGSpellGo, actualLogin836(t))
			if s.roadLogin836Accepted || len(f.packets) != 0 {
				t.Fatal("history or active state waived", name)
			}
		})
	}
}

func TestRoadLogin836DurablePacketAndTransportHistory(t *testing.T) {
	for _, mode := range []string{"second_initial", "prior_start_go", "prior_start_failure", "prior_poison", "ambiguous_facing", "successful_facing", "ambiguous_cast", "repeat_login"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, _, _ := roadRig(t)
			switch mode {
			case "second_initial":
				learnSpells(s, shadowBolt)
			case "prior_start_go", "prior_start_failure":
				roadPacket(s, opcode.SMSGSpellStart, roadEvent(false, 0, 8))
				if mode == "prior_start_go" {
					roadPacket(s, opcode.SMSGSpellGo, roadEvent(true, 0, 8))
				} else {
					p := binary.LittleEndian.AppendUint32([]byte{0}, shadowBolt)
					roadPacket(s, opcode.SMSGCastFailed, append(p, 1))
				}
				if !s.roadCast.known || s.roadCast.busy {
					t.Fatal("prior ordinary result fixture not idle")
				}
			case "prior_poison":
				roadPacket(s, opcode.SMSGSpellGo, []byte{0})
				learnSpells(s, shadowBolt)
			case "ambiguous_facing", "successful_facing":
				f.failSend = mode == "ambiguous_facing"
				s.sendLocked(context.Background(), opcode.MSGMoveSetFacing, movementPacket(8, 1, s.pos, false))
				if !s.roadAnyWriteAttempted || mode == "ambiguous_facing" && s.state.TransportWrites != 0 {
					t.Fatal("ambiguous write was not reserved before transport")
				}
			case "ambiguous_cast":
				f.failSend = true
				s.pendingSpell, s.pendingTarget = shadowBolt, c.Guid
				s.sendLocked(context.Background(), opcode.CMSGCastSpell, nil)
				if !s.roadAnyWriteAttempted || !s.roadOutgoingSeen || s.state.TransportWrites != 0 {
					t.Fatal("ambiguous cast history lost")
				}
			case "repeat_login":
				roadPacket(s, opcode.SMSGSpellGo, actualLogin836(t))
				if !s.roadLogin836Accepted {
					t.Fatal("first actual event rejected")
				}
			}
			roadPacket(s, opcode.SMSGSpellGo, actualLogin836(t))
			if mode != "repeat_login" && s.roadLogin836Accepted || s.roadCast.known || !s.roadCast.poisoned {
				t.Fatal("event healed unknown/busy/history", mode, s.roadCastObserverSnapshotLocked())
			}
			if mode == "repeat_login" && (s.roadFirstRejected == nil || !bytes.Equal(s.roadFirstOwnGo.Payload, actualLogin836(t))) {
				t.Fatal("repeat did not preserve original evidence")
			}
		})
	}
}
