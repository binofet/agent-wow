package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func roadAttackStop(s *Service, target uint64, dead uint32) {
	p := append(packedGUID(target), packedGUID(8)...)
	p = binary.LittleEndian.AppendUint32(p, dead)
	roadPacket(s, opcode.SMSGAttackstop, p)
}

func TestRoadClearRealAgesOneSplineSevenCastsAndDeath(t *testing.T) {
	for _, terminal := range []string{"retained_elapsed_pose", "fresh_server_stop", "teleported_stop", "foreign_map_stop", "unknown_stop"} {
		t.Run(terminal, func(t *testing.T) { roadAgingLifecycle(t, terminal) })
	}
}

func roadAgingLifecycle(t *testing.T, terminal string) {
	s, f, c, q, advance := roadRig(t)
	h2 := proto.Clone(c).(*worldv1.CreatureState)
	h2.Guid = 17379390990846026630
	h2.Level = 3
	h2.Health = 71
	h2.MaxHealth = 71
	h2.X = -6167.99
	h2.Y = 135.883
	h2.Z = 423.193
	h2.PositionAgeMs = 9000
	h2.ObservationAgeMs = 9000
	f.world.NearbyCreatures = append(f.world.NearbyCreatures, h2)
	c.PositionAgeMs = 8000
	c.ObservationAgeMs = 8000
	elapse := func(d time.Duration) {
		advance(d)
		ms := uint64(d.Milliseconds())
		f.world.Self.PositionAgeMs += ms
		f.world.Self.VitalsAgeMs += ms
		f.world.Self.ObservationAgeMs += ms
		for _, x := range f.world.NearbyCreatures {
			x.PositionAgeMs += ms
			x.ObservationAgeMs += ms
			if x.AttackVictimKnown {
				x.AttackAgeMs += ms
			}
			if x.Trajectory != nil {
				x.Trajectory.AgeMs += ms
			}
		}
	}
	// Login facts age through an ordinary4.5s walk; only the achieved client
	// pose is new. Polling does not renew any world observation timestamp.
	elapse(4500 * time.Millisecond)
	s.posAt = s.now()
	q.ExpectedPosition = s.clientPositionLocked()
	roadStart(t, s, q)
	s.Tick(context.Background())
	if !s.state.CastPending {
		t.Fatal(s.roadSnapshotLocked())
	}
	raw := playerCreaturePosition(c)
	for cast := 1; cast <= 7; cast++ {
		count := s.castCount
		roadPacket(s, opcode.SMSGSpellStart, roadEvent(false, count, c.Guid))
		elapse(1700 * time.Millisecond)
		s.Tick(context.Background())
		if !s.road.active || countPackets(f.fakeSession, opcode.CMSGCastSpell) != cast {
			t.Fatal("pending overlap/abort", cast, s.roadSnapshotLocked())
		}
		roadPacket(s, opcode.SMSGSpellGo, roadEvent(true, count, c.Guid))
		damage := uint32(13)
		if cast == 7 {
			damage = 8
		}
		roadPacket(s, opcode.SMSGSpellnonmeleedamagelog, roadDamage(c.Guid, damage))
		if cast < 7 {
			c.Health -= damage
		}
		c.ObservationAgeMs = 0
		f.world.ObservedPackets++
		f.world.Self.Power -= 12
		f.world.Self.VitalsAgeMs = 0
		f.world.Self.ObservationAgeMs = 0
		f.world.ObservedPackets++
		if cast == 1 {
			c.UnitFlags |= 0x80000
			c.TargetGuid = 8
			c.AttackVictimKnown = true
			c.AttackVictimGuid = 8
			c.AttackAgeMs = 0
			f.world.Self.UnitFlags |= 0x80000
			p := binary.LittleEndian.AppendUint64(nil, c.Guid)
			p = binary.LittleEndian.AppendUint64(p, 8)
			roadPacket(s, opcode.SMSGAttackstart, p)
			// ONE advertised3s move. Raw START remains unchanged forever;
			// reaching its endpoint is never fabricated as a raw position event.
			c.PositionRevision++
			c.PositionAgeMs = 0
			c.MovementFlags = 0x08000001
			c.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 3000, DestinationKnown: true, DestinationX: s.pos.x + 2.5, DestinationY: s.pos.y, DestinationZ: s.pos.z}
			for i := 0; i < 6; i++ {
				elapse(500 * time.Millisecond)
				s.Tick(context.Background())
				if !s.road.active || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 {
					t.Fatal("valid spline approach aborted/recast", s.roadSnapshotLocked())
				}
			}
		} else if cast < 7 {
			elapse(1300 * time.Millisecond)
		}
		if cast == 7 {
			if terminal != "retained_elapsed_pose" {
				c.X, c.Y, c.Z = s.road.contactAim.x, s.road.contactAim.y, s.road.contactAim.z
				c.PositionRevision++
				c.PositionAgeMs = 0
				c.MovementFlags = 0
				c.Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true, DestinationKnown: true, DestinationX: c.X, DestinationY: c.Y, DestinationZ: c.Z}
				switch terminal {
				case "teleported_stop":
					c.X += 2
					c.Trajectory.DestinationX = c.X
				case "foreign_map_stop":
					c.MapId = 1
				case "unknown_stop":
					c.Trajectory.Known = false
				}
				// STOP can be observed before both ATTACKSTOP and the health-zero
				// update. A same-contact stop must suspend, never resume offense.
				s.Tick(context.Background())
				if countPackets(f.fakeSession, opcode.CMSGCastSpell) != 7 || s.roadSnapshotLocked().SourceDeathObserved {
					t.Fatal("terminal pose granted offense/death")
				}
			}
			roadAttackStop(s, c.Guid, 1)
			s.Tick(context.Background())
			if s.roadSnapshotLocked().SourceDeathObserved {
				t.Fatal("STOP alone fabricated death")
			}
			c.Alive = false
			c.Health = 0
			c.UnitFlags = 0
			c.DynamicFlags = 1
			c.AttackVictimKnown = false
			c.AttackVictimGuid = 0
			c.ObservationAgeMs = 0
			f.world.Self.UnitFlags = 0
			f.world.ObservedPackets++
			s.Tick(context.Background())
			break
		}
		roadPacket(s, opcode.SMSGAttackerstateupdate, meleeDamageFixture(c.Guid, 8, 2, 0))
		f.world.Self.Health -= 2
		f.world.Self.VitalsAgeMs = 0
		f.world.Self.ObservationAgeMs = 0
		f.world.ObservedPackets++
		s.Tick(context.Background())
		if countPackets(f.fakeSession, opcode.CMSGCastSpell) != cast+1 {
			t.Fatal("stationary next cast missing", cast, s.roadSnapshotLocked())
		}
		if playerCreaturePosition(c) != raw || c.PositionRevision != 2 || c.Trajectory.Stopped {
			t.Fatal("test rewrote raw arrival")
		}
	}
	a := s.roadSnapshotLocked()
	valid := terminal == "retained_elapsed_pose" || terminal == "fresh_server_stop"
	if a.Active || a.SourceDeathObserved != valid || valid && (a.RemainingMs == 0 || a.SourceDeathPositionRevision != c.PositionRevision) || a.SourceGuid != c.Guid || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 7 || h2.PositionAgeMs <= 20000 || terminal == "retained_elapsed_pose" && c.PositionAgeMs <= 15000 || a.Origin.AgeMs <= 20000 {
		t.Fatal("real age lifecycle failed", a, h2.PositionAgeMs, c.PositionAgeMs)
	}
	for _, p := range f.packets {
		if p.Opcode != opcode.CMSGCastSpell && p.Opcode != opcode.MSGMoveSetFacing && (valid || p.Opcode != opcode.MSGMoveStop) {
			t.Fatal("road moved or autoattacked", p)
		}
	}
}

