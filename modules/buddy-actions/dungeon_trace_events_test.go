package buddyactions

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/dungeontrace"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestDungeonOwnerProjectionNeverTurnsGapsIntoActions(t *testing.T) {
	const observer = 1
	const npc = 0xf130000001234567
	outside := &worldv1.SnapshotResponse{
		Self:  &worldv1.PlayerState{Guid: observer, Present: true, MapKnown: true},
		Owner: &worldv1.PlayerState{Guid: ownerGUID, Present: true, MapKnown: true, TargetKnown: true, TargetGuid: npc},
	}
	inside := &worldv1.SnapshotResponse{
		Self: &worldv1.PlayerState{Guid: observer, Present: true, MapKnown: true, MapId: 36},
		Owner: &worldv1.PlayerState{Guid: ownerGUID, Present: true, MapKnown: true, MapId: 36,
			PositionKnown: true, Orientation: 1.5, PositionRevision: 12, PositionAgeMs: 200,
			MovementFlagsKnown: true, MovementFlags: 1, MovementTime: 550,
			HealthKnown: true, Health: 300, MaxHealth: 500, AliveKnown: true, Alive: true,
			UnitFlagsKnown: true, UnitFlags: 0x00080000,
			TargetKnown: true, TargetGuid: npc},
		OwnerTarget: &worldv1.CreatureState{Guid: npc, Entry: 1, Present: true, MapKnown: true, MapId: 36,
			PositionKnown: true, X: 5},
	}
	observed := projectDungeonOwner(inside, outside, observer)
	if !observed.OwnerVisible || observed.Pose == nil || observed.Pose.Facing != 1.5 ||
		observed.Pose.AgeMS != 200 || observed.Movement == nil || observed.Movement.SourceMS != 550 ||
		observed.Target.Kind != "npc" || observed.TargetState == nil || observed.TargetState.Pose.X != 5 ||
		observed.TargetChangeObserved || observed.Health == nil || observed.Health.Current != 300 ||
		observed.Alive == nil || !*observed.Alive || observed.InCombat == nil || !*observed.InCombat {
		t.Fatalf("unexpected owner observation: %+v", observed)
	}
	lost := &worldv1.SnapshotResponse{
		Self:  inside.Self,
		Owner: &worldv1.PlayerState{Guid: ownerGUID, MapKnown: true, MapId: 36, TargetKnown: true, TargetGuid: npc},
	}
	gap := projectDungeonOwner(lost, inside, observer)
	if gap.OwnerVisible || gap.Gap == "" || gap.Pose != nil || gap.Movement != nil ||
		gap.Target.Kind != "unknown" || gap.TargetState != nil || gap.TargetChangeObserved ||
		gap.Health != nil || gap.Alive != nil || gap.InCombat != nil {
		t.Fatalf("stale owner data escaped gap: %+v", gap)
	}
	changed := &worldv1.SnapshotResponse{
		Self: inside.Self,
		Owner: &worldv1.PlayerState{Guid: ownerGUID, Present: true, MapKnown: true, MapId: 36,
			TargetKnown: true, TargetGuid: 912345},
	}
	redacted := projectDungeonOwner(changed, inside, observer)
	if !redacted.OwnerVisible || !redacted.TargetChangeObserved || redacted.Target.Kind != "redacted" ||
		redacted.Target.GUID != "" || redacted.TargetState != nil {
		t.Fatalf("target transition was lost or revealed unrelated player data: %+v", redacted)
	}
	if resumed := projectDungeonOwner(changed, lost, observer); resumed.TargetChangeObserved {
		t.Fatalf("invented change across visibility gap: %+v", resumed)
	}
}

