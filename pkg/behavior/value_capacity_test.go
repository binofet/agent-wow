package behavior

import (
	"context"
	"errors"
	lua "github.com/yuin/gopher-lua"
	"reflect"
	"testing"
)

func smallObservedRows() map[string]any {
	rows := make([]any, 64)
	for i := range rows {
		rows[i] = map[string]any{"guid": "18446744073709551615", "x": float64(i), "known": true}
	}
	return map[string]any{"observations": rows}
}

func TestTableCapacityHintsKeepCompleteValuesAndAdmissionBounds(t *testing.T) {
	L := newVM()
	defer L.Close()
	input := smallObservedRows()
	value, err := toLua(L, input, &budget{nodeLimit: MaxInputNodes})
	if err != nil {
		t.Fatal(err)
	}
	output, err := fromLua(value, &budget{nodeLimit: MaxInputNodes}, map[*lua.LTable]bool{}, 0)
	if err != nil || !reflect.DeepEqual(input, output) {
		t.Fatal("collection was capped or changed", err)
	}
	if _, err = toLua(L, input, &budget{nodeLimit: 10}); err == nil {
		t.Fatal("capacity hint bypassed node budget")
	}
	if _, err = toLua(L, input, &budget{byteLimit: 10}); err == nil {
		t.Fatal("capacity hint bypassed byte budget")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = toLua(L, input, &budget{ctx: ctx}); !errors.Is(err, context.Canceled) {
		t.Fatal("capacity hint bypassed cancellation", err)
	}
}

func BenchmarkToLuaSmallObservedRows(b *testing.B) {
	L := newVM()
	defer L.Close()
	input := smallObservedRows()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := toLua(L, input, &budget{nodeLimit: MaxInputNodes}); err != nil {
			b.Fatal(err)
		}
	}
}
