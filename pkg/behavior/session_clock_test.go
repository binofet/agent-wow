package behavior

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionClockDrivesLuaExpiryAcrossReload(t *testing.T) {
	const source = `local state={}
return {on_event=function(e)return true end,
 decide=function(frame)
   state.deadline=state.deadline or frame.clock.monotonicMs+500
   return {expired=frame.clock.monotonicMs>=state.deadline,deadline=state.deadline,
           clock=frame.clock,session=frame.session_id}
 end,
 export_state=function()return {version=1,data=state} end,
 import_state=function(checkpoint)state=checkpoint.data;return true end}`
	s := newTestSessionWithPolicy(t, source)
	start, elapsed := time.Now(), 100*time.Millisecond
	s.clockStart = start
	s.clockNow = func() time.Time { return start.Add(elapsed) }
	first := sessionDecide(t, s)
	clock := first.Output["clock"].(map[string]any)
	if first.Output["expired"] != false || first.Output["deadline"] != float64(600) ||
		clock["monotonicMs"] != float64(100) || clock["unixMs"] != float64(start.Add(elapsed).UnixMilli()) {
		t.Fatal("Lua did not receive the host clock", first.Output)
	}
	if _, err := s.Reload(context.Background(), source+"\n-- replacement VM", s.Status().Revision); err != nil {
		t.Fatal(err)
	}
	elapsed = 599 * time.Millisecond
	if out := sessionDecide(t, s).Output; out["expired"] != false || out["deadline"] != float64(600) {
		t.Fatal("reload reset deadline or clock origin", out)
	}
	elapsed = 600 * time.Millisecond
	if out := sessionDecide(t, s).Output; out["expired"] != true || out["session"] != "offline-session" {
		t.Fatal("Lua could not finish at its chosen deadline", out)
	}
	reading, err := s.Clock()
	if err != nil || reading.MonotonicMs != 600 || reading.UnixMs != start.Add(elapsed).UnixMilli() {
		t.Fatalf("host read differs from persistent frame: %+v %v", reading, err)
	}
}

func TestSessionClockIsHostOwnedAndStopsWithSession(t *testing.T) {
	s := newTestSessionWithPolicy(t, `return {on_event=function(e)return true end,
 decide=function(frame)return {clock=frame.clock} end,
 export_state=function()return {version=1,data={}} end,import_state=function(s)return true end}`)
	out, err := s.Decide(context.Background(), map[string]any{"clock": map[string]any{"monotonicMs": float64(-1)}})
	if err != nil {
		t.Fatal(err)
	}
	clock := out.Output["clock"].(map[string]any)
	if clock["monotonicMs"].(float64) < 0 || clock["unixMs"].(float64) <= 0 {
		t.Fatal("input replaced the host clock", out.Output)
	}
	s.Close()
	if _, err := s.Clock(); !errors.Is(err, ErrSessionClosed) {
		t.Fatal("closed session still supplied clock readings", err)
	}
}