func TestRoadClearCausalAddSetAndFullFactionCoverage(t *testing.T) {
	for _, mode := range []string{"filtered_only_near_add", "missing_target", "saturated", "duplicate", "conflicting", "unknown_map"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, q, _ := roadRig(t)
			f.filtered = []*worldv1.CreatureState{c}
			a := proto.Clone(c).(*worldv1.CreatureState)
			a.Guid = 0xf130000000000000 | uint64(1718)<<24 | 99
			a.X = s.pos.x + 10
			switch mode {
			case "filtered_only_near_add":
				f.filtered = append(f.filtered, a)
			case "missing_target":
				f.filtered = []*worldv1.CreatureState{}
			case "saturated":
				for i := 1; i < 32; i++ {
					n := proto.Clone(c).(*worldv1.CreatureState)
					n.Guid += uint64(i)
					f.filtered = append(f.filtered, n)
				}
			case "duplicate":
				f.filtered = append(f.filtered, c)
			case "conflicting":
				n := proto.Clone(c).(*worldv1.CreatureState)
				n.Health--
				f.filtered[0] = n
			case "unknown_map":
				a.MapKnown = false
				f.filtered = append(f.filtered, a)
			}
			if got, err := s.StartRoadClear(context.Background(), q); err == nil && got.RoadClear.Active || len(f.packets) != 0 {
				t.Fatal("filtered coverage bypass", got, err)
			}
			if len(f.queries) == 0 || f.queries[0].CreatureFactionTemplate != 59 || f.queries[0].CreatureGuid != c.Guid {
				t.Fatal("not actual faction query")
			}
		})
	}
	for _, mode := range []string{"new", "missing", "moved", "moving", "unknown", "world_parse_failure", "world_counter_reset"} {
		t.Run("held_"+mode, func(t *testing.T) {
			s, f, c, q, _ := roadRig(t)
			a := proto.Clone(c).(*worldv1.CreatureState)
			a.Guid++
			a.X = -6167.99
			a.Y = 135.883
			a.Z = 423.193
			f.world.NearbyCreatures = append(f.world.NearbyCreatures, a)
			roadStart(t, s, q)
			switch mode {
			case "new":
				n := proto.Clone(a).(*worldv1.CreatureState)
				n.Guid++
				f.world.NearbyCreatures = append(f.world.NearbyCreatures, n)
			case "missing":
				f.world.NearbyCreatures = f.world.NearbyCreatures[:1]
			case "moved":
				a.X++
				a.PositionRevision++
			case "moving":
				a.MovementFlags = 1
			case "unknown":
				a.HealthKnown = false
			case "world_parse_failure":
				f.world.MalformedPackets++
			case "world_counter_reset":
				f.world.ObservedPackets = 99
			}
			s.Tick(context.Background())
			if s.road.active || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 0 {
				t.Fatal("changed held facts authorized offense", s.roadSnapshotLocked())
			}
		})
	}
}

