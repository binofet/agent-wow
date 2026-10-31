package buddyactions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func syntheticOwnGo(spell uint32, target uint64) []byte {
	p := roadEvent(true, 0, target)
	_, _, _, offset, _ := castEvent(p)
	binary.LittleEndian.PutUint32(p[offset-4:], spell)
	return p
}

func TestRoadCastObserverSyntheticUnpairedGoCapturesWithoutAdmission(t *testing.T) {
	s, f, _, q, advance := roadRig(t)
	// The saved actual prefix89899754... logs INITIAL then one32-byte GO,
	// without payload bytes. This33-byte synthetic unit-target836 frame is a
	// diagnostics regression, NOT a reconstruction or replay of that packet.
	p := syntheticOwnGo(836, 8)
	if len(p) != 33 {
		t.Fatal("synthetic fixture changed")
	}
	roadPacket(s, opcode.SMSGSpellGo, p)
	a, err := s.Status(context.Background(), &emptypb.Empty{})
	if err != nil || a.RoadClear != nil || a.RoadCastObserver == nil || a.RoadCastObserver.Known || !a.RoadCastObserver.Poisoned || a.RoadCastObserver.Busy || !a.RoadCastObserver.InitialSeen || !a.RoadCastObserver.EpochKnown || !a.RoadCastObserver.SpellbookKnown || a.RoadCastObserver.OutgoingSeen {
		t.Fatal("diagnostics changed cast authority", a, err)
	}
	e := a.RoadCastObserver.FirstRejected
	h := sha256.Sum256(p)
	if e == nil || e.Reason != "unpaired_or_conflicting_own_go" || e.PayloadSha256 != hex.EncodeToString(h[:]) || !bytes.Equal(e.Payload, p) || e.PayloadTruncated || e.PayloadSize != 33 || !e.CasterKnown || !e.HeaderKnown || e.CasterGuid != 8 || e.CastObjectGuid != 8 || e.SpellId != 836 || !e.TargetGuidKnown || e.TargetGuid != 8 || !e.BodySupported || !e.GoEvent || !e.HitCountKnown || e.HitCount != 1 || !e.FirstHitKnown || e.FirstHitGuid != 8 || !e.MissCountKnown || e.MissCount != 0 {
		t.Fatal("typed diagnostic lost observed bytes", e)
	}
	if a.RoadCastObserver.FirstOwnGo == nil || !bytes.Equal(a.RoadCastObserver.FirstOwnGo.Payload, p) {
		t.Fatal("first own GO missing")
	}
	if a, err := s.StartRoadClear(context.Background(), q); err == nil || !strings.Contains(err.Error(), "current_cast_unknown_or_busy") || a != nil || len(f.packets) != 0 || len(s.roadRequests) != 0 {
		t.Fatal("diagnostic admitted or reserved a pull", a, err)
	}
	// No Status read refreshes the event clock, and its returned byte slice is
	// detached from the retained event and original packet caller's storage.
	p[0] ^= 0xff
	e.Payload[0] ^= 0xff
	advance(time.Second)
	a, _ = s.Status(context.Background(), &emptypb.Empty{})
	if a.RoadCastObserver.FirstRejected.AgeMs != 1000 || a.RoadCastObserver.FirstRejected.PayloadSha256 != hex.EncodeToString(h[:]) || bytes.Equal(a.RoadCastObserver.FirstRejected.Payload, p) {
		t.Fatal("diagnostic storage aliased or polling renewed its age", a.RoadCastObserver)
	}
	first := proto.Clone(a.RoadCastObserver.FirstRejected)
	learnSpells(s, shadowBolt)
	roadPacket(s, opcode.SMSGSpellGo, syntheticOwnGo(shadowBolt, 8))
	advance(time.Second)
	a, _ = s.Status(context.Background(), &emptypb.Empty{})
	copy := proto.Clone(a.RoadCastObserver.FirstRejected).(*actionsv1.RoadCastObservedEvent)
	copy.AgeMs = 1000
	if !proto.Equal(first, copy) || a.RoadCastObserver.Known || !a.RoadCastObserver.Poisoned || a.RoadCastObserver.FirstRejected.AgeMs != 2000 || a.RoadCastObserver.FirstOwnGo.PayloadSha256 != hex.EncodeToString(h[:]) {
		t.Fatal("later INITIAL/GO healed or replaced first rejection", a.RoadCastObserver)
	}
}

