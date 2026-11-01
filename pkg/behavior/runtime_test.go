package behavior

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

const goodPolicy = `return {decide=function(state) return {action="hold", health=state.health or 0} end}`

func TestRichMissionInputHasSeparateFiniteBudget(t *testing.T) {
	component := func(n int) map[string]any {
		return map[string]any{"values": make([]any, n-3)}
	}
	// Neutral shape preserves the measured component costs of the actual
	// 19,766-byte rejected mission frame without embedding private gameplay.
	input := map[string]any{
		"goal": component(414), "memory": component(395), "last": component(101),
		"body":              map[string]any{"sessionId": "body", "selfGuid": "5"},
		"observations":      map[string]any{"actions": component(45), "professions": component(485), "quests": component(551), "world": component(48)},
		"observationErrors": map[string]any{}, "pending": []any{}, "shared": map[string]any{}, "now": 1,
	}
	// Lua arrays cannot contain null holes; false is an actual retained value.
	for _, v := range []map[string]any{input["goal"].(map[string]any), input["memory"].(map[string]any), input["last"].(map[string]any)} {
		for i := range v["values"].([]any) {
			v["values"].([]any)[i] = false
		}
	}
	for _, raw := range input["observations"].(map[string]any) {
		rows := raw.(map[string]any)["values"].([]any)
		for i := range rows {
			rows[i] = false
		}
	}
	b := &budget{nodeLimit: MaxInputNodes}
	if _, err := toLua(nil, input, b); err != nil || b.nodes != 2065 {
		t.Fatal("measured input component budget changed", b.nodes, err)
	}
	r := New()
	load(t, r, `return {decide=function(s) return {phase="valid",professions=#s.observations.professions.values} end}`)
	if out, err := r.Evaluate(input); err != nil || out["professions"] != float64(482) {
		t.Fatal(out, err)
	}
	// Exact 4096-node input succeeds; one extra input value remains rejected.
	rows := make([]any, MaxInputNodes-3)
	for i := range rows {
		rows[i] = false
	}
	load(t, r, `return {decide=function(s) return {count=#s.rows} end}`)
	started := time.Now()
	if _, err := r.Evaluate(map[string]any{"rows": rows}); err != nil {
		t.Fatal(err)
	}
	t.Logf("exact 4096-node input full evaluation: %s", time.Since(started))
	if _, err := r.Evaluate(map[string]any{"rows": append(rows, false)}); err == nil {
		t.Fatal("input budget became unbounded")
	}
	load(t, r, `return {decide=function(s) local out={}; for i=1,2100 do out[i]=false end; return {rows=out} end}`)
	if _, err := r.Evaluate(nil); err == nil {
		t.Fatal("input allowance widened output budget")
	}
	if MaxNodes != 2048 || MaxJSONBytes != 32768 || MaxDepth != 16 || EvaluationLimit != 5*time.Millisecond {
		t.Fatal("unrelated limits changed")
	}
}

func TestComposedPolicySourceAboveJSONFrameLimit(t *testing.T) {
	// Three independently closed-over modules model real source composition;
	// their retained constants are never smuggled into the JSON frame.
	var source strings.Builder
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&source, `local m%d=(function() local data="%s"; return {size=function() return #data end, step=function(v) return v+%d end} end)();`, i, strings.Repeat("x", 12000), i)
	}
	source.WriteString(`return {decide=function(s) return {value=m3.step(m2.step(m1.step(s.value))), sourceDataBytes=m1.size()+m2.size()+m3.size()} end}`)
	if source.Len() <= 32*1024 {
		t.Fatal("fixture does not exercise source composition above the former limit")
	}
	r := New()
	revision := load(t, r, source.String())
	out, err := r.Evaluate(map[string]any{"value": 7})
	if err != nil || out["value"] != float64(13) || out["sourceDataBytes"] != float64(36000) {
		t.Fatal(out, err)
	}
	if _, err := r.Reload(source.String()+strings.Repeat(" ", MaxSourceBytes), revision); err == nil || r.Status().Revision != revision {
		t.Fatal("oversize source replaced the working modules", err, r.Status())
	}
	if _, err := r.Evaluate(map[string]any{"value": 7, "oversize": strings.Repeat("x", 32*1024)}); err == nil || r.Status().Revision != revision {
		t.Fatal("larger source widened the JSON input frame", err, r.Status())
	}
	if MaxJSONBytes != 32*1024 || EvaluationLimit != 5*time.Millisecond {
		t.Fatal("source-only change widened execution authority")
	}
}