func TestRoadClearTypedCastPoisonFailureScopeAndDeadlineReceipt(t *testing.T) {
	t.Run("foreign_failure_ignored_own_mismatch_poison", func(t *testing.T) {
		s, _, _, q, _ := roadRig(t)
		roadStart(t, s, q)
		s.Tick(context.Background())
		p := append(packedGUID(99), s.castCount)
		p = binary.LittleEndian.AppendUint32(p, shadowBolt)
		p = append(p, 1)
		roadPacket(s, opcode.SMSGSpellFailure, p)
		if !s.road.active || s.roadCast.poisoned {
			t.Fatal("foreign failure poisoned own cast")
		}
		p = append(packedGUID(8), s.castCount+1)
		p = binary.LittleEndian.AppendUint32(p, shadowBolt)
		p = append(p, 1)
		roadPacket(s, opcode.SMSGSpellFailure, p)
		if s.road.active || !s.roadCast.poisoned || !s.state.CastPending {
			t.Fatal("wrong own failure resolved pending")
		}
	})
	t.Run("poison_stays_after_initial_and_old_go", func(t *testing.T) {
		s, _, c, q, _ := roadRig(t)
		roadStart(t, s, q)
		s.Tick(context.Background())
		roadPacket(s, opcode.SMSGSpellStart, roadEvent(false, s.castCount, c.Guid))
		roadPacket(s, opcode.SMSGSpellGo, []byte{0})
		learnSpells(s, shadowBolt)
		roadPacket(s, opcode.SMSGSpellGo, roadEvent(true, s.castCount, c.Guid))
		if s.roadCast.known || !s.roadCast.poisoned || s.road.active {
			t.Fatal("ambiguity healed")
		}
	})
	t.Run("single_now_and_growing_origin_age", func(t *testing.T) {
		s, _, _, q, advance := roadRig(t)
		roadStart(t, s, q)
		advance(time.Second)
		a := s.roadSnapshotLocked()
		if a.Origin.AgeMs != 1000 || !a.Origin.Estimated || a.RemainingMs != 179000 {
			t.Fatal(a)
		}
		n := 0
		s.now = func() time.Time {
			n++
			if n == 1 {
				return s.road.until.Add(-time.Millisecond)
			}
			return s.road.until.Add(time.Millisecond)
		}
		a = s.roadSnapshotLocked()
		if n != 1 || a.RemainingMs != 1 || a.RemainingMs > 180000 {
			t.Fatal("deadline wrapped", a, n)
		}
	})
	t.Run("alive_stop_never_rearms", func(t *testing.T) {
		s, f, c, q, _ := roadRig(t)
		roadStart(t, s, q)
		s.Tick(context.Background())
		roadCastResult(s, c, 13)
		roadAttackStop(s, c.Guid, 0)
		s.Tick(context.Background())
		if s.road.active || s.roadSnapshotLocked().SourceDeathObserved || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 {
			t.Fatal(s.roadSnapshotLocked())
		}
	})
	t.Run("stop_before_go_and_actual_death", func(t *testing.T) {
		s, f, c, q, _ := roadRig(t)
		roadStart(t, s, q)
		s.Tick(context.Background())
		roadAttackStop(s, c.Guid, 1)
		s.Tick(context.Background())
		if s.roadSnapshotLocked().SourceDeathObserved || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 {
			t.Fatal("stop fabricated death")
		}
		roadPacket(s, opcode.SMSGSpellStart, roadEvent(false, s.castCount, c.Guid))
		roadPacket(s, opcode.SMSGSpellGo, roadEvent(true, s.castCount, c.Guid))
		roadPacket(s, opcode.SMSGSpellnonmeleedamagelog, roadDamage(c.Guid, 13))
		c.Health = 0
		c.Alive = false
		c.ObservationAgeMs = 0
		s.Tick(context.Background())
		if !s.roadSnapshotLocked().SourceDeathObserved {
			t.Fatal(s.roadSnapshotLocked())
		}
	})
}

