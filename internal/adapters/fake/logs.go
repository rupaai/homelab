package fake

import (
	"context"
	"sync"

	"github.com/rupaai/homelab/internal/core"
)

// IngestCall is one Ingest the fake received.
type IngestCall struct {
	Batch    core.LogBatch
	MinLevel string
}

// Logs is a fake log store: it keeps what it is given, so tests can look at it.
type Logs struct {
	mu sync.Mutex
	// Ingested and Events are the calls received, in order.
	Ingested []IngestCall
	Events   []core.LogEvent
	// Err is what every call fails with (nil: they work).
	Err error
	// EventSeen, when not nil, gets a value after each Event (for tests that wait).
	EventSeen chan struct{}
}

func NewLogs() *Logs { return &Logs{} }

// Set makes calls fail (or work again, with nil).
func (l *Logs) Set(err error) {
	l.mu.Lock()
	l.Err = err
	l.mu.Unlock()
}

func (l *Logs) Ingest(_ context.Context, b core.LogBatch, minLevel string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Err != nil {
		return l.Err
	}
	b.Lines = append([]core.LogLine(nil), b.Lines...)
	l.Ingested = append(l.Ingested, IngestCall{Batch: b, MinLevel: minLevel})
	return nil
}

func (l *Logs) Event(_ context.Context, e core.LogEvent) error {
	l.mu.Lock()
	if l.Err != nil {
		err := l.Err
		l.mu.Unlock()
		return err
	}
	l.Events = append(l.Events, e)
	seen := l.EventSeen
	l.mu.Unlock()
	if seen != nil {
		select {
		case seen <- struct{}{}:
		default:
		}
	}
	return nil
}

// Calls returns copies of what was received so far.
func (l *Logs) Calls() ([]IngestCall, []core.LogEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]IngestCall(nil), l.Ingested...), append([]core.LogEvent(nil), l.Events...)
}

var _ core.Logs = (*Logs)(nil)