func load(t *testing.T, r *Runtime, source string) string {
	t.Helper()
	rev, err := r.Reload(source, r.Status().Revision)
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

func TestRevisionSwapAndStaleWriter(t *testing.T) {
	r := New()
	if _, err := r.Evaluate(nil); !errors.Is(err, ErrNoPolicy) {
		t.Fatal(err)
	}
	first := load(t, r, goodPolicy)
	out, err := r.Evaluate(map[string]any{"health": 37})
	if err != nil || out["health"] != float64(37) || out["action"] != "hold" {
		t.Fatal(out, err)
	}
	second := load(t, r, `return {decide=function(s) return {action="heal", targets={"self", "party"}} end}`)
	if first == second || len(second) != 64 {
		t.Fatal("revision did not bind source")
	}
	if got, err := r.Reload(goodPolicy, first); !errors.Is(err, ErrRevisionConflict) || got != second {
		t.Fatal(got, err)
	}
	out, err = r.Evaluate(nil)
	if err != nil || out["action"] != "heal" || len(out["targets"].([]any)) != 2 || r.Status().LastGoodRevision != second {
		t.Fatal(out, err, r.Status())
	}
}

func TestInvalidReloadPreservesWorkingRevision(t *testing.T) {
	for _, source := range []string{"", strings.Repeat(" ", MaxSourceBytes+1), "return {", `error("initialization failed")`, "return 7", "return {}", "return {decide=type}"} {
		t.Run(fmt.Sprintf("%q", source[:min(len(source), 40)]), func(t *testing.T) {
			r := New()
			first := load(t, r, goodPolicy)
			if _, err := r.Reload(source, first); err == nil || r.Status().Revision != first {
				t.Fatal(err, r.Status())
			}
			out, err := r.Evaluate(map[string]any{"health": 21})
			if err != nil || out["health"] != float64(21) {
				t.Fatal(out, err)
			}
		})
	}
}

func TestRuntimeFailureRestoresGoodPolicyWithOriginalInput(t *testing.T) {
	r := New()
	first := load(t, r, goodPolicy)
	if _, err := r.Evaluate(nil); err != nil {
		t.Fatal(err)
	}
	bad := `return {decide=function(s) s.health=0; error("bad candidate") end}`
	second := load(t, r, bad)
	input := map[string]any{"health": 42}
	out, err := r.Evaluate(input)
	status := r.Status()
	if err != nil || out["health"] != float64(42) || input["health"] != 42 || status.Revision != first || status.QuarantinedRevision != second || status.Failures != 1 {
		t.Fatal(out, input, err, status)
	}
	if _, err := r.Reload(bad, first); err == nil {
		t.Fatal("quarantined source rearmed")
	}
}

func TestNoGlobalOrClosureStateLeaksAcrossEvaluations(t *testing.T) {
	r := New()
	load(t, r, `local count=0; return {decide=function(s) count=count+1; globalCount=(globalCount or 0)+1; s.child.n=99; return {count=count,global=globalCount} end}`)
	input := map[string]any{"child": map[string]any{"n": 7}}
	for i := 0; i < 3; i++ {
		out, err := r.Evaluate(input)
		if err != nil || out["count"] != float64(1) || out["global"] != float64(1) || input["child"].(map[string]any)["n"] != 7 {
			t.Fatal(out, err, input)
		}
	}
}

func TestInfiniteLoopDeadlineAndFallback(t *testing.T) {
	for _, chunkLoop := range []bool{false, true} {
		t.Run(fmt.Sprint(chunkLoop), func(t *testing.T) {
			r := New()
			first := load(t, r, goodPolicy)
			if _, err := r.Evaluate(nil); err != nil {
				t.Fatal(err)
			}
			source := `return {decide=function(s) while true do end end}`
			if chunkLoop {
				source = `while true do end`
			}
			start := time.Now()
			_, err := r.Reload(source, first)
			if chunkLoop && err == nil || !chunkLoop && err != nil {
				t.Fatal(err)
			}
			out, err := r.Evaluate(nil)
			if err != nil || out["action"] != "hold" || r.Status().Revision != first || r.Status().Failures != 1 {
				t.Fatal(out, err, r.Status())
			}
			if time.Since(start) > time.Second {
				t.Fatal("cooperative Lua loop was not cancelled promptly")
			}
		})
	}
}

func TestUnsafeLibrariesAndHostAccessAbsent(t *testing.T) {
	r := New()
	load(t, r, `return {decide=function(s)
local names={"io","os","debug","package","require","module","load","loadstring","loadfile","dofile","getfenv","setfenv","getmetatable","setmetatable","collectgarbage","newproxy","_G","print","coroutine","string","table","pcall","xpcall"}
for _, name in ipairs(names) do assert(s[name]==nil) end
assert(io==nil and os==nil and debug==nil and package==nil and require==nil and load==nil and dofile==nil and getfenv==nil and setfenv==nil and getmetatable==nil and setmetatable==nil and collectgarbage==nil and _G==nil and print==nil and coroutine==nil and string==nil and table==nil and pcall==nil and xpcall==nil)
assert(math.random==nil and math.randomseed==nil)
local total=0; for _, value in pairs(s) do total=total+value end
return {total=total, maximum=math.max(2,3), floor=math.floor(1.8)} end}`)
	out, err := r.Evaluate(map[string]any{"a": 2, "b": 4})
	if err != nil || out["total"] != float64(6) || out["maximum"] != float64(3) || out["floor"] != float64(1) {
		t.Fatal(out, err)
	}
}

func TestInvalidAndOversizedOutputsQuarantineCandidate(t *testing.T) {
	for name, body := range map[string]string{
		"scalar": "return 7", "function": "return {bad=function() end}",
		"nan": "return {bad=0/0}", "infinite": "return {bad=1/0}",
		"cycle": "local t={}; t.bad=t; return t", "mixed": "return {bad={1,key=2}}",
		"sparse": "return {bad={[2]=1}}", "table_key": "return { [{}]=1 }",
		"array_root": "return {1,2}", "string_bytes": `local x="1234567890"; for i=1,12 do x=x..x end; return {bad=x}`,
		"nodes": "local t={}; for i=1,2100 do t[i]=i end; return {bad=t}",
		"depth": "local t={}; local p=t; for i=1,20 do p.child={}; p=p.child end; return t",
	} {
		t.Run(name, func(t *testing.T) {
			r := New()
			first := load(t, r, goodPolicy)
			r.Evaluate(nil)
			bad := load(t, r, "return {decide=function(s) "+body+" end}")
			out, err := r.Evaluate(nil)
			if err != nil || out["action"] != "hold" || r.Status().Revision != first || r.Status().QuarantinedRevision != bad {
				t.Fatal(out, err, r.Status())
			}
		})
	}
}

func TestInvalidInputDoesNotQuarantinePolicy(t *testing.T) {
	cycle := map[string]any{}
	cycle["cycle"] = cycle
	for name, input := range map[string]map[string]any{
		"nan": {"x": math.NaN()}, "infinite": {"x": math.Inf(1)},
		"guid_precision": {"x": uint64(17379390969991956137)}, "cycle": cycle,
		"bytes": {"x": strings.Repeat("a", MaxJSONBytes)}, "invalid_utf8": {"x": string([]byte{255})},
		"function":   {"x": func() {}},
		"null_array": {"x": []any{1, nil, 3}},
	} {
		t.Run(name, func(t *testing.T) {
			r := New()
			first := load(t, r, goodPolicy)
			if _, err := r.Evaluate(input); err == nil || r.Status().Revision != first || r.Status().Failures != 0 {
				t.Fatal(err, r.Status())
			}
		})
	}
}

func TestFailedFallbackLeavesNoPolicy(t *testing.T) {
	r := New()
	load(t, r, `return {decide=function(s) assert(s.healthy); return {} end}`)
	r.Evaluate(map[string]any{"healthy": true})
	load(t, r, `return {decide=function(s) error("new failure") end}`)
	if _, err := r.Evaluate(map[string]any{"healthy": false}); err == nil || r.Status().Revision != "" || r.Status().Failures != 2 {
		t.Fatal(err, r.Status())
	}
	if _, err := r.Evaluate(nil); !errors.Is(err, ErrNoPolicy) {
		t.Fatal(err)
	}
}

func TestConcurrentEvaluateStatusAndReloadCAS(t *testing.T) {
	r := New()
	first := load(t, r, goodPolicy)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := r.Evaluate(map[string]any{"health": 50}); err != nil {
					t.Error(err)
				}
				r.Status()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			if _, err := r.Reload(goodPolicy, first); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	if r.Status().Evaluations != 160 {
		t.Fatal(r.Status())
	}
}

func BenchmarkEvaluate(b *testing.B) {
	r := New()
	if _, err := r.Reload(`return {decide=function(s) if s.health<50 then return {action="heal", target=s.self} end; return {action="hold"} end}`, ""); err != nil {
		b.Fatal(err)
	}
	input := map[string]any{"health": 37, "self": "1"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Evaluate(input); err != nil {
			b.Fatal(err)
		}
	}
}