func TestRoadClearSecondExplicitSourcePinsKnownDeadPriorMember(t *testing.T) {
	for _, change := range []string{"unchanged_dead", "resurrected", "unknown_health", "changed_flags", "missing", "changed_trajectory"} {
		t.Run(change, func(t *testing.T) {
			s, f, h1, q, advance := roadRig(t)
			roadStart(t, s, q)
			s.Tick(context.Background())
			roadCastResult(s, h1, 86)
			h1.Alive, h1.UnitFlags, h1.DynamicFlags = false, 0, 1
			h1.AttackVictimKnown, h1.AttackVictimGuid = false, 0
			h1.PositionRevision++
			h1.MovementFlags = 0x08000001
			h1.Trajectory = &worldv1.CreatureTrajectory{Known: true, DestinationKnown: true, DestinationX: s.pos.x + 2.5, DestinationY: s.pos.y, DestinationZ: s.pos.z, DurationMs: 3000, AgeMs: 4000}
			s.Tick(context.Background())
			if !s.roadSnapshotLocked().SourceDeathObserved {
				t.Fatal("first source not complete", s.roadSnapshotLocked())
			}
			// A later explicit source uses its own achieved standoff. The prior
			// corpse remains lootable, with its actual elapsed unSTOPPED spline.
			advance(4 * time.Second)
			s.pos = position{x: -6168, y: 135.883, z: 423.193}
			s.posAt = s.now()
			s.posRevision++
			f.world.Self.UnitFlags = 0
			f.world.Self.Health, f.world.Self.Power = 113, 283
			h2 := &worldv1.CreatureState{Guid: 0xf130000000000000 | 1718<<24 | 26630, Entry: 1718, Present: true, MapKnown: true, PositionKnown: true, X: -6148.47, Y: 135.883, Z: 423.193, PositionRevision: 1, LevelKnown: true, Level: 3, HealthKnown: true, Health: 71, MaxHealth: 71, AliveKnown: true, Alive: true, FactionTemplate: 59, UnitFlagsKnown: true, DynamicFlagsKnown: true, TargetKnown: true, CombatReachKnown: true, CombatReach: 1.05}
			f.world.NearbyCreatures = append(f.world.NearbyCreatures, h2)
			f.lookupCreatures[h2.Guid] = h2
			q2 := proto.Clone(q).(*actionsv1.RoadClearRequest)
			q2.RequestId, q2.TargetGuid, q2.ExpectedPosition = "road-two", h2.Guid, s.clientPositionLocked()
			roadStart(t, s, q2)
			switch change {
			case "resurrected":
				h1.Alive, h1.Health = true, h1.MaxHealth
			case "unknown_health":
				h1.HealthKnown = false
			case "changed_flags":
				h1.DynamicFlags = 0
			case "missing":
				f.world.NearbyCreatures = f.world.NearbyCreatures[1:]
			case "changed_trajectory":
				h1.Trajectory.DestinationX++
			}
			s.Tick(context.Background())
			want := 1
			if change == "unchanged_dead" {
				want = 2
			}
			if countPackets(f.fakeSession, opcode.CMSGCastSpell) != want || change == "unchanged_dead" && !s.road.active || change != "unchanged_dead" && s.road.active {
				t.Fatal("prior dead membership mishandled", s.roadSnapshotLocked())
			}
		})
	}
}