func TestDungeonTraceSeparatesCastLifecycleFromEffects(t *testing.T) {
	const npc = 0xf130000001234567
	const unrelated = 912345
	const count = 7
	header := func(guid uint64, packetTime uint32) []byte {
		p := append(packedGUID(guid), packedGUID(guid)...)
		p = append(p, count)
		p = binary.LittleEndian.AppendUint32(p, 585)
		p = binary.LittleEndian.AppendUint32(p, 0)
		return binary.LittleEndian.AppendUint32(p, packetTime)
	}
	start := binary.LittleEndian.AppendUint32(header(ownerGUID, 1500), 2)
	start = append(start, packedGUID(npc)...)
	started, ok := decodeOwnerCast(opcode.SMSGSpellStart, start, 1)
	if !ok || started.Outcome != "started" || started.CastDurationMS == nil || *started.CastDurationMS != 1500 ||
		started.ServerClockMS != nil || started.ExplicitTarget.Kind != "npc" {
		t.Fatalf("unexpected start: %+v", started)
	}
	goPacket := append(header(ownerGUID, 999), 2)
	goPacket = binary.LittleEndian.AppendUint64(goPacket, npc)
	goPacket = binary.LittleEndian.AppendUint64(goPacket, unrelated)
	goPacket = append(goPacket, 1)
	goPacket = binary.LittleEndian.AppendUint64(goPacket, npc)
	goPacket = append(goPacket, 11, 7)
	goPacket = binary.LittleEndian.AppendUint32(goPacket, 2)
	goPacket = append(goPacket, packedGUID(npc)...)
	goRow, ok := decodeOwnerCast(opcode.SMSGSpellGo, goPacket, 1)
	if !ok || goRow.Outcome != "launched" || goRow.Damage != nil ||
		goRow.ServerClockMS == nil || *goRow.ServerClockMS != 999 || goRow.CastDurationMS != nil ||
		len(goRow.HitTargets) != 2 || goRow.HitTargets[1].Kind != "redacted" || goRow.HitTargets[1].GUID != "" ||
		len(goRow.MissedTargets) != 1 || goRow.MissedTargets[0].Reflect == nil || *goRow.MissedTargets[0].Reflect != 7 {
		t.Fatalf("unexpected launch: %+v", goRow)
	}
	truncated, ok := decodeOwnerCast(opcode.SMSGSpellGo, goPacket[:len(goPacket)-2], 1)
	if !ok || truncated.Outcome != "go_header_only" || truncated.TargetCoverage != "incomplete" ||
		len(truncated.HitTargets) != 0 || len(truncated.MissedTargets) != 0 {
		t.Fatalf("truncated cast reported target outcomes: %+v", truncated)
	}
	failure := healingRejectionPacket(opcode.SMSGSpellFailure, ownerGUID, count, 585).Payload
	interrupted, ok := decodeOwnerCast(opcode.SMSGSpellFailure, failure, 1)
	if !ok || interrupted.Outcome != "interrupted" || interrupted.ResultCode == nil || *interrupted.ResultCode != 47 ||
		interrupted.ExplicitTarget.Kind != "unknown" {
		t.Fatalf("unexpected interruption: %+v", interrupted)
	}
	damage := damageGolden(t)
	// This fixture's packed target is five bytes and its packed caster is two.
	damage[6] = byte(ownerGUID)
	effect, ok := decodeOwnerCast(opcode.SMSGSpellnonmeleedamagelog, damage, 1)
	if !ok || effect.Outcome != "damage_log_observed" || effect.Damage == nil || *effect.Damage != 9 ||
		effect.ExplicitTarget.Kind != "npc" || effect.CastCount != nil {
		t.Fatalf("unexpected spell effect: %+v", effect)
	}
	for _, op := range []uint32{opcode.SMSGSpellFailedOther, opcode.SMSGCastFailed} {
		wire := healingRejectionPacket(op, ownerGUID, count, 585).Payload
		if _, ok := decodeOwnerCast(op, wire, 1); ok {
			t.Fatalf("duplicate or unattributable failure accepted for opcode %#x", op)
		}
	}
	foreign := binary.LittleEndian.AppendUint32(header(unrelated, 1500), 2)
	foreign = append(foreign, packedGUID(npc)...)
	if _, ok := decodeOwnerCast(opcode.SMSGSpellStart, foreign, 1); ok {
		t.Fatal("foreign actor cast leaked into Joel trace")
	}
}

func TestDungeonTraceWaitsForItsOwnWorldBoundaryAndEndsOnSameMapTransfer(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := dungeontrace.New(dir, "fixture-companion", 36)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{selfGUID: 1, dungeonTrace: r}
	t0 := time.Now()
	t.Cleanup(func() {
		r.Close(time.Now(), "fixture_end")
		<-r.Done()
	})
	snapshot := func(mapID uint32) *worldv1.SnapshotResponse {
		return &worldv1.SnapshotResponse{
			Self:  &worldv1.PlayerState{Guid: 1, Present: true, MapKnown: true, MapId: mapID},
			Owner: &worldv1.PlayerState{Guid: ownerGUID, Present: true, MapKnown: true, MapId: mapID},
		}
	}
	transfer := func(op uint32, at time.Time) {
		payload := make([]byte, 20)
		binary.LittleEndian.PutUint32(payload, 36)
		s.traceOwnerPacket(op, payload, at)
	}
	s.traceWorld(snapshot(0), t0)
	s.traceWorld(snapshot(36), t0.Add(time.Second))
	if r.Active() {
		t.Fatal("other module's entry snapshot opened visit before local server boundary")
	}
	transfer(opcode.SMSGTransferPending, t0.Add(2*time.Second))
	s.traceWorld(snapshot(36), t0.Add(3*time.Second))
	if r.Active() {
		t.Fatal("opened while transfer was pending")
	}
	transfer(opcode.SMSGNewWorld, t0.Add(4*time.Second))
	s.traceWorld(snapshot(0), t0.Add(5*time.Second))
	if r.Active() {
		t.Fatal("stale outside snapshot opened visit")
	}
	s.traceWorld(snapshot(36), t0.Add(6*time.Second))
	if !r.Active() {
		t.Fatal("verified entry did not open visit")
	}
	cast := healingRejectionPacket(opcode.SMSGSpellFailure, ownerGUID, 7, 585).Payload
	s.tracePreviousWorld = snapshot(36)
	s.tracePreviousWorld.Owner.Present = false
	s.traceOwnerPacket(opcode.SMSGSpellFailure, cast, t0.Add(6200*time.Millisecond))
	s.tracePreviousWorld.Owner.Present = true
	s.traceOwnerPacket(opcode.SMSGSpellFailure, cast, t0.Add(6400*time.Millisecond))
	transfer(opcode.SMSGTransferPending, t0.Add(7*time.Second))
	s.traceWorld(snapshot(36), t0.Add(8*time.Second))
	if r.Active() {
		t.Fatal("old map resumed after transfer began")
	}
	transfer(opcode.SMSGNewWorld, t0.Add(9*time.Second))
	s.traceWorld(snapshot(36), t0.Add(10*time.Second))
	if r.Active() {
		t.Fatal("new same-map instance resumed previous recording")
	}
	select {
	case <-r.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("trace did not close on second NEW_WORLD")
	}
	raw, err := os.ReadFile(filepath.Join(dir, dungeontrace.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte(`"kind":"cast_gap"`)) != 1 || bytes.Count(raw, []byte(`"kind":"owner_cast"`)) != 1 {
		t.Fatalf("expected exactly one visibility gap and one attributable cast: %s", raw)
	}
}
