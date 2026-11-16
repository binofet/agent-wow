package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func putAtPortal(p *worldv1.PlayerState, mapID uint32, pos position) {
	p.MapId, p.X, p.Y, p.Z = mapID, pos.x, pos.y, pos.z
}

func TestFollowDungeonPortalUsesOrdinaryEntryAfterOwnerDisappears(t *testing.T) {
	for _, portal := range dungeonEntrances {
		t.Run(strconv.FormatUint(uint64(portal.trigger), 10), func(t *testing.T) {
			ctx := context.Background()
			s, f, advance := rig(t)
			s.navDataDir = "fixture"
			start := portal.center
			start.y -= float32(portal.radius + 2)
			putAtPortal(f.world.Self, portal.source, start)
			putAtPortal(f.world.Owner, portal.source, portal.center)
			toggle(t, s, true)
			s.Tick(ctx)
			f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
			for range 16 {
				advance(250 * time.Millisecond)
				s.Tick(ctx)
			}
			var triggerWrites int
			for _, packet := range f.packets {
				if packet.Opcode == opcode.CMSGAreatrigger {
					triggerWrites++
					if !bytes.Equal(packet.Payload, binary.LittleEndian.AppendUint32(nil, portal.trigger)) {
						t.Fatalf("portal packet %x", packet.Payload)
					}
				}
			}
			if triggerWrites != 1 || !s.state.Following || !s.followPortal.notified ||
				distance(s.pos, portal.center) >= portal.radius {
				t.Fatalf("portal writes %d, reason %s, pose %+v, follow %t", triggerWrites, s.state.Reason, s.pos, s.state.Following)
			}
			f.world.Self.MapId = portal.destination
			f.world.Self.PositionRevision++
			advance(250 * time.Millisecond)
			s.Tick(ctx)
			if s.state.Reason != "follow_instance_transfer_observed" || !s.state.Following {
				t.Fatalf("transfer did not preserve follow: %+v", &s.state)
			}
		})
	}
}

func TestFollowDungeonPortalCatchesUpThroughVerifiedBend(t *testing.T) {
	s, f, advance := rig(t)
	ctx := context.Background()
	portal := dungeonEntrances[3]
	start := portal.center
	start.y -= 200
	putAtPortal(f.world.Self, portal.source, start)
	putAtPortal(f.world.Owner, portal.source, portal.center)
	s.navDataDir = "fixture"
	bend := position{x: start.x + 6, y: start.y + 8, z: start.z}
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		if distance(from, start) < .1 && distance(to, portal.center) < .1 {
			return nil, errors.New("direct ground corridor unavailable")
		}
		return []position{from, to}, nil
	}
	toggle(t, s, true)
	s.Tick(ctx)
	lostAt := s.now()
	f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
	var visitedBend, advancedFromBend bool
	for range 155 {
		advance(250 * time.Millisecond)
		s.Tick(ctx)
		if distance(s.pos, bend) < .1 {
			visitedBend = true
		}
		if visitedBend && s.pos.y > bend.y+10 {
			advancedFromBend = true
		}
	}
	writes := 0
	for _, packet := range f.packets {
		if packet.Opcode == opcode.CMSGAreatrigger {
			writes++
			if !bytes.Equal(packet.Payload, binary.LittleEndian.AppendUint32(nil, portal.trigger)) {
				t.Fatalf("wrong portal packet: %x", packet.Payload)
			}
		}
	}
	if !visitedBend || !advancedFromBend || writes != 1 || s.now().Sub(lostAt) <= 20*time.Second {
		t.Fatalf("portal progression incomplete: bend=%t, advanced=%t, writes=%d, position=%+v, reason=%s", visitedBend, advancedFromBend, writes, s.pos, s.state.Reason)
	}
}

func TestFollowDungeonPortalDoesNotEnterWithoutCurrentAuthority(t *testing.T) {
	for name, change := range map[string]func(*Service, *fakeSession){
		"owner still visible": func(_ *Service, f *fakeSession) { f.world.Owner.Present = true },
		"owner offline":       func(_ *Service, f *fakeSession) { f.party.Group.Members[1].Online = false },
		"in combat":           func(_ *Service, f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"not following":       func(s *Service, _ *fakeSession) { s.state.Following = false },
		"stale observation":   func(s *Service, _ *fakeSession) { s.followPortal.seenAt = s.now().Add(-21 * time.Second) },
		"outside entrance": func(s *Service, f *fakeSession) {
			f.world.Self.Y -= 10
			f.world.Self.PositionRevision++
			s.navDataDir = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, f, advance := rig(t)
			portal := dungeonEntrances[0]
			putAtPortal(f.world.Self, portal.source, portal.center)
			putAtPortal(f.world.Owner, portal.source, portal.center)
			toggle(t, s, true)
			s.Tick(ctx)
			f.world.Owner.Present = false
			advance(250 * time.Millisecond)
			s.Tick(ctx)
			change(s, f)
			advance(time.Second)
			s.Tick(ctx)
			for _, packet := range f.packets {
				if packet.Opcode == opcode.CMSGAreatrigger {
					t.Fatalf("sent unauthorized portal packet %x", packet.Payload)
				}
			}
		})
	}
}