func TestRoadClearTypedUnexpectedAttackersCannotRemainHidden(t *testing.T) {
	for _, mode := range []string{"foreign_spell", "selected_spell", "foreign_attack_start", "other_victim_spell"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, q, _ := roadRig(t)
			roadStart(t, s, q)
			s.Tick(context.Background())
			caster, victim := uint64(99), uint64(8)
			if mode == "foreign_attack_start" {
				p := binary.LittleEndian.AppendUint64(nil, caster)
				p = binary.LittleEndian.AppendUint64(p, victim)
				roadPacket(s, opcode.SMSGAttackstart, p)
			} else {
				if mode == "selected_spell" {
					caster = c.Guid
				}
				if mode == "other_victim_spell" {
					victim = 7
				}
				p := append(packedGUID(victim), packedGUID(caster)...)
				tail := make([]byte, 32)
				binary.LittleEndian.PutUint32(tail, shadowBolt)
				binary.LittleEndian.PutUint32(tail[4:], 1)
				tail[12] = 32
				roadPacket(s, opcode.SMSGSpellnonmeleedamagelog, append(p, tail...))
			}
			s.Tick(context.Background())
			if s.road.active != (mode == "other_victim_spell") || !s.state.CastPending || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 {
				t.Fatal("typed attacker guard or retained pending failed", s.roadSnapshotLocked())
			}
		})
	}
}

