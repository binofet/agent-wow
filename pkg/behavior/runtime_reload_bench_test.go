package behavior

import (
	"github.com/agent-wow/agent-wow/behaviors"
	"testing"
	"time"
)

// Match the profiled native semantic fixture's explicit budget. This measures
// repeated compilation cost, not acceptance under the separate default 5ms CLI.
func BenchmarkReloadActualStarterSource(b *testing.B) {
	r, err := NewWithOptions(SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		b.Fatal(err)
	}
	source := behaviors.StarterSpellSource
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	revision := ""
	for range b.N {
		revision, err = r.Reload(source, revision)
		if err != nil {
			b.Fatal(err)
		}
	}
}
