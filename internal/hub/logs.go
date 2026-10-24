package hub

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

const (
	// logMaskTTL is how long the secrets to hide and the app's min_level are remembered.
	// Homelab forgets them at once when it sets, makes or deletes a secret or deploys the
	// app. A secret the app saves itself (secrets API) is hidden within this time.
	logMaskTTL = 5 * time.Second
	// logEventTimeout bounds sending one activity event to the logs.
	logEventTimeout = 5 * time.Second
	// logEventsAtOnce is how many activity events may be on their way to the logs at once.
	// More are dropped: the activity log has them anyway.
	logEventsAtOnce = 32
)

// logMask is what IngestLogs remembers about one app environment.
type logMask struct {
	at       time.Time
	minLevel string
	masker   interface{ Replace(string) string } // nil: nothing to hide
}

// forgetLogMask drops what IngestLogs remembers about an app environment (env "" is every
// environment of the app; app "" is every app, for platform settings).
func (h *Hub) forgetLogMask(app, env string) {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	for k := range h.logMasks {
		if (app == "" || k.app == app) && (env == "" || k.env == env) {
			delete(h.logMasks, k)
		}
	}
}

// logSettings gives the min_level and the masker of an app environment, from a cache that
// lasts logMaskTTL. ok is false for an app Homelab doesn't know.
func (h *Hub) logSettings(ctx context.Context, app, env string) (logMask, bool, error) {
	key := envKey{app, env}
	now := h.cfg.Now()
	h.logMu.Lock()
	m, hit := h.logMasks[key]
	h.logMu.Unlock()
	if hit && now.Sub(m.at) < logMaskTTL {
		return m, true, nil
	}
	a, err := h.store.App(ctx, app)
	if errors.Is(err, core.ErrNotFound) {
		return logMask{}, false, nil
	}
	if err != nil {
		return logMask{}, false, err
	}
	m = logMask{at: now, minLevel: "info"}
	if env == core.Dev {
		m.minLevel = "debug"
	}
	if mf, err := h.parse(a.Manifest, a.Repo); err == nil {
		m.minLevel = mf.LogMinLevel(env)
	}
	if r := h.masker(ctx, a, env); r != nil {
		m.masker = r
	}
	h.logMu.Lock()
	if h.logMasks == nil {
		h.logMasks = map[envKey]logMask{}
	}
	h.logMasks[key] = m
	h.logMu.Unlock()
	return m, true, nil
}

// IngestLogs stores the lines an app container wrote. Secret values are hidden first, the
// same way the live logs hide them, and the app's min_level (homelab.yaml, else debug on
// dev and info on prod) is passed on. Lines of an app Homelab doesn't know are dropped.
func (h *Hub) IngestLogs(ctx context.Context, b core.LogBatch) error {
	if h.ad.Logs == nil || len(b.Lines) == 0 {
		return nil
	}
	m, ok, err := h.logSettings(ctx, b.App, b.Env)
	if err != nil || !ok {
		return err
	}
	if m.masker != nil {
		lines := make([]core.LogLine, len(b.Lines))
		for i, l := range b.Lines {
			l.Text = m.masker.Replace(l.Text)
			lines[i] = l
		}
		b.Lines = lines
	}
	return h.ad.Logs.Ingest(ctx, b, m.minLevel)
}

// sendLogEvent mirrors an activity event to the logs as a Homelab event. It never blocks
// and never fails the caller: it sends in the background for at most 5 seconds and drops
// the event on any error.
//
// An event that is about an app environment shows in that app's own logs, and the app's
// key can read them. So:
//   - The text is masked with the app environment's secrets, like its container logs.
//   - An event whose text may carry what other programs said (errors, health answers,
//     smoke test output) is not linked to the app: it names the app in extra and only
//     admins see it.
//   - The actor (a person's name or a key's name) is only sent for events about an app
//     when it is Homelab itself.
func (h *Hub) sendLogEvent(e state.Event) {
	if h.ad.Logs == nil {
		return
	}
	h.logMu.Lock()
	if h.logSending == nil {
		h.logSending = make(chan struct{}, logEventsAtOnce)
	}
	sending := h.logSending
	h.logMu.Unlock()
	select {
	case sending <- struct{}{}:
	default:
		return
	}
	ev := core.LogEvent{Service: logEventService(e.Action), Level: "info", Msg: e.Detail, AboutApp: e.App, AboutEnv: e.Env,
		Extra: map[string]any{"action": e.Action}}
	if strings.HasSuffix(e.Action, "failed") {
		ev.Level = "error"
	}
	if e.App != "" && logEventUntrusted(e.Action) {
		ev.AboutApp, ev.AboutEnv = "", ""
		ev.Extra["app"], ev.Extra["env"] = e.App, e.Env
	}
	if ev.AboutApp == "" || e.Actor == core.ActorHomelab {
		ev.Extra["actor"] = e.Actor
	}
	go func() {
		defer func() { <-sending }()
		ctx, cancel := context.WithTimeout(context.Background(), logEventTimeout)
		defer cancel()
		if e.App != "" && e.Env != "" {
			if m, ok, err := h.logSettings(ctx, e.App, e.Env); err == nil && ok && m.masker != nil {
				ev.Msg = m.masker.Replace(ev.Msg)
			}
		}
		_ = h.ad.Logs.Event(ctx, ev)
	}()
}

// logEventUntrusted says whether an activity event's text may hold output of another
// program: an error from Docker, GitHub or the app, a health answer, smoke test output.
// Those are not shown in the app's own logs.
func logEventUntrusted(action string) bool {
	return strings.HasSuffix(action, "failed") || strings.HasPrefix(action, "health.") ||
		strings.HasPrefix(action, "smoke.") || action == "key.used" || action == "build.skipped"
}

// logEventService is the service an activity action belongs to in the logs: deploys for
// the things that ship an app, else the text before the first dot (homelab.started is
// homelab, key.scopes is key).
func logEventService(action string) string {
	prefix, _, _ := strings.Cut(action, ".")
	switch prefix {
	case "deploy", "promote", "rollback", "build":
		return "deploys"
	}
	var b strings.Builder
	for _, r := range strings.ToLower(prefix) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := b.String()
	if s == "" || s[0] == '-' || s[0] == '_' {
		return "homelab"
	}
	return s
}
