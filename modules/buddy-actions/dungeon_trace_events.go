package buddyactions

import (
	"bytes"
	"encoding/binary"
	"math"
	"strconv"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

type traceTarget struct {
	Kind string `json:"kind"`
	GUID string `json:"guid,omitempty"`
}

type tracePose struct {
	X      float32 `json:"x"`
	Y      float32 `json:"y"`
	Z      float32 `json:"z"`
	Facing float32 `json:"facing"`
	AgeMS  uint64  `json:"ageMs"`
	Rev    uint64  `json:"revision"`
}

type traceMovement struct {
	Flags      uint32 `json:"flags"`
	ExtraFlags uint32 `json:"extraFlags"`
	SourceMS   uint32 `json:"playerMovementClockMs"`
}

type traceTargetState struct {
	Target traceTarget `json:"target"`
	Entry  uint32      `json:"entry"`
	Pose   *tracePose  `json:"pose,omitempty"`
}

type traceHealth struct {
	Current uint32 `json:"current"`
	Max     uint32 `json:"max"`
	AgeMS   uint64 `json:"ageMs"`
}

type tracePower struct {
	Current uint32 `json:"current"`
	Max     uint32 `json:"max"`
	Type    uint32 `json:"type"`
	AgeMS   uint64 `json:"ageMs"`
}

type traceOwnerObservation struct {
	Provenance           string            `json:"provenance"`
	OwnerVisible         bool              `json:"ownerVisible"`
	Gap                  string            `json:"gap,omitempty"`
	Pose                 *tracePose        `json:"pose,omitempty"`
	Health               *traceHealth      `json:"health,omitempty"`
	Power                *tracePower       `json:"power,omitempty"`
	Alive                *bool             `json:"alive,omitempty"`
	InCombat             *bool             `json:"inCombat,omitempty"`
	Movement             *traceMovement    `json:"movement,omitempty"`
	Target               traceTarget       `json:"target"`
	TargetChangeObserved bool              `json:"targetChangeObserved,omitempty"`
	TargetState          *traceTargetState `json:"targetState,omitempty"`
}

func dungeonTraceTarget(guid uint64, observer uint64) traceTarget {
	switch guid {
	case 0:
		return traceTarget{Kind: "none"}
	case ownerGUID:
		return traceTarget{Kind: "joel"}
	case observer:
		return traceTarget{Kind: "observer"}
	}
	switch guid >> 48 {
	case 0xf130, 0xf150:
		return traceTarget{Kind: "npc", GUID: strconv.FormatUint(guid, 10)}
	case 0xf110:
		return traceTarget{Kind: "game_object", GUID: strconv.FormatUint(guid, 10)}
	default:
		return traceTarget{Kind: "redacted"}
	}
}

// ownerForDungeonTrace is intentionally separate from the combat eligibility
// checks. Observations of Joel are useful even when Joel is dead or out of combat.
func ownerForDungeonTrace(w *worldv1.SnapshotResponse, observer uint64) (*worldv1.PlayerState, string) {
	if w == nil || w.Self == nil || w.Self.Guid != observer || !w.Self.Present || !w.Self.MapKnown {
		return nil, "observer_map_unknown"
	}
	if w.Owner == nil || w.Owner.Guid != ownerGUID || !w.Owner.Present {
		return nil, "owner_not_visible"
	}
	if !w.Owner.MapKnown || w.Self.MapId != w.Owner.MapId {
		return nil, "owner_not_confirmed_on_observer_map"
	}
	return w.Owner, ""
}

func projectDungeonOwner(w, previous *worldv1.SnapshotResponse, observer uint64) traceOwnerObservation {
	p, gap := ownerForDungeonTrace(w, observer)
	row := traceOwnerObservation{
		Provenance:   "companion_world_snapshot",
		OwnerVisible: p != nil,
		Gap:          gap,
		Target:       traceTarget{Kind: "unknown"},
	}
	if p == nil {
		return row
	}
	if p.PositionKnown && finite(playerPosition(p)) {
		row.Pose = &tracePose{X: p.X, Y: p.Y, Z: p.Z, Facing: p.Orientation, AgeMS: p.PositionAgeMs, Rev: p.PositionRevision}
	}
	if p.MovementFlagsKnown {
		row.Movement = &traceMovement{Flags: p.MovementFlags, ExtraFlags: p.ExtraMovementFlags, SourceMS: p.MovementTime}
	}
	if p.HealthKnown {
		row.Health = &traceHealth{Current: p.Health, Max: p.MaxHealth, AgeMS: p.VitalsAgeMs}
	}
	if p.PowerKnown {
		row.Power = &tracePower{Current: p.Power, Max: p.MaxPower, Type: p.PowerType, AgeMS: p.VitalsAgeMs}
	}
	if p.AliveKnown {
		alive := p.Alive
		row.Alive = &alive
	}
	if p.UnitFlagsKnown {
		inCombat := p.UnitFlags&0x00080000 != 0
		row.InCombat = &inCombat
	}
	if !p.TargetKnown {
		return row
	}
	row.Target = dungeonTraceTarget(p.TargetGuid, observer)
	if old, _ := ownerForDungeonTrace(previous, observer); old != nil && old.MapId == p.MapId && old.TargetKnown {
		row.TargetChangeObserved = old.TargetGuid != p.TargetGuid
	}
	target := w.OwnerTarget
	if row.Target.Kind != "npc" || target == nil || target.Guid != p.TargetGuid ||
		!target.Present || !target.MapKnown || target.MapId != p.MapId {
		return row
	}
	row.TargetState = &traceTargetState{Target: row.Target, Entry: target.Entry}
	if target.PositionKnown && finite(position{target.X, target.Y, target.Z, target.Orientation}) {
		row.TargetState.Pose = &tracePose{X: target.X, Y: target.Y, Z: target.Z, Facing: target.Orientation, AgeMS: target.PositionAgeMs, Rev: target.PositionRevision}
	}
	return row
}

// The world and actions modules receive server packets on separate queues.
// An independently updated snapshot can show a destination before the actions
// recorder sees NEW_WORLD. Pin changed snapshots to this recorder's boundary.
type dungeonTraceBoundary struct {
	mapID   uint32
	known   bool
	pending bool
}

func (b *dungeonTraceBoundary) accepts(mapID uint32) bool {
	if b.pending {
		return false
	}
	if !b.known {
		b.known, b.mapID = true, mapID
	}
	return b.mapID == mapID
}

func (s *Service) traceWorld(w *worldv1.SnapshotResponse, at time.Time) {
	if s.dungeonTrace == nil {
		return
	}
	if w != nil && w.Self != nil && w.Self.Guid == s.selfGUID && w.Self.Present && w.Self.MapKnown &&
		s.traceBoundary.accepts(w.Self.MapId) {
		s.dungeonTrace.ObserveMap(w.Self.MapId, true, at)
	} else {
		s.dungeonTrace.ObserveMap(0, false, at)
	}
	if s.dungeonTrace.Active() {
		s.dungeonTrace.Record("owner_observation", at, projectDungeonOwner(w, s.tracePreviousWorld, s.selfGUID))
	}
}

type traceCastMiss struct {
	Target  traceTarget `json:"target"`
	Reason  byte        `json:"reason"`
	Reflect *byte       `json:"reflectResult,omitempty"`
}

type traceCast struct {
	Provenance     string          `json:"provenance"`
	Outcome        string          `json:"outcome"`
	SpellID        uint32          `json:"spellId"`
	CastCount      *byte           `json:"castCount,omitempty"`
	CastFlags      *uint32         `json:"castFlags,omitempty"`
	CastDurationMS *uint32         `json:"castDurationMs,omitempty"`
	ServerClockMS  *uint32         `json:"serverGameClockMs,omitempty"`
	TargetCoverage string          `json:"targetCoverage"`
	ExplicitTarget traceTarget     `json:"explicitTarget"`
	HitTargets     []traceTarget   `json:"hitTargets,omitempty"`
	MissedTargets  []traceCastMiss `json:"missedTargets,omitempty"`
	ResultCode     *byte           `json:"resultCode,omitempty"`
	Damage         *uint32         `json:"damage,omitempty"`
}

func traceGUID(data []byte) (uint64, int, bool) {
	guid, n, err := readGUID(data)
	return guid, n, err == nil && bytes.Equal(data[:n], packedGUID(guid))
}

// decodeOwnerCast retains only typed observations that explicitly identify Joel
// as caster. START is a server-observed start. GO is a launch notification with
// server hit/miss lists, not proof of an applied effect. CAST_FAILED has no
// caster in its body and goes to the casting client, so cannot be attributed
// to Joel from a companion session.
func decodeOwnerCast(op uint32, data []byte, observer uint64) (traceCast, bool) {
	row := traceCast{
		Provenance: opcode.WorldName(op), TargetCoverage: "unavailable",
		ExplicitTarget: traceTarget{Kind: "unknown"},
	}
	switch op {
	case opcode.SMSGSpellFailure:
		// AzerothCore broadcasts the same interruption as SPELL_FAILED_OTHER;
		// use the canonical SPELL_FAILURE notification once.
		guid, n, ok := traceGUID(data)
		if !ok || guid != ownerGUID || len(data) != n+6 {
			return row, false
		}
		row.SpellID = binary.LittleEndian.Uint32(data[n+1:])
		count, result := data[n], data[n+5]
		row.CastCount, row.ResultCode = &count, &result
		row.Outcome = "interrupted"
		return row, row.SpellID != 0
	case opcode.SMSGSpellnonmeleedamagelog:
		caster, target, spell, damage, err := decodeSpellDamage(data)
		if err != nil || caster != ownerGUID {
			return row, false
		}
		row.SpellID, row.Damage = spell, &damage
		row.ExplicitTarget = dungeonTraceTarget(target, observer)
		row.TargetCoverage, row.Outcome = "damage_log_target", "damage_log_observed"
		return row, true
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo:
		guid, count, spell, offset, err := castEvent(data)
		if err != nil || guid != ownerGUID || spell == 0 {
			return row, false
		}
		// Validate both packed GUID encodings even when the first is a cast item.
		_, n, ok := traceGUID(data)
		if _, _, casterOK := traceGUID(data[n:]); !ok || !casterOK {
			return row, false
		}
		row.SpellID, row.CastCount = spell, &count
		flags := binary.LittleEndian.Uint32(data[offset:])
		row.CastFlags = &flags
		field := binary.LittleEndian.Uint32(data[offset+4:])
		if op == opcode.SMSGSpellStart {
			row.Outcome, row.CastDurationMS = "started", &field
		} else {
			row.Outcome, row.ServerClockMS = "launched", &field
		}
		if !projectTraceCastTargets(&row, data[offset+8:], op == opcode.SMSGSpellGo, observer) {
			row.TargetCoverage = "incomplete"
			if op == opcode.SMSGSpellStart {
				row.Outcome = "start_header_only"
			} else {
				row.Outcome = "go_header_only"
			}
		}
		return row, true
	default:
		return row, false
	}
}

func projectTraceCastTargets(row *traceCast, data []byte, goEvent bool, observer uint64) bool {
	var hits []traceTarget
	var misses []traceCastMiss
	if goEvent {
		if len(data) == 0 || int(data[0])*8 >= len(data)-1 {
			return false
		}
		hitCount := int(data[0])
		data = data[1:]
		for range hitCount {
			guid := binary.LittleEndian.Uint64(data)
			hits = append(hits, dungeonTraceTarget(guid, observer))
			data = data[8:]
		}
		missCount := int(data[0])
		data = data[1:]
		for range missCount {
			if len(data) < 9 {
				return false
			}
			miss := traceCastMiss{Target: dungeonTraceTarget(binary.LittleEndian.Uint64(data), observer), Reason: data[8]}
			data = data[9:]
			if miss.Reason == 11 { // SPELL_MISS_REFLECT has a reflected result byte.
				if len(data) == 0 {
					return false
				}
				reflect := data[0]
				miss.Reflect = &reflect
				data = data[1:]
			}
			misses = append(misses, miss)
		}
	}
	if len(data) < 4 {
		return false
	}
	// SpellCastTargets::Write places the packed object GUID first when any
	// explicit unit, game object, corpse or minipet target bit is present.
	// We do not retain item, string, ground location or optional flag data.
	const objectTargetMask = 0x2 | 0x200 | 0x800 | 0x8000 | 0x10000
	if binary.LittleEndian.Uint32(data)&objectTargetMask != 0 {
		guid, _, ok := traceGUID(data[4:])
		if !ok {
			return false
		}
		row.ExplicitTarget = dungeonTraceTarget(guid, observer)
		row.TargetCoverage = "explicit_object_prefix"
	} else {
		row.ExplicitTarget = traceTarget{Kind: "no_explicit_object"}
		row.TargetCoverage = "no_explicit_object_in_prefix"
	}
	row.HitTargets, row.MissedTargets = hits, misses
	return true
}

func (s *Service) traceOwnerPacket(op uint32, p []byte, at time.Time) {
	if s.dungeonTrace == nil {
		return
	}
	switch {
	case op == opcode.SMSGTransferPending && len(p) >= 4:
		s.traceBoundary.pending = true
		s.dungeonTrace.ObserveMap(0, false, at)
		return
	case op == opcode.SMSGNewWorld && len(p) == 20:
		pose := position{
			x: math.Float32frombits(binary.LittleEndian.Uint32(p[4:])),
			y: math.Float32frombits(binary.LittleEndian.Uint32(p[8:])),
			z: math.Float32frombits(binary.LittleEndian.Uint32(p[12:])),
			o: math.Float32frombits(binary.LittleEndian.Uint32(p[16:])),
		}
		if !finite(pose) {
			return
		}
		// A second world entry may use the same map ID for another instance.
		// Close on the server transfer even if that ID did not change.
		if s.dungeonTrace.Status().Phase == "recording" {
			s.dungeonTrace.Close(at, "observer_world_transfer")
		}
		s.traceBoundary = dungeonTraceBoundary{known: true, mapID: binary.LittleEndian.Uint32(p)}
		return
	}
	if !s.dungeonTrace.Active() {
		return
	}
	row, ok := decodeOwnerCast(op, p, s.selfGUID)
	if !ok {
		return
	}
	if owner, gap := ownerForDungeonTrace(s.tracePreviousWorld, s.selfGUID); owner == nil {
		s.dungeonTrace.Record("cast_gap", at, map[string]string{"reason": gap})
		return
	}
	s.dungeonTrace.Record("owner_cast", at, row)
}
