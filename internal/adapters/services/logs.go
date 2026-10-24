package services

import (
	"context"
	"net/http"

	"github.com/rupaai/homelab/internal/core"
)

// Logs is core.Logs backed by homelab-services, called as the system caller like Versions.
type Logs struct{ s *Services }

var _ core.Logs = (*Logs)(nil)

// Logs returns the logs client.
func (s *Services) Logs() *Logs { return &Logs{s: s} }

const (
	// logsMaxLines and logsMaxBytes keep one call under the service's limits (2000 lines,
	// 4 MB of body).
	logsMaxLines = 2000
	logsMaxBytes = 3 << 20
)

// Ingest sends the lines of a batch. A big batch goes in several calls.
func (l *Logs) Ingest(_ context.Context, b core.LogBatch, minLevel string) error {
	for len(b.Lines) > 0 {
		n, size := 0, 0
		for n < len(b.Lines) && n < logsMaxLines && (n == 0 || size+len(b.Lines[n].Text)+64 <= logsMaxBytes) {
			size += len(b.Lines[n].Text) + 64
			n++
		}
		body := map[string]any{"app": b.App, "env": b.Env, "service": b.Service, "min_level": minLevel, "lines": b.Lines[:n]}
		if _, err := l.s.call("logs", errNoServices, callTimeout, http.MethodPost, "/internal/logs/ingest", nil, body); err != nil {
			return err
		}
		b.Lines = b.Lines[n:]
	}
	return nil
}

// Event sends one line about Homelab's own work.
func (l *Logs) Event(_ context.Context, e core.LogEvent) error {
	_, err := l.s.call("logs", errNoServices, callTimeout, http.MethodPost, "/internal/logs/event", nil, e)
	return err
}
