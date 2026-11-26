package behavior

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCompiledPrototypeReuseKeepsFreshVMAndRuntimeCustody(t *testing.T) {
	const source = `local count=0 local private={} return {decide=function(input) count=count+1 private.id=input.id input.id="mutated" return {count=count,id=private.id} end}`
	first := New()
	rev, err := first.Reload(source, "")
	if err != nil {
		t.Fatal(err)
	}
	second := New()
	if _, err = second.Reload(source, ""); err != nil {
		t.Fatal(err)
	}
	if first.active == second.active || first.active.proto != second.active.proto {
		t.Fatal("immutable prototype was not shared independently of runtime policy")
	}
	var wg sync.WaitGroup
	for index, r := range []*Runtime{first, second} {
		wg.Add(1)
		go func(id int, r *Runtime) {
			defer wg.Done()
			for range 4 {
				input := map[string]any{"id": fmt.Sprint(id)}
				out, err := r.Evaluate(input)
				if err != nil || out["count"] != float64(1) || out["id"] != fmt.Sprint(id) || input["id"] != fmt.Sprint(id) {
					t.Errorf("shared mutable VM/input state: %v %v %v", out, input, err)
				}
			}
		}(index, r)
	}
	wg.Wait()
	if _, err = second.Reload(source, "wrong"); !errors.Is(err, ErrRevisionConflict) || second.Status().Revision != rev {
		t.Fatal("cache bypassed CAS", err, second.Status())
	}
	limited, err := NewWithSourceLimit(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = limited.Reload(source, ""); err == nil {
		t.Fatal("cached source bypassed source limit")
	}
	tiny, err := NewWithInitializationLimit(SessionOptions{}, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tiny.Reload(source, ""); !errors.Is(err, ErrExecutionBudget) || tiny.Status().Revision != "" {
		t.Fatal("cache skipped actual chunk initialization budget", err, tiny.Status())
	}
}

func TestCompiledPrototypeCacheRetainsOnlyLastAcceptedChunk(t *testing.T) {
	a, b := New(), New()
	const sourceA = `return {decide=function(input)return {value="A"}end}`
	const sourceB = `return {decide=function(input)return {value="B"}end}`
	_, err := a.Reload(sourceA, "")
	if err != nil {
		t.Fatal(err)
	}
	old := a.active.proto
	bRevision, err := b.Reload(sourceB, "")
	if err != nil {
		t.Fatal(err)
	}
	lastCompiledPolicy.RLock()
	cached := lastCompiledPolicy.policy
	lastCompiledPolicy.RUnlock()
	if cached.revision != bRevision || cached.proto != b.active.proto {
		t.Fatal("cache is not a single most-recent accepted prototype")
	}
	if _, err = b.Reload(`error("reject")`, bRevision); err == nil {
		t.Fatal("invalid chunk accepted")
	}
	lastCompiledPolicy.RLock()
	cached = lastCompiledPolicy.policy
	lastCompiledPolicy.RUnlock()
	if cached.revision != bRevision {
		t.Fatal("failed chunk replaced accepted cache entry")
	}
	c := New()
	if _, err = c.Reload(sourceA, ""); err != nil {
		t.Fatal(err)
	}
	if c.active.proto == old {
		t.Fatal("evicted prototype was retained by cache")
	}
	out, err := c.Evaluate(map[string]any{})
	if err != nil || out["value"] != "A" {
		t.Fatal(out, err)
	}
}