func TestRoadClearTerminalEvidenceGraceAndPublicationEpoch(t *testing.T) {
	for _, mode := range []string{"stop_before_cutoff", "stop_at_cutoff", "stop_after_cutoff", "result_at_cutoff", "result_after_cutoff"} {
		t.Run(mode, func(t *testing.T) {
			s, _, c, q, advance := roadRig(t)
			roadStart(t, s, q)
			s.Tick(context.Background())
			roadPacket(s, opcode.SMSGSpellStart, roadEvent(false, s.castCount, c.Guid))
			roadPacket(s, opcode.SMSGSpellGo, roadEvent(true, s.castCount, c.Guid))
			roadPacket(s, opcode.SMSGSpellnonmeleedamagelog, roadDamage(c.Guid, 86))
			if mode == "result_at_cutoff" || mode == "result_after_cutoff" {
				advance(5 * time.Second)
			} else {
				roadAttackStop(s, c.Guid, 1)
				d := 2 * time.Second
				if mode == "stop_before_cutoff" {
					d -= time.Millisecond
				}
				advance(d)
			}
			if mode == "stop_after_cutoff" || mode == "result_after_cutoff" {
				advance(time.Millisecond)
			}
			c.Health, c.Alive = 0, false
			s.Tick(context.Background())
			if s.roadSnapshotLocked().SourceDeathObserved != (mode == "stop_before_cutoff") {
				t.Fatal("late polling bypassed terminal/result cutoff", s.roadSnapshotLocked())
			}
		})
	}
	for _, mode := range []string{"unchanged", "malformed", "counter_regression", "raw_revision", "own_unknown"} {
		t.Run("terminal_status_"+mode, func(t *testing.T) {
			s, f, c, q, _ := roadRig(t)
			roadStart(t, s, q)
			s.Tick(context.Background())
			roadCastResult(s, c, 86)
			c.Alive = false
			f.world.Self.UnitFlags = 0
			s.Tick(context.Background())
			if !s.roadSnapshotLocked().SourceDeathObserved {
				t.Fatal("test has no actual death")
			}
			switch mode {
			case "malformed":
				f.world.MalformedPackets++
			case "counter_regression":
				f.world.ObservedPackets--
			case "raw_revision":
				f.world.Self.PositionRevision++
			case "own_unknown":
				f.world.Self.HealthKnown = false
			}
			a, err := s.Status(context.Background(), &emptypb.Empty{})
			if err != nil || !a.RoadClear.SourceDeathObserved || a.RoadClear.SourceGuid != c.Guid || (a.RoadClear.RemainingMs > 0) != (mode == "unchanged") {
				t.Fatal("terminal authority survived unavailable proof or erased history", a, err)
			}
		})
	}
	t.Run("stop_after_go_cannot_replace_unresolved_result", func(t *testing.T) {
		s, f, c, q, _ := roadRig(t)
		roadStart(t, s, q)
		s.Tick(context.Background())
		roadPacket(s, opcode.SMSGSpellStart, roadEvent(false, s.castCount, c.Guid))
		roadPacket(s, opcode.SMSGSpellGo, roadEvent(true, s.castCount, c.Guid))
		_, _ = s.Practice(context.Background(), &actionsv1.ToggleRequest{})
		if s.state.CastPending || !s.road.waitingResult || !s.roadCast.known || s.roadCast.busy {
			t.Fatal("test did not reach resolved GO/unresolved damage")
		}
		q2 := proto.Clone(q).(*actionsv1.RoadClearRequest)
		q2.RequestId, q2.TargetGuid = "different-request", c.Guid+1
		if a, err := s.StartRoadClear(context.Background(), q2); err == nil || a != nil || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 || s.road.target != c.Guid {
			t.Fatal("new target replaced unknown prior result", a, err)
		}
	})
}
