// Package logx writes heatctl's structured JSON log lines.
//
// Every line is one JSON object with at least an "event" field (L4). Control
// cycles additionally carry the fields required by L1. Logging is
// asynchronous and best-effort: a slow or broken standard output never blocks
// or influences control (L3).
package logx

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Fields holds the additional fields of one event.
type Fields map[string]any

// Logger writes structured events.
type Logger interface {
	// Event writes one event. It never blocks for long and never returns an
	// error.
	Event(name string, fields Fields)
}

// Nop discards every event.
type Nop struct{}

// Event implements Logger.
func (Nop) Event(string, Fields) {}

// JSONLogger writes one JSON object per line to an io.Writer. Events are
// queued to a bounded channel and written by a background goroutine so that a
// stalled reader cannot stall the control loop. When the queue is full events
// are dropped rather than blocking.
type JSONLogger struct {
	w       io.Writer
	ch      chan []byte
	done    chan struct{}
	closeMu sync.Mutex
	closed  bool

	dropped atomic.Uint64

	// Now returns the timestamp used in events. It may be replaced in tests.
	Now func() time.Time
}

// QueueSize is the number of events buffered before events are dropped.
const QueueSize = 1024

// New returns a logger writing to w. A nil w discards everything.
func New(w io.Writer) *JSONLogger {
	if w == nil {
		w = io.Discard
	}
	l := &JSONLogger{
		w:    w,
		ch:   make(chan []byte, QueueSize),
		done: make(chan struct{}),
		Now:  time.Now,
	}
	go l.run()
	return l
}

// Event implements Logger.
func (l *JSONLogger) Event(name string, fields Fields) {
	if l == nil {
		return
	}
	line := l.encode(name, fields)
	if line == nil {
		return
	}
	l.closeMu.Lock()
	if l.closed {
		l.closeMu.Unlock()
		return
	}
	select {
	case l.ch <- line:
	default:
		// The reader is not keeping up. Dropping a log line must not delay
		// control.
		l.dropped.Add(1)
	}
	l.closeMu.Unlock()
}

func (l *JSONLogger) encode(name string, fields Fields) []byte {
	obj := make(map[string]any, len(fields)+3)
	for k, v := range fields {
		obj[k] = v
	}
	obj["event"] = name
	if _, ok := obj["schema"]; !ok {
		obj["schema"] = 1
	}
	if _, ok := obj["ts"]; !ok {
		now := time.Now
		if l.Now != nil {
			now = l.Now
		}
		obj["ts"] = now().UTC().Format(time.RFC3339)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return nil
	}
	return buf.Bytes()
}

func (l *JSONLogger) run() {
	defer close(l.done)
	for line := range l.ch {
		// A write error, including a closed standard output, is ignored: the
		// controller keeps running unchanged (L3, F8.AC2).
		_, _ = l.w.Write(line)
	}
}

// Close stops accepting events, flushes what is queued and waits briefly for
// the writer to finish. It returns the number of events dropped because the
// queue was full.
func (l *JSONLogger) Close() uint64 {
	if l == nil {
		return 0
	}
	l.closeMu.Lock()
	if !l.closed {
		l.closed = true
		close(l.ch)
	}
	l.closeMu.Unlock()
	select {
	case <-l.done:
	case <-time.After(2 * time.Second):
	}
	return l.dropped.Load()
}
