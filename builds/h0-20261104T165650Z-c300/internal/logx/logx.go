// Package logx writes JSON-lines events to standard output without ever
// blocking or failing the caller (L3).
package logx

import (
	"bytes"
	"encoding/json"
	"io"
	"time"
)

type Logger struct {
	ch  chan []byte
	now func() time.Time
}

// New starts a writer goroutine on w. Events are dropped when it falls behind.
func New(w io.Writer) *Logger {
	l := &Logger{ch: make(chan []byte, 1024), now: time.Now}
	go func() {
		for b := range l.ch {
			_, _ = w.Write(b) // errors deliberately ignored
		}
	}()
	return l
}

func (l *Logger) send(b []byte) {
	select {
	case l.ch <- b:
	default:
	}
}

// Emit writes v as one JSON line.
func (l *Logger) Emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	l.send(append(b, '\n'))
}

// Log writes {"event":..., "ts":..., key:value...} with fields in the given order.
func (l *Logger) Log(event string, kv ...any) {
	var buf bytes.Buffer
	buf.WriteString(`{"event":`)
	e, _ := json.Marshal(event)
	buf.Write(e)
	buf.WriteString(`,"ts":"` + l.now().UTC().Format(time.RFC3339) + `"`)
	for i := 0; i+1 < len(kv); i += 2 {
		k, _ := kv[i].(string)
		v, err := json.Marshal(kv[i+1])
		if err != nil {
			continue
		}
		kb, _ := json.Marshal(k)
		buf.WriteByte(',')
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteString("}\n")
	l.send(buf.Bytes())
}
