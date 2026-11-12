package dungeontrace

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "trace")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func waitRecorder(t *testing.T, r *Recorder) {
	t.Helper()
	select {
	case <-r.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("trace writer did not finish")
	}
}

func readRows(t *testing.T, dir string) []Row {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, Filename))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Mode().Perm() != 0o600 {
		t.Fatalf("trace was not private: %v %v", stat, err)
	}
	var rows []Row
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var row Row
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestTraceRequiresOutsideEntryAndStopsAtFirstExit(t *testing.T) {
	dir := privateDir(t)
	r, err := New(dir, "observer-session", 36)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.ObserveMap(36, true, now)
	r.Record("cast", now, map[string]int{"spell": 9})
	if r.Active() {
		t.Fatal("starting inside incorrectly armed the trace")
	}
	if _, err := os.Stat(filepath.Join(dir, Filename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("armed recorder created a file before entry: %v", err)
	}

	r.ObserveMap(0, true, now)
	r.ObserveMap(36, true, now.Add(time.Second))
	if !r.Active() {
		t.Fatal("observed outside-to-dungeon transition did not start")
	}
	payload := map[string]int{"spell": 42}
	r.Record("cast", now.Add(2*time.Second), payload)
	payload["spell"] = 7
	r.ObserveMap(0, false, now.Add(3*time.Second))
	if r.Active() {
		t.Fatal("unknown map allowed payload capture")
	}
	r.Record("cast", now.Add(4*time.Second), payload)
	r.ObserveMap(36, true, now.Add(5*time.Second))
	r.Record("policy", now.Add(6*time.Second), map[string]bool{"visible": false})
	r.ObserveMap(1, true, now.Add(7*time.Second))
	r.ObserveMap(36, true, now.Add(8*time.Second))
	r.Record("cast", now.Add(9*time.Second), payload)
	waitRecorder(t, r)

	rows := readRows(t, dir)
	var kinds []string
	for i, row := range rows {
		kinds = append(kinds, row.Kind)
		if row.Sequence != uint64(i+1) || row.SessionID != "observer-session" || row.MapID != 36 || row.Version != 1 {
			t.Fatalf("unbound row: %+v", row)
		}
	}
	if want := []string{"start", "cast", "visibility", "visibility", "policy", "end"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("record kinds = %v, want %v", kinds, want)
	}
	if string(rows[1].Payload) != `{"spell":42}` || rows[1].ElapsedNS != int64(time.Second) ||
		!rows[1].Timestamp.Equal(now.Add(2*time.Second)) || rows[1].Timestamp.Location() != time.UTC {
		t.Fatalf("record was not frozen on the observer clock: %+v", rows[1])
	}
	if !strings.Contains(string(rows[2].Payload), "observer_map_unknown") ||
		!strings.Contains(string(rows[3].Payload), "observer_map_known") ||
		!strings.Contains(string(rows[5].Payload), "observed_map_exit") {
		t.Fatal("trace omitted a map visibility or exit reason")
	}
	status := r.Status()
	if status.Phase != complete || status.Records != uint64(len(rows)) || status.DurableRecords != status.Records || status.Dropped != 0 {
		t.Fatalf("unexpected completed status: %+v", status)
	}
}

func TestTraceRefusesPublicDirectoryAndExistingFile(t *testing.T) {
	public := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if r, err := New(public, "session", 36); err == nil {
		r.Close(time.Now(), "test")
		t.Fatal("a public directory was accepted")
	}

	dir := privateDir(t)
	r, err := New(dir, "session", 36)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, Filename)
	sentinel := []byte("preserved previous evidence")
	if err := os.WriteFile(file, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.ObserveMap(0, true, now)
	r.ObserveMap(36, true, now)
	waitRecorder(t, r)
	actual, err := os.ReadFile(file)
	if err != nil || !reflect.DeepEqual(actual, sentinel) || r.Status().Phase != failed {
		t.Fatalf("existing evidence was touched or collision unreported: %q %v %+v", actual, err, r.Status())
	}
}

func TestTraceReportsRejectedPayloadsBeforeTerminalRecord(t *testing.T) {
	dir := privateDir(t)
	r, err := New(dir, "session", 36)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.ObserveMap(0, true, now)
	r.ObserveMap(36, true, now)
	r.Record("policy", now, map[string]string{"oversize": strings.Repeat("a", maxPayloadBytes)})
	r.Record("cast", now, make(chan int))
	r.Close(now, "observer_logout")
	waitRecorder(t, r)
	rows := readRows(t, dir)
	if len(rows) != 3 || rows[1].Kind != "gap" || rows[2].Kind != "end" || r.Status().Dropped != 2 {
		t.Fatalf("missing terminal gap: %+v", rows)
	}
	var detail gap
	if err := json.Unmarshal(rows[1].Payload, &detail); err != nil ||
		detail.Counts["record_too_large"] != 1 || detail.Counts["record_encoding_failed"] != 1 {
		t.Fatalf("unexpected gap payload %s: %v", rows[1].Payload, err)
	}
}
