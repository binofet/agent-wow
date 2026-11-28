package world

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestIndependentDefaultLuaSelectsOnlyObservedRealShip(t *testing.T) {
	const hullGUID = 0x1fc0000000000123
	for _, kind := range []string{"real_board", "real_ride", "real_leave", "no_observed_hull", "confirmed_elevator", "unrelated_hull", "absent_owner", "unknown_self_origin"} {
		t.Run(kind, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := fallbackObservation()
			me := o.Players[fallbackGUID(fallbackSelf)]
			me.PositionKnown = true
			me.MovementKnown = true
			me.PositionRevision = 1
			owner := fallbackPlayer(3)
			owner.PositionKnown = true
			owner.MovementKnown = true
			owner.PositionRevision = 9
			o.Players["3"] = owner
			surface := &actionsv1.MovingSurfaceObservation{Supported: true, SelfAttachmentKnown: true, Owner: &worldv1.PassengerMovementFacts{Revision: 7, VisitRevision: o.VisitId, Info: &worldv1.MovementInfoFacts{
				Flags: 0x200, Transport: &worldv1.MovementTransportFacts{Guid: hullGUID, Seat: -1}}}}
			hull := &worldv1.GameObjectState{Guid: hullGUID, Entry: 176310, Present: true, MapKnown: true, MapId: 0, TypeKnown: true, GameObjectType: 15, PositionKnown: true, MovingTransport: &worldv1.MovingTransportFacts{ObjectRevision: 3}}
			surface.Transports = []*worldv1.GameObjectState{hull}
			want := ""
			switch kind {
			case "real_board":
				want = "board"
			case "real_ride":
				surface.SelfTransportGuid = hullGUID
				want = "ride"
			case "real_leave":
				surface.SelfTransportGuid = hullGUID
				surface.Owner.Info.Transport = nil
				surface.Owner.Info.Flags = 0
				want = "leave"
			case "no_observed_hull":
				surface.Transports = nil
			case "confirmed_elevator":
				surface.Owner.Info.Transport.Guid = 0xf110000000000123
				surface.Transports = nil
			case "unrelated_hull":
				hull.Guid++
			case "unknown_self_origin":
				surface.SelfAttachmentKnown = false
			case "absent_owner":
				owner.Present = false
			}
			o.Navigation = &actionsv1.NavigationObservation{Following: true, FollowFormationSupported: true, GroundNavigationConfigured: true, OwnerGuid: 3, FollowRevision: 1, FollowOwner: owner, MovingSurface: surface}
			o.HostOwned = proto.Bool(true)
			decision := p.decide(p.observe(o))
			goal, _ := decision.Output["followFormation"].(map[string]any)
			got, _ := goal["surfaceMode"].(string)
			if got != want {
				t.Fatalf("normal mode %q, want %q, actual goal=%+v phase=%v", got, want, goal, decision.Output["phase"])
			}
		})
	}
}
