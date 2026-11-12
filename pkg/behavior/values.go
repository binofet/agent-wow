package behavior

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"

	lua "github.com/yuin/gopher-lua"
)

// Lua numbers are float64. Require larger integral identities (including GUIDs)
// as strings instead of silently rounding them across the policy boundary.
const maxExactInteger = 1<<53 - 1

type budget struct {
	nodes, bytes, depth int
	nodeLimit           int
	ctx                 context.Context
}

func (b *budget) take(s string) error {
	b.nodes++
	b.bytes += len(s)
	limit := b.nodeLimit
	if limit == 0 {
		limit = MaxNodes
	}
	if b.nodes > limit || b.bytes > MaxJSONBytes || b.depth > MaxDepth {
		return errors.New("policy value exceeds node, byte or depth bounds")
	}
	if !utf8.ValidString(s) {
		return errors.New("policy strings must be valid UTF-8")
	}
	if b.ctx != nil {
		return b.ctx.Err()
	}
	return nil
}

func jsonSize(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > MaxJSONBytes {
		return errors.New("policy JSON exceeds byte bound")
	}
	return nil
}

func validateInput(input map[string]any) error {
	// Conversion with no VM validates the entire input before running Lua.
	if _, err := toLua(nil, input, &budget{nodeLimit: MaxInputNodes}); err != nil {
		return err
	}
	return jsonSize(input)
}

func toLua(L *lua.LState, value any, b *budget) (lua.LValue, error) {
	if err := b.take(""); err != nil {
		return nil, err
	}
	number := func(n float64) (lua.LValue, error) {
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > maxExactInteger {
			return nil, errors.New("policy number is nonfinite or outside exact JSON integer range; use a string")
		}
		return lua.LNumber(n), nil
	}
	var table *lua.LTable
	switch v := value.(type) {
	case nil:
		return lua.LNil, nil
	case bool:
		return lua.LBool(v), nil
	case string:
		if err := b.take(v); err != nil {
			return nil, err
		}
		return lua.LString(v), nil
	case float64:
		return number(v)
	case float32:
		return number(float64(v))
	case int:
		return number(float64(v))
	case int64:
		return number(float64(v))
	case int32:
		return number(float64(v))
	case uint:
		return number(float64(v))
	case uint64:
		return number(float64(v))
	case uint32:
		return number(float64(v))
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			return nil, err
		}
		return number(n)
	case map[string]any:
		if L != nil {
			table = L.NewTable()
		}
		b.depth++
		defer func() { b.depth-- }()
		for k, child := range v {
			if err := b.take(k); err != nil {
				return nil, err
			}
			converted, err := toLua(L, child, b)
			if err != nil {
				return nil, err
			}
			if table != nil {
				table.RawSetString(k, converted)
			}
		}
	case []any:
		if L != nil {
			table = L.NewTable()
		}
		b.depth++
		defer func() { b.depth-- }()
		for i, child := range v {
			if child == nil {
				return nil, errors.New("null array elements cannot be represented faithfully in Lua")
			}
			converted, err := toLua(L, child, b)
			if err != nil {
				return nil, err
			}
			if table != nil {
				table.RawSetInt(i+1, converted)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported policy input type %T", value)
	}
	return table, nil
}

func fromLua(value lua.LValue, b *budget, path map[*lua.LTable]bool, depth int) (any, error) {
	b.depth = depth
	if err := b.take(""); err != nil {
		return nil, err
	}
	switch v := value.(type) {
	case *lua.LNilType:
		return nil, nil
	case lua.LBool:
		return bool(v), nil
	case lua.LString:
		if err := b.take(string(v)); err != nil {
			return nil, err
		}
		return string(v), nil
	case lua.LNumber:
		n := float64(v)
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > maxExactInteger {
			return nil, errors.New("policy output number is nonfinite or outside exact integer range")
		}
		return n, nil
	case *lua.LTable:
		if path[v] {
			return nil, errors.New("cyclic policy output")
		}
		path[v] = true
		defer delete(path, v)
		object, array := map[string]any{}, map[int]any{}
		key := lua.LValue(lua.LNil)
		for {
			k, child := v.Next(key)
			if k == lua.LNil {
				break
			}
			key = k
			converted, err := fromLua(child, b, path, depth+1)
			if err != nil {
				return nil, err
			}
			switch k := k.(type) {
			case lua.LString:
				if err := b.take(string(k)); err != nil {
					return nil, err
				}
				object[string(k)] = converted
			case lua.LNumber:
				n := float64(k)
				if math.IsNaN(n) || n < 1 || n > MaxNodes || math.Trunc(n) != n {
					return nil, errors.New("policy array keys must be bounded positive integers")
				}
				array[int(n)] = converted
			default:
				return nil, errors.New("policy table keys must be strings or contiguous array indices")
			}
		}
		if len(array) == 0 {
			return object, nil
		}
		if len(object) != 0 {
			return nil, errors.New("mixed policy object/array table")
		}
		out := make([]any, len(array))
		for i := range out {
			var ok bool
			if out[i], ok = array[i+1]; !ok {
				return nil, errors.New("sparse policy array")
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported policy output type %s", value.Type())
	}
}