func TestRoadCastObserverBoundedMalformedForeignAndPendingDiagnostics(t *testing.T) {
	t.Run("bounded_first_rejection", func(t *testing.T) {
		s, _, _, _, _ := roadRig(t)
		p := syntheticOwnGo(836, 8)
		p = append(p, bytes.Repeat([]byte{0xff}, 4096-len(p))...)
		roadPacket(s, opcode.SMSGSpellGo, p)
		e := s.roadCastObserverSnapshotLocked().FirstRejected
		h := sha256.Sum256(p)
		if e == nil || len(e.Payload) != 256 || !e.PayloadTruncated || e.PayloadSize != 4096 || e.PayloadSha256 != hex.EncodeToString(h[:]) || !s.roadCast.poisoned {
			t.Fatal(e)
		}
		roadPacket(s, opcode.SMSGSpellGo, []byte{0})
		if got := s.roadCastObserverSnapshotLocked().FirstRejected; got.PayloadSize != 4096 || !bytes.Equal(got.Payload, e.Payload) {
			t.Fatal("first rejection overwritten", got)
		}
	})
	t.Run("malformed_header_has_no_invented_fields", func(t *testing.T) {
		s, _, _, _, _ := roadRig(t)
		roadPacket(s, opcode.SMSGSpellGo, []byte{0xff})
		e := s.roadCastObserverSnapshotLocked().FirstRejected
		if e == nil || e.CasterKnown || e.HeaderKnown || e.CastFlagsKnown || e.TargetFlagsKnown || e.TargetGuidKnown || e.BodySupported || s.roadCast.known {
			t.Fatal(e)
		}
	})
	t.Run("valid_foreign_go_never_becomes_own_event", func(t *testing.T) {
		s, _, _, _, _ := roadRig(t)
		p := append(packedGUID(99), packedGUID(99)...)
		own := syntheticOwnGo(836, 8)
		_, n, _ := readGUID(own)
		_, m, _ := readGUID(own[n:])
		p = append(p, own[n+m:]...)
		roadPacket(s, opcode.SMSGSpellGo, p)
		a := s.roadCastObserverSnapshotLocked()
		if !a.Known || a.Poisoned || a.FirstRejected != nil || a.FirstOwnGo != nil {
			t.Fatal("foreign GO changed own proof", a)
		}
	})
	t.Run("truncated_foreign_header_retains_old_unknown_guard", func(t *testing.T) {
		s, _, _, _, _ := roadRig(t)
		p := append(packedGUID(99), packedGUID(99)...)
		roadPacket(s, opcode.SMSGSpellGo, p)
		a := s.roadCastObserverSnapshotLocked()
		if a.Known || !a.Poisoned || a.FirstRejected == nil || !a.FirstRejected.CasterKnown || a.FirstRejected.CasterGuid != 99 || a.FirstRejected.HeaderKnown || a.FirstOwnGo != nil {
			t.Fatal("diagnostics changed malformed foreign classification", a)
		}
	})
	t.Run("pending_mismatch_is_not_healed", func(t *testing.T) {
		s, _, c, q, _ := roadRig(t)
		roadStart(t, s, q)
		s.Tick(context.Background())
		a := s.roadCastObserverSnapshotLocked()
		if a.Known || !a.Busy || !a.OutgoingSeen || a.PendingSpellId != shadowBolt || a.PendingTargetGuid != c.Guid {
			t.Fatal(a)
		}
		roadPacket(s, opcode.SMSGSpellGo, syntheticOwnGo(836, 8))
		a = s.roadCastObserverSnapshotLocked()
		if a.Known || !a.Poisoned || !a.Busy || !s.state.CastPending || s.road.active || a.FirstRejected == nil {
			t.Fatal("diagnostic healed pending unknown", a)
		}
	})
}

func TestRoadCastObserverMatchedGoIsCapturedWithoutGrantingDamage(t *testing.T) {
	s, _, c, q, _ := roadRig(t)
	roadStart(t, s, q)
	s.Tick(context.Background())
	roadPacket(s, opcode.SMSGSpellStart, roadEvent(false, s.castCount, c.Guid))
	roadPacket(s, opcode.SMSGSpellGo, roadEvent(true, s.castCount, c.Guid))
	a := s.roadCastObserverSnapshotLocked()
	if !a.Known || a.Busy || a.Poisoned || !a.OutgoingSeen || a.FirstRejected != nil || a.FirstOwnGo == nil || a.FirstOwnGo.SpellId != shadowBolt || a.FirstOwnGo.TargetGuid != c.Guid || !s.road.waitingResult || s.road.resultDamage || s.road.damageObserved {
		t.Fatal("GO diagnostics changed existing result authority", a)
	}
}

func TestRoadCastObserverWireNumberPreservesExistingScopes(t *testing.T) {
	state := actionsv1.File_modules_buddy_actions_actions_proto.Messages().ByName("State")
	for name, number := range map[protoreflect.Name]protoreflect.FieldNumber{"coldridge_leveling": 70, "road_clear": 73, "road_cast_observer": 74} {
		field := state.Fields().ByName(name)
		if field == nil || field.Number() != number {
			t.Fatal("observer changed a scope wire field", name, field)
		}
	}
	if state.Fields().ByName("road_cast_observer").Message().FullName() != "warcraftbuddy.actions.v1.RoadCastObserverState" {
		t.Fatal("observer diagnostic type mismatch")
	}
	// The existing TestColdridgeLevelingPublishedDescriptorMatchesAPI compares
	// the entire generated file to module.pb, including this additive field.
}

func TestRoadCastObserverSyntheticMissMetadataIsNotActualPacketReplay(t *testing.T) {
	p := append(packedGUID(8), packedGUID(8)...)
	p = append(p, 0)
	p = binary.LittleEndian.AppendUint32(p, 836)
	p = binary.LittleEndian.AppendUint32(p, 0x40101)
	p = binary.LittleEndian.AppendUint32(p, 123)
	p = append(p, 0, 1)
	p = binary.LittleEndian.AppendUint64(p, 8)
	p = append(p, 11, 7)
	p = binary.LittleEndian.AppendUint32(p, 0)
	e := roadObservedCastEvent(opcode.SMSGSpellGo, p, "synthetic_miss_metadata", 1)
	if !e.HeaderKnown || !e.CastFlagsKnown || e.CastFlags != 0x40101 || !e.HitCountKnown || e.HitCount != 0 || !e.MissCountKnown || e.MissCount != 1 || !e.FirstMissKnown || e.FirstMissGuid != 8 || e.FirstMissReason != 11 || !e.FirstMissReflectKnown || e.FirstMissReflectResult != 7 || !e.TargetFlagsKnown || e.TargetFlags != 0 || e.TargetGuidKnown || e.BodySupported {
		t.Fatal("miss diagnostics invented supported unit target authority", e)
	}
}
