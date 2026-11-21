package buddyactions

import (
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	"github.com/agent-wow/agent-wow/pkg/behavior"
)

// formationTarget describes a personal place near the owner, not permission to
// move there. The caller retains world safety, navigation and movement control.
type formationTarget struct {
	Position       position
	StartDistance  float64
	StopDistance   float64
	Ready          bool // Gates a new start; it must not interrupt an existing move.
	IdleReposition bool // No idle fidgeting in this first formation controller.
	Hold           bool
	Phase          string
	Valid          bool
}

// followFormation keeps detached policy memory and a private builtin runtime
// under the controller lock. Attached policies compose the same source.
type followFormation struct {
	memory         map[string]any
	runtime        *behavior.Runtime
	remote         func(map[string]any) (bool, string, map[string]any, error)
	sourceRevision string
}

func (f *followFormation) Reset() { f.memory = nil }

func (f *followFormation) decide(input map[string]any) map[string]any {
	if f.remote != nil {
		available, revision, output, err := f.remote(input)
		if err != nil {
			return nil // Never replace a failed active edit with an older default.
		}
		if available {
			f.sourceRevision = revision
			return output
		}
	}
	if f.runtime == nil {
		runtime := behavior.New()
		if _, err := runtime.Reload(behaviors.FollowFormationSource, ""); err != nil {
			return nil
		}
		f.runtime = runtime
	}
	f.sourceRevision = f.runtime.Status().Revision
	output, err := f.runtime.Evaluate(input)
	if err != nil {
		// Preserve the detached committed memory if a pure evaluation fails.
		f.runtime = nil
		return nil
	}
	return output
}

func formationAngle(value float64) float64 {
	value = math.Mod(value, 2*math.Pi)
	if value < 0 {
		value += 2 * math.Pi
	}
	return value
}

func formationAngleDifference(a, b float64) float64 {
	return math.Remainder(a-b, 2*math.Pi)
}

func (f *followFormation) Target(now time.Time, selfGUID uint64, classID uint32, owner position, ownerMoving bool, ownerFlags ...uint32) formationTarget {
	return f.evaluate(now, selfGUID, classID, owner, ownerMoving, nil, false, false, ownerFlags, "")
}

func (f *followFormation) Plan(now time.Time, selfGUID uint64, classID uint32, owner position, ownerMoving bool, selfPos position, selfMoving, ownerArrival bool, ownerFlags ...uint32) formationTarget {
	return f.evaluate(now, selfGUID, classID, owner, ownerMoving, &selfPos, selfMoving, ownerArrival, ownerFlags, "")
}

func (f *followFormation) NearOwner(now time.Time, selfGUID uint64, owner position, ownerMoving bool, selfPos position, selfMoving bool, ownerFlags ...uint32) formationTarget {
	return f.evaluate(now, selfGUID, 0, owner, ownerMoving, &selfPos, selfMoving, false, ownerFlags, "near_owner")
}

func (f *followFormation) evaluate(now time.Time, selfGUID uint64, classID uint32, owner position, ownerMoving bool, selfPos *position, selfMoving, ownerArrival bool, ownerFlags []uint32, mode string) formationTarget {
	if selfGUID == 0 || !finite(owner) || selfPos != nil && !finite(*selfPos) {
		f.Reset()
		return formationTarget{}
	}
	input := map[string]any{
		"nowMs": float64(now.UnixMilli()), "selfGuid": strconv.FormatUint(selfGUID, 10), "classId": classID,
		"owner":        map[string]any{"x": owner.x, "y": owner.y, "z": owner.z, "o": owner.o, "moving": ownerMoving},
		"ownerArrival": ownerArrival,
	}
	if len(ownerFlags) != 0 {
		input["owner"].(map[string]any)["playerFlags"] = ownerFlags[0]
	}
	if mode != "" {
		input["mode"] = mode
	}
	if f.memory != nil {
		input["memory"] = f.memory
	}
	if selfPos != nil {
		input["self"] = map[string]any{"x": selfPos.x, "y": selfPos.y, "z": selfPos.z, "moving": selfMoving}
	}
	output := f.decide(input)
	if output == nil {
		return formationTarget{}
	}
	var result struct {
		Position                    struct{ X, Y, Z, O float32 }
		StartDistance, StopDistance float64
		Ready, Hold                 *bool
		Phase                       string
		Memory                      map[string]any
	}
	raw, err := json.Marshal(output)
	if err != nil || json.Unmarshal(raw, &result) != nil {
		f.Reset()
		return formationTarget{}
	}
	pos := position{result.Position.X, result.Position.Y, result.Position.Z, result.Position.O}
	validPhase := result.Hold != nil && (result.Phase == "following_formation" && !*result.Hold ||
		(result.Phase == "within_personal_follow_space" || result.Phase == "formation_reaction_delay" ||
			result.Phase == "within_follow_distance" || result.Phase == "follow_target_too_far_or_vertical") && *result.Hold)
	if !finite(pos) || math.IsNaN(result.StartDistance) || math.IsInf(result.StartDistance, 0) ||
		math.IsNaN(result.StopDistance) || math.IsInf(result.StopDistance, 0) || result.StopDistance < .1 ||
		result.StopDistance > 4 || result.StartDistance < result.StopDistance || result.StartDistance > 10 ||
		result.Ready == nil || !validPhase || len(result.Memory) == 0 || result.Memory["guid"] != input["selfGuid"] {
		f.Reset()
		return formationTarget{}
	}
	f.memory = result.Memory
	return formationTarget{Position: pos, StartDistance: result.StartDistance, StopDistance: result.StopDistance,
		Ready: *result.Ready, Hold: *result.Hold, Phase: result.Phase, Valid: true}
}
