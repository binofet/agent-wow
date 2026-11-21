package buddyactions

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGhostRecoveryNativeMaintenanceNeverChoosesRouteOrReclaim(t *testing.T) {
	s, f, advance := recoveryRig(t)
	// The legacy false toggle cannot install a disabled ordinary policy state.
	s.Recovery(context.Background(), &actionsv1.ToggleRequest{})
	tickDeathLua(t, s)
	ghostAt(f, 50)
	tickDeathLua(t, s)
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
	recoveryObserve(t, s, opcode.SMSGCorpseReclaimDelay, binary.LittleEndian.AppendUint32(nil, 0))
	before := len(f.packets)
	for range 400 {
		s.Tick(context.Background())
		advance(200 * time.Millisecond)
	}
	if len(f.packets) != before || s.state.Moving || s.recovery.state.ReclaimAttempted {
		t.Fatal("native maintenance chose death gameplay", s.stateLocked(), f.packets)
	}
	tickDeathLua(t, s)
	if countOpcode(f, opcode.CMSGReclaimCorpse) != 1 || !s.recovery.state.ReclaimAttempted || s.recovery.state.AliveObserved {
		t.Fatal("ordinary Lua failed to reclaim without enable orders", s.stateLocked(), f.packets)
	}
}
func TestGhostRecoverySharedLuaRangeTuningReachesExactNativePlanner(t *testing.T) {
	s, f, _ := recoveryRig(t)
	tickDeathLua(t, s)
	ghostAt(f, 26)
	tickDeathLua(t, s)
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
	recoveryObserve(t, s, opcode.SMSGCorpseReclaimDelay, binary.LittleEndian.AppendUint32(nil, 0))
	vmRaw, _ := recoveryPolicies.Load(s)
	vm := vmRaw.(*behavior.Session)
	source := strings.Replace(deathPolicySource(t), "reclaim_range=35", "reclaim_range=10", 1)
	if _, err := vm.Reload(context.Background(), source, vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	var observed float64
	s.planRecoveryPath = func(ctx context.Context, root string, mid uint32, from, to position, radius float64) ([]position, error) {
		observed = radius
		return s.mapNavigationPath(ctx, root, mid, from, to)
	}
	tickDeathLua(t, s)
	if observed != 10 || countOpcode(f, opcode.CMSGReclaimCorpse) != 0 || countOpcode(f, opcode.MSGMoveStartForward) != 1 {
		t.Fatal("same-session radius edit did not control exact approach", observed, s.stateLocked(), f.packets)
	}
}
func TestGhostRecoveryExactPrimitivesRejectForeignQueryPoseCorpseAndControl(t *testing.T) {
	for _, mode := range []string{"foreign_body", "stale_death", "stale_pose", "stale_query", "control_lost", "unsupported", "foreign_corpse", "bones", "corpse_revision"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := recoveryRig(t)
			tickDeathLua(t, s)
			ghostAt(f, 50)
			tickDeathLua(t, s)
			recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
			recoveryObserve(t, s, opcode.SMSGCorpseReclaimDelay, binary.LittleEndian.AppendUint32(nil, 0))
			q := &actionsv1.CorpseApproachRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, DeathEpoch: s.recovery.state.DeathEpoch, ExpectedPositionRevision: f.world.Self.PositionRevision, CorpseQueryRevision: s.recovery.state.CorpseQueryRevision, RangeYards: 5}
			r := &actionsv1.ReclaimCorpseRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, DeathEpoch: q.DeathEpoch, ExpectedPositionRevision: q.ExpectedPositionRevision, CorpseGuid: ownCorpseGUID, ExpectedCorpseRevision: 1}
			switch mode {
			case "foreign_body":
				q.SelfGuid++
				r.SelfGuid++
			case "stale_death":
				q.DeathEpoch++
				r.DeathEpoch++
			case "stale_pose":
				q.ExpectedPositionRevision++
				r.ExpectedPositionRevision++
			case "stale_query":
				q.CorpseQueryRevision++
			case "control_lost":
				s.clientControlLost = true
			case "unsupported":
				f.world.Self.MovementFlags |= 0x200
			case "foreign_corpse":
				f.world.OwnCorpse.OwnerGuid = 99
			case "bones":
				f.world.OwnCorpse.Flags = 1
			case "corpse_revision":
				r.ExpectedCorpseRevision++
			}
			before := len(f.packets)
			var err error
			if mode == "foreign_corpse" || mode == "bones" || mode == "corpse_revision" {
				_, err = s.ReclaimCorpse(context.Background(), r)
			} else {
				_, err = s.ApproachCorpse(context.Background(), q)
			}
			if status.Code(err) != codes.FailedPrecondition || len(f.packets) != before {
				t.Fatal("foreign/stale physical intent wrote", mode, err, s.stateLocked(), f.packets)
			}
		})
	}
}
