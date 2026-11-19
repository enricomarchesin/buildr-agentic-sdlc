// Package logx writes JSON-lines events to standard output.
//
// Logging never affects control: events are handed to a background writer
// through a bounded queue. If the queue is full or the output cannot be
// written, events are dropped and the caller never blocks.
package logx

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Logger writes one JSON object per line.
type Logger struct {
	queue chan []byte
	done  chan struct{}
	once  sync.Once
	now   func() time.Time

	syncW  io.Writer // when set, events are written directly (tests)
	syncMu sync.Mutex
}

// NewSync returns a logger that writes synchronously to w. It is meant for
// tests; production code uses New.
func NewSync(w io.Writer, now func() time.Time) *Logger {
	return &Logger{syncW: w, now: now}
}

// New starts a logger writing to w.
func New(w io.Writer) *Logger {
	l := &Logger{
		queue: make(chan []byte, 4096),
		done:  make(chan struct{}),
		now:   time.Now,
	}
	go l.run(w)
	return l
}

func (l *Logger) run(w io.Writer) {
	defer close(l.done)
	for line := range l.queue {
		// Errors are ignored on purpose: a closed or broken stdout must not
		// change anything else.
		_, _ = w.Write(line)
	}
}

func (l *Logger) enqueue(line []byte) {
	if l == nil {
		return
	}
	if l.syncW != nil {
		l.syncMu.Lock()
		_, _ = l.syncW.Write(line)
		l.syncMu.Unlock()
		return
	}
	defer func() { _ = recover() }() // queue closed during shutdown
	select {
	case l.queue <- line:
	default: // queue full: drop rather than block control
	}
}

// Event logs an event with the given key/value pairs. Keys are written in
// order after "event" and "ts".
func (l *Logger) Event(name string, kv ...any) {
	if l == nil {
		return
	}
	var b bytes.Buffer
	b.WriteString(`{"event":`)
	writeJSON(&b, name)
	b.WriteString(`,"ts":`)
	writeJSON(&b, l.now().UTC().Format(time.RFC3339Nano))
	for i := 0; i+1 < len(kv); i += 2 {
		k, _ := kv[i].(string)
		b.WriteByte(',')
		writeJSON(&b, k)
		b.WriteByte(':')
		writeJSON(&b, kv[i+1])
	}
	b.WriteString("}\n")
	l.enqueue(b.Bytes())
}

// Raw logs v, which must marshal to a JSON object with an "event" field.
func (l *Logger) Raw(v any) {
	if l == nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		l.Event("log_error", "error", err.Error())
		return
	}
	l.enqueue(append(data, '\n'))
}

// Close flushes queued events, waiting at most timeout.
func (l *Logger) Close(timeout time.Duration) {
	if l == nil || l.syncW != nil {
		return
	}
	l.once.Do(func() { close(l.queue) })
	select {
	case <-l.done:
	case <-time.After(timeout):
	}
}

func writeJSON(b *bytes.Buffer, v any) {
	if err, ok := v.(error); ok {
		v = err.Error()
	}
	if d, ok := v.(time.Duration); ok {
		v = d.String()
	}
	data, err := json.Marshal(v)
	if err != nil {
		data, _ = json.Marshal(err.Error())
	}
	b.Write(data)
}
