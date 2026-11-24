package buddyactions

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

func TestPracticeLifecycleRankingValidatesOnlyCandidatesThatCanReplaceWinner(t *testing.T) {
	for _, mode := range []string{"last_valid", "last_claimed", "invalid_first"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := practiceRig(t, 8)
			base := f.world.NearbyCreatures[0]
			f.world.NearbyCreatures = nil
			for i := 0; i < 32; i++ {
				row := proto.Clone(base).(*worldv1.CreatureState)
				row.Guid, row.X = base.Guid+uint64(i), -8830
				f.world.NearbyCreatures = append(f.world.NearbyCreatures, row)
			}
			last := f.world.NearbyCreatures[31]
			last.X = -8930
			input, known := s.practiceLifecycleFactsLocked(f.world, "tick")
			if !known || len(input["nearby"].([]any)) != 32 {
				t.Fatal("complete candidate frame was lost")
			}
			want := last.Guid
			rows := input["nearby"].([]any)
			switch mode {
			case "last_claimed":
				rows[31].(map[string]any)["attackVictimKnown"] = true
				rows[31].(map[string]any)["attackVictimGuid"] = "3"
				want = base.Guid
			case "invalid_first":
				rows[0].(map[string]any)["guid"] = "malformed"
			}
			// Count actual sandbox byte lookups around the maintained selector;
			// no policy body, candidate row or validation is replaced. This is a
			// semantic work bound, separate from the untouched default5ms test.
			source := `local original_byte_at=byte_at;local calls=0
byte_at=function(value,index) calls=calls+1;return original_byte_at(value,index) end
local actual=(function()
` + behaviors.StarterSpellSource + `
end)()
return {decide=function(input)
 calls=0;local decision=actual.decide(input)
 return {decision=decision,byteCalls=calls}
end}`
			r, err := behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.Reload(source, ""); err != nil {
				t.Fatal(err)
			}
			out, err := r.EvaluateContext(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			decision, ok := out["decision"].(map[string]any)
			calls, measured := out["byteCalls"].(float64)
			if !ok || decision["kind"] != "select" || decision["targetGuid"] != strconv.FormatUint(want, 10) || !measured || calls <= 0 || calls > 128 {
				t.Fatal("ranking changed or repeated identity work for non-improving rows", out, want)
			}
		})
	}
}
