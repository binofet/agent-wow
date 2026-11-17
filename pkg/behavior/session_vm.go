package behavior

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// A host frame can contain a full policy checkpoint or input plus a bounded
// 256-row reservation ledger. Ordinary Lua inputs and outputs retain their
// original JSON and node bounds.
const maxSessionFrameBytes = 64 * 1024

// sessionVM owns a Lua state and its persistent policy closures.
type sessionVM struct {
	mu        sync.Mutex
	state     *lua.LState
	callbacks map[string]*lua.LFunction
}

func newSessionVM(parent context.Context, source string) (*sessionVM, error) {
	if len(source) == 0 || len(source) > MaxSourceBytes {
		return nil, fmt.Errorf("policy source must contain 1..%d bytes", MaxSourceBytes)
	}
	ctx, cancel := context.WithTimeout(parent, EvaluationLimit)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	state := newVM()
	vm := &sessionVM{state: state, callbacks: make(map[string]*lua.LFunction, 4)}
	if err := vm.load(ctx, source); err != nil {
		vm.close()
		return nil, fmt.Errorf("load session policy: %w", err)
	}
	return vm, nil
}

func (v *sessionVM) load(ctx context.Context, source string) error {
	state := v.state
	top := state.GetTop()
	state.SetContext(ctx)
	defer state.SetTop(top)
	defer state.RemoveContext()

	chunk, err := state.Load(strings.NewReader(source), "session policy")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	err = state.CallByParam(lua.P{Fn: chunk, NRet: 1, Protect: true})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	callbacks, ok := state.Get(-1).(*lua.LTable)
	if !ok {
		return errors.New("session policy chunk must return a table")
	}
	for _, name := range []string{"decide", "on_event", "export_state", "import_state"} {
		callback, ok := callbacks.RawGetString(name).(*lua.LFunction)
		if !ok || callback.IsG {
			return fmt.Errorf("session policy must define %s as a Lua function", name)
		}
		v.callbacks[name] = callback
	}
	return ctx.Err()
}

func (v *sessionVM) close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != nil {
		v.state.Close()
		v.state, v.callbacks = nil, nil
	}
}

func (v *sessionVM) decide(ctx context.Context, frame map[string]any) (map[string]any, error) {
	return v.object(ctx, "decide", frame)
}

func (v *sessionVM) onEvent(ctx context.Context, event map[string]any) error {
	return v.call(ctx, "on_event", requireSessionSuccess, event)
}

func (v *sessionVM) exportState(ctx context.Context) (map[string]any, error) {
	state, err := v.object(ctx, "export_state")
	if err != nil {
		return nil, err
	}
	if err := validateSessionState(state); err != nil {
		return nil, fmt.Errorf("export_state: %w", err)
	}
	return state, nil
}

func (v *sessionVM) importState(ctx context.Context, state map[string]any) error {
	envelope := map[string]any{"version": state["version"], "data": state["data"]}
	if err := validateSessionState(envelope); err != nil {
		return fmt.Errorf("import_state: %w", err)
	}
	return v.call(ctx, "import_state", requireSessionSuccess, state)
}

func validateSessionState(state map[string]any) error {
	version, err := toLua(nil, state["version"], &budget{})
	n, numeric := version.(lua.LNumber)
	data, object := state["data"].(map[string]any)
	if err != nil || len(state) != 2 || !numeric || n <= 0 || math.Trunc(float64(n)) != float64(n) || !object || data == nil {
		return errors.New("policy state must contain exactly a positive integer version and a data object")
	}
	return nil
}

func requireSessionSuccess(_ context.Context, value lua.LValue) error {
	if value != lua.LTrue {
		return errors.New("callback must return true")
	}
	return nil
}

func (v *sessionVM) object(ctx context.Context, name string, args ...map[string]any) (map[string]any, error) {
	var result map[string]any
	err := v.call(ctx, name, func(ctx context.Context, value lua.LValue) error {
		detached, err := fromLua(value, &budget{ctx: ctx}, make(map[*lua.LTable]bool), 0)
		if err != nil {
			return err
		}
		var ok bool
		result, ok = detached.(map[string]any)
		if !ok {
			return errors.New("callback must return an object")
		}
		return jsonSize(result)
	}, args...)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (v *sessionVM) call(parent context.Context, name string, check func(context.Context, lua.LValue) error, args ...map[string]any) error {
	ctx, cancel := context.WithTimeout(parent, EvaluationLimit)
	defer cancel()
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	state := v.state
	if state == nil {
		return errors.New("session policy VM is closed")
	}

	top := state.GetTop()
	state.SetContext(ctx)
	defer state.SetTop(top)
	defer state.RemoveContext()

	byteLimit, nodeLimit := MaxJSONBytes, MaxInputNodes
	if name == "import_state" || name == "decide" {
		byteLimit, nodeLimit = maxSessionFrameBytes, MaxInputNodes+maxPendingActions*8
	}
	inputs := make([]lua.LValue, 0, len(args))
	for _, arg := range args {
		value, err := toLua(state, arg, &budget{nodeLimit: nodeLimit, byteLimit: byteLimit, ctx: ctx})
		if err != nil {
			return fmt.Errorf("%s input: %w", name, err)
		}
		if err := boundedJSONSize(arg, byteLimit); err != nil {
			return fmt.Errorf("%s input: %w", name, err)
		}
		inputs = append(inputs, value)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := state.CallByParam(lua.P{Fn: v.callbacks[name], NRet: 1, Protect: true}, inputs...)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		err = check(ctx, state.Get(-1))
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
