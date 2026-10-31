package buddyactions

import (
	"os"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
)

// Fields71/72 belong to the independently integrated discovery acknowledgement.
// A standalone road patch must retain its distinct wire number when combined.
func TestRoadClearWireContractAndPublishedMethod(t *testing.T) {
	api := actionsv1.File_modules_buddy_actions_actions_proto
	state := api.Messages().ByName("State")
	road := state.Fields().ByName("road_clear")
	if road == nil || road.Number() != 73 || road.Message().FullName() != "warcraftbuddy.actions.v1.RoadClearState" {
		t.Fatal("road handoff must use independent field73", road)
	}
	leveling := state.Fields().ByName("coldridge_leveling")
	if leveling == nil || leveling.Number() != 70 {
		t.Fatal("road handoff changed existing leveling wire field", leveling)
	}
	method := api.Services().ByName("BuddyActions").Methods().ByName("StartRoadClear")
	if method == nil || method.Input().FullName() != "warcraftbuddy.actions.v1.RoadClearRequest" || method.Output().FullName() != "warcraftbuddy.actions.v1.State" {
		t.Fatal("road RPC contract missing or changed", method)
	}
	manifest, err := os.ReadFile("module.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(manifest), "  start_road_clear: /warcraftbuddy.actions.v1.BuddyActions/StartRoadClear\n") != 1 {
		t.Fatal("road RPC must be published exactly once")
	}
}
