// Package behavior runs pure Lua decision policies. It grants no game, file,
// network or process capabilities. VM deadlines are cooperative; this is not a
// hard memory sandbox or a substitute for validating decisions in the caller.
package behavior

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
)

const (
	MaxSourceBytes  = 128 * 1024
	MaxJSONBytes    = 32 * 1024
	MaxDepth        = 16
	MaxNodes        = 2048
	MaxInputNodes   = 4096
	EvaluationLimit = 5 * time.Millisecond
)

var (
	ErrRevisionConflict = errors.New("behavior revision conflict")
	ErrNoPolicy         = errors.New("no usable behavior policy")
)

// Status describes the active policy separately from the last failed candidate.
// A successful fallback is returned normally by Evaluate; Failures and
// QuarantinedRevision still expose the failed candidate to its caller.
type Status struct {
	Revision            string
	LastGoodRevision    string
	QuarantinedRevision string
	LastError           string
	Evaluations         uint64
	Failures            uint64
}

type policy struct {
	revision string
	proto    *lua.FunctionProto
}

// Runtime serializes reloads/evaluations. Its zero value is ready for use.
// Each call creates a fresh VM: Lua globals, closures and input mutations cannot
// survive an evaluation or affect the caller's input.
type Runtime struct {
	mu     sync.Mutex
	active *policy
	good   *policy
	status Status
}

func New() *Runtime { return &Runtime{} }

func (r *Runtime) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Reload compare-and-swaps the active revision. The initial expected revision
// is empty. It validates the chunk in a fresh VM, requiring a returned table
// containing decide=function(state). Decide is evaluated only on real input;
// an error there quarantines the candidate and restores the last working policy.
func (r *Runtime) Reload(source, expectedRevision string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if expectedRevision != r.status.Revision {
		return r.status.Revision, ErrRevisionConflict
	}
	if len(source) == 0 || len(source) > MaxSourceBytes {
		return r.status.Revision, fmt.Errorf("policy source must contain 1..%d bytes", MaxSourceBytes)
	}
	revision := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
	if revision == r.status.QuarantinedRevision {
		return r.status.Revision, errors.New("policy revision is quarantined; change the source before retrying")
	}
	L := newVM()
	defer L.Close()
	fn, err := L.Load(strings.NewReader(source), "policy")
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), EvaluationLimit)
		defer cancel()
		L.SetContext(ctx)
		_, err = decisionFunction(L, fn)
		if err == nil {
			err = ctx.Err()
		}
	}
	if err != nil {
		r.failed(revision, err)
		return r.status.Revision, fmt.Errorf("invalid behavior policy: %w", err)
	}
	r.active = &policy{revision: revision, proto: fn.Proto}
	r.status.Revision = revision
	return revision, nil
}

// Evaluate returns a detached JSON object. Input validation failures leave the
// policy intact. Policy errors restore and evaluate the last working policy
// against the original input, never the failed VM's possibly mutated table.
func (r *Runtime) Evaluate(input map[string]any) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.Evaluations++
	if err := validateInput(input); err != nil {
		return nil, fmt.Errorf("invalid behavior input: %w", err)
	}
	if r.active == nil {
		return nil, ErrNoPolicy
	}
	output, err := evaluate(r.active, input)
	if err == nil {
		r.good = r.active
		r.status.LastGoodRevision = r.good.revision
		return output, nil
	}
	r.failed(r.active.revision, err)
	if r.good == r.active {
		r.good = nil
		r.status.LastGoodRevision = ""
	}
	r.active = r.good
	r.status.Revision = ""
	if r.active == nil {
		return nil, fmt.Errorf("behavior policy quarantined: %w", err)
	}
	r.status.Revision = r.active.revision
	output, fallbackErr := evaluate(r.active, input)
	if fallbackErr != nil {
		r.failed(r.active.revision, fallbackErr)
		r.active, r.good = nil, nil
		r.status.Revision, r.status.LastGoodRevision = "", ""
		return nil, fmt.Errorf("behavior fallback also failed: %w", fallbackErr)
	}
	return output, nil
}

func (r *Runtime) failed(revision string, err error) {
	r.status.Failures++
	r.status.QuarantinedRevision = revision
	r.status.LastError = err.Error()
	if len(r.status.LastError) > 512 {
		r.status.LastError = r.status.LastError[:512]
	}
}

func evaluate(p *policy, input map[string]any) (map[string]any, error) {
	L := newVM()
	defer L.Close()
	ctx, cancel := context.WithTimeout(context.Background(), EvaluationLimit)
	defer cancel()
	L.SetContext(ctx)
	fn, err := decisionFunction(L, L.NewFunctionFromProto(p.proto))
	if err != nil {
		return nil, err
	}
	state, err := toLua(L, input, &budget{nodeLimit: MaxInputNodes})
	if err != nil {
		return nil, err
	}
	if err = L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, state); err != nil {
		return nil, err
	}
	value, err := fromLua(L.Get(-1), &budget{ctx: ctx}, map[*lua.LTable]bool{}, 0)
	if err != nil {
		return nil, err
	}
	output, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("decide must return a JSON object table")
	}
	if err := jsonSize(output); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return output, nil
}

func decisionFunction(L *lua.LState, fn *lua.LFunction) (*lua.LFunction, error) {
	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
		return nil, err
	}
	t, ok := L.Get(-1).(*lua.LTable)
	if !ok {
		return nil, errors.New("policy chunk must return a table")
	}
	decide, ok := t.RawGetString("decide").(*lua.LFunction)
	if !ok || decide.IsG {
		return nil, errors.New("policy table must contain decide=function(state)")
	}
	L.Pop(1)
	return decide, nil
}

func newVM() *lua.LState {
	L := lua.NewState(lua.Options{SkipOpenLibs: true, CallStackSize: 128,
		RegistrySize: 1024, RegistryMaxSize: 4096, RegistryGrowStep: 256, MinimizeStackMemory: true})
	lua.OpenBase(L)
	L.Pop(1)
	keep := map[string]bool{"assert": true, "error": true, "ipairs": true,
		"next": true, "pairs": true, "select": true, "tonumber": true, "tostring": true, "type": true}
	global := L.Get(lua.GlobalsIndex).(*lua.LTable)
	var remove []lua.LValue
	global.ForEach(func(k, _ lua.LValue) {
		if !keep[k.String()] {
			remove = append(remove, k)
		}
	})
	for _, k := range remove {
		global.RawSet(k, lua.LNil)
	}
	// Only constant-time numeric helpers; no string/pattern, sort, random,
	// coroutine, protected-call, loading, environment or metatable APIs.
	lua.OpenMath(L)
	L.Pop(1)
	mathTable := L.GetGlobal("math").(*lua.LTable)
	keepMath := map[string]bool{"abs": true, "ceil": true, "floor": true, "min": true, "max": true, "sqrt": true}
	remove = nil
	mathTable.ForEach(func(k, _ lua.LValue) {
		if !keepMath[k.String()] {
			remove = append(remove, k)
		}
	})
	for _, k := range remove {
		mathTable.RawSet(k, lua.LNil)
	}
	return L
}
