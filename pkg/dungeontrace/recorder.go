// Package dungeontrace writes one private, normalized trace for a dungeon visit.
// It records caller-supplied observations and never subscribes to game traffic itself.
package dungeontrace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	Filename        = "dungeon-trace.jsonl"
	queueDepth      = 128
	maxPayloadBytes = 128 << 10
	syncInterval    = time.Second
)

const (
	armed     = "armed"
	recording = "recording"
	closing   = "closing"
	complete  = "complete"
	failed    = "failed"
	disarmed  = "disarmed"
)

// Row carries one sequenced observation. Timestamp is the observer's UTC wall
// time. ElapsedNS uses the same observer clock and visit start; it is monotonic
// clock based when the caller supplies times from time.Now. A source packet's
// own timestamp, if available, belongs in Payload and is not interchangeable
// with this clock. Sequence is file order, not a claim of source event order.
type Row struct {
	Version   int             `json:"version"`
	SessionID string          `json:"sessionId"`
	MapID     uint32          `json:"mapId"`
	Sequence  uint64          `json:"sequence"`
	Timestamp time.Time       `json:"observedAt"`
	ElapsedNS int64           `json:"elapsedNs"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Status distinguishes written rows from records confirmed by a sync.
type Status struct {
	Phase          string
	Records        uint64
	DurableRecords uint64
	Dropped        uint64
	Error          string
}

type gap struct {
	First  time.Time         `json:"firstObservedAt"`
	Last   time.Time         `json:"lastObservedAt"`
	Counts map[string]uint64 `json:"counts"`
}

// Recorder is safe to call concurrently. One background writer owns the file.
type Recorder struct {
	mu         sync.Mutex
	root       *os.Root
	sessionID  string
	mapID      uint32
	start      time.Time
	next       uint64
	sawOutside bool
	mapKnown   bool
	status     Status
	pendingGap gap
	queue      chan Row
	tail       []Row
	done       chan struct{}
}

// New arms an observer for the next known outside-to-dungeon transition.
// The configured directory must already exist and exclude other users. A
// directory is single-use: an existing trace is never opened or overwritten.
func New(directory, sessionID string, mapID uint32) (*Recorder, error) {
	if !filepath.IsAbs(directory) || len(sessionID) == 0 || len(sessionID) > 128 {
		return nil, errors.New("an absolute directory and a session ID are required")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect trace directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("trace directory must be a private directory, not a symlink")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open trace directory: %w", err)
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) || actual.Mode().Perm()&0o077 != 0 {
		root.Close()
		return nil, errors.New("trace directory changed during validation")
	}
	if _, err := root.Lstat(Filename); !errors.Is(err, os.ErrNotExist) {
		root.Close()
		if err == nil {
			return nil, errors.New("trace directory already contains a visit")
		}
		return nil, fmt.Errorf("inspect trace filename: %w", err)
	}
	return &Recorder{root: root, sessionID: sessionID, mapID: mapID,
		status: Status{Phase: armed}, done: make(chan struct{})}, nil
}

// ObserveMap uses the observing client's known map, never the map of a target
// inferred from other signals. Starting inside cannot silently claim a full
// visit. An unknown map pauses payload capture until membership is established.
func (r *Recorder) ObserveMap(mapID uint32, known bool, at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.status.Phase {
	case armed:
		if !known {
			return
		}
		if mapID != r.mapID {
			r.sawOutside = true
		} else if r.sawOutside {
			r.startLocked(at)
		}
	case recording:
		if known && mapID != r.mapID {
			r.closeLocked(at, "observed_map_exit")
		} else if r.mapKnown != known {
			r.mapKnown = known
			reason := "observer_map_unknown"
			if known {
				reason = "observer_map_known"
			}
			r.enqueueLocked("visibility", at, reasonPayload(reason))
		}
	}
}

func (r *Recorder) startLocked(at time.Time) {
	f, err := r.root.OpenFile(Filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		r.root.Close()
		r.root = nil
		r.status.Phase, r.status.Error = failed, err.Error()
		close(r.done)
		return
	}
	r.start = at
	r.mapKnown = true
	r.queue = make(chan Row, queueDepth)
	r.status.Phase = recording
	first := r.rowLocked("start", at, reasonPayload("observed_map_entry"))
	go r.write(r.root, f, r.queue, first)
	r.root = nil // The writer now owns it.
}

func (r *Recorder) rowLocked(kind string, at time.Time, payload json.RawMessage) Row {
	r.next++
	return Row{Version: 1, SessionID: r.sessionID, MapID: r.mapID, Sequence: r.next,
		Timestamp: at.UTC(), ElapsedNS: at.Sub(r.start).Nanoseconds(), Kind: kind, Payload: payload}
}

func reasonPayload(reason string) json.RawMessage {
	data, _ := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	return data
}

func (r *Recorder) dropLocked(at time.Time, reason string) {
	r.status.Dropped++
	if r.pendingGap.Counts == nil {
		r.pendingGap = gap{First: at.UTC(), Counts: make(map[string]uint64)}
	}
	r.pendingGap.Last = at.UTC()
	r.pendingGap.Counts[reason]++
}

func (r *Recorder) gapLocked(at time.Time) Row {
	data, _ := json.Marshal(r.pendingGap)
	r.pendingGap = gap{}
	return r.rowLocked("gap", at, data)
}

func (r *Recorder) enqueueLocked(kind string, at time.Time, payload json.RawMessage) {
	// The mutex serializes producers. Only the writer can reduce queue length.
	if r.pendingGap.Counts != nil && len(r.queue) < cap(r.queue) {
		r.queue <- r.gapLocked(at)
	}
	if len(r.queue) == cap(r.queue) {
		r.dropLocked(at, "writer_queue_full")
		return
	}
	r.queue <- r.rowLocked(kind, at, payload)
}

// Record captures a normalized value from the current, observed visit.
// Callers should construct a deliberately scoped projection rather than
// passing packet payloads or an unrestricted world snapshot.
func (r *Recorder) Record(kind string, at time.Time, payload any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.Phase != recording || !r.mapKnown {
		return
	}
	if kind == "" || len(kind) > 64 || kind == "start" || kind == "end" || kind == "gap" || kind == "visibility" {
		r.dropLocked(at, "invalid_record_kind")
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		r.dropLocked(at, "record_encoding_failed")
		return
	}
	if len(data) > maxPayloadBytes {
		r.dropLocked(at, "record_too_large")
		return
	}
	r.enqueueLocked(kind, at, data)
}

// Close ends the visit, or disarms a recorder that never entered the dungeon.
// Once closing, it cannot arm again. Callers can wait on Done during shutdown.
func (r *Recorder) Close(at time.Time, reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeLocked(at, reason)
}

func (r *Recorder) closeLocked(at time.Time, reason string) {
	switch r.status.Phase {
	case armed:
		r.root.Close()
		r.root = nil
		r.status.Phase = disarmed
		close(r.done)
	case recording:
		if reason == "" || len(reason) > 128 {
			reason = "observer_closed"
		}
		if r.pendingGap.Counts != nil {
			r.tail = append(r.tail, r.gapLocked(at))
		}
		r.tail = append(r.tail, r.rowLocked("end", at, reasonPayload(reason)))
		r.status.Phase = closing
		close(r.queue)
	}
}

// Active reports whether the recorder currently accepts data rows.
func (r *Recorder) Active() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status.Phase == recording && r.mapKnown
}

func (r *Recorder) Status() Status {
	if r == nil {
		return Status{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Done closes when the writer finishes or the recorder is disarmed.
func (r *Recorder) Done() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.done
}

func (r *Recorder) write(root *os.Root, f *os.File, queue <-chan Row, first Row) {
	err := r.writeRows(root, f, queue, first)
	err = errors.Join(err, f.Close(), root.Close())
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queue, r.tail = nil, nil
	r.status.Phase = complete
	if err != nil {
		r.status.Phase, r.status.Error = failed, err.Error()
	}
	close(r.done)
}

func (r *Recorder) writeRows(root *os.Root, f *os.File, queue <-chan Row, first Row) error {
	enc := json.NewEncoder(f)
	var written, synced uint64
	write := func(row Row) error {
		if err := enc.Encode(row); err != nil {
			return err
		}
		written++
		r.mu.Lock()
		r.status.Records = written
		r.mu.Unlock()
		return nil
	}
	markSynced := func() {
		synced = written
		r.mu.Lock()
		r.status.DurableRecords = synced
		r.mu.Unlock()
	}
	sync := func() error {
		if synced == written {
			return nil
		}
		if err := f.Sync(); err != nil {
			return err
		}
		markSynced()
		return nil
	}
	if err := write(first); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return err
	}
	markSynced()

	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()
	for {
		select {
		case row, ok := <-queue:
			if !ok {
				// The closing producer assigned tail before closing queue.
				for _, last := range r.tail {
					if err := write(last); err != nil {
						return err
					}
				}
				return sync()
			}
			if err := write(row); err != nil {
				return err
			}
		case <-ticker.C:
			if err := sync(); err != nil {
				return err
			}
		}
	}
}
