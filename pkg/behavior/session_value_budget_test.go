package behavior

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestPersistentWorldObservationAndCheckpointExceedStatelessLimits(t *testing.T) {
	source := `local state={} return {on_event=function(e)state=e.value;return true end,
 decide=function(frame)return {count=state.count} end,
 export_state=function()return {version=1,data=state} end,
 import_state=function(saved)state=saved.data;return true end}`
	s, err := NewSession(context.Background(), "large-world", source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows := map[string]any{}
	for i := 0; i < 5000; i++ {
		rows[fmt.Sprint(i)] = map[string]any{"health": 100, "name": "observed"}
	}
	_, err = s.Enqueue(Event{Kind: "world_observation", Value: map[string]any{"count": 5000, "units": rows, "text": strings.Repeat("x", 128*1024)}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Decide(context.Background(), nil)
	if err != nil || d.Output["count"] != float64(5000) {
		t.Fatalf("large real-world view could not run: %+v %v", d.Output, err)
	}
	if _, err := s.Reload(context.Background(), source+"\n-- same body", s.Status().Revision); err != nil {
		t.Fatal(err)
	}
	d, err = s.Decide(context.Background(), nil)
	if err != nil || d.Output["count"] != float64(5000) {
		t.Fatalf("large checkpoint did not survive hot reload: %+v %v", d.Output, err)
	}
	small, err := NewSessionWithOptions(context.Background(), "explicit-budget", source, SessionOptions{ValueBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	if _, err := small.Enqueue(Event{Kind: "world_observation", Value: map[string]any{"text": strings.Repeat("x", 8192)}}); err == nil {
		t.Fatal("configured resource budget ignored")
	}
}
