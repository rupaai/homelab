package core

import (
	"context"
	"slices"
)

// LogLevels are the levels a log line can have, lowest first. A line with no clear level
// is "unknown" and is always kept, so it is not in this list.
var LogLevels = []string{"debug", "info", "warn", "error", "fatal"}

// ValidLogLevel says whether s is a level that min_level may be.
func ValidLogLevel(s string) bool { return slices.Contains(LogLevels, s) }

// LogLine is one line an app container wrote.
type LogLine struct {
	T      int64  `json:"t"`      // unix milliseconds
	Stream string `json:"stream"` // stdout | stderr
	Text   string `json:"text"`
}

// LogBatch is lines of one container: one app environment, one service.
type LogBatch struct {
	App     string    `json:"app"`
	Env     string    `json:"env"`
	Service string    `json:"service"`
	Lines   []LogLine `json:"lines"`
}

// LogEvent is a line Homelab writes about its own work. About names the app environment
// it is about, when there is one. It never holds secret values, SQL, file contents or keys.
type LogEvent struct {
	Service  string         `json:"service"`
	Level    string         `json:"level"`
	Msg      string         `json:"msg"`
	Extra    map[string]any `json:"extra,omitempty"`
	AboutApp string         `json:"about_app,omitempty"`
	AboutEnv string         `json:"about_env,omitempty"`
}

// Logs stores and searches logs (homelab-services does the work).
type Logs interface {
	// Ingest stores the lines of a batch that are at or above minLevel.
	Ingest(ctx context.Context, b LogBatch, minLevel string) error
	// Event stores one line about Homelab's own work.
	Event(ctx context.Context, e LogEvent) error
}
