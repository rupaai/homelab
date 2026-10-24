package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

func TestIngestPostsTheBatchAsTheSystemCaller(t *testing.T) {
	s, b := newSecretsBackend(t)
	b.answer(200, `{"stored":2}`)
	err := s.Logs().Ingest(context.Background(), core.LogBatch{App: "movies", Env: "prod", Service: "app",
		Lines: []core.LogLine{{T: 1730000000123, Stream: "stdout", Text: "hello"}, {T: 1730000000124, Stream: "stderr", Text: "oops"}}}, "info")
	if err != nil {
		t.Fatal(err)
	}
	c := b.last(t, s)
	if c.method != "POST" || c.uri != "/internal/logs/ingest" || c.caller.Kind != core.ServiceKindSystem {
		t.Fatalf("%s %s %+v", c.method, c.uri, c.caller)
	}
	var got struct {
		App, Env, Service string
		MinLevel          string `json:"min_level"`
		Lines             []core.LogLine
	}
	if err := json.Unmarshal([]byte(c.body), &got); err != nil {
		t.Fatal(err)
	}
	if got.App != "movies" || got.Env != "prod" || got.Service != "app" || got.MinLevel != "info" || len(got.Lines) != 2 ||
		got.Lines[0].T != 1730000000123 || got.Lines[1].Stream != "stderr" || got.Lines[1].Text != "oops" {
		t.Fatalf("%+v", got)
	}
}

func TestABigBatchGoesInSeveralCalls(t *testing.T) {
	s, b := newSecretsBackend(t)
	b.answer(200, `{"stored":0}`)
	var lines []core.LogLine
	for i := 0; i < 4500; i++ {
		lines = append(lines, core.LogLine{T: int64(i), Stream: "stdout", Text: "x"})
	}
	if err := s.Logs().Ingest(context.Background(), core.LogBatch{App: "a1", Env: "dev", Service: "app", Lines: lines}, "debug"); err != nil {
		t.Fatal(err)
	}
	if b.count() != 3 {
		t.Fatalf("calls = %d, want 3 (2000 + 2000 + 500)", b.count())
	}
	// Long lines are split by size as well.
	b2s, b2 := newSecretsBackend(t)
	b2.answer(200, `{"stored":0}`)
	long := strings.Repeat("y", 17000)
	lines = nil
	for i := 0; i < 500; i++ {
		lines = append(lines, core.LogLine{T: int64(i), Stream: "stdout", Text: long})
	}
	if err := b2s.Logs().Ingest(context.Background(), core.LogBatch{App: "a1", Env: "dev", Service: "app", Lines: lines}, "debug"); err != nil {
		t.Fatal(err)
	}
	if b2.count() < 3 {
		t.Fatalf("500 long lines went in %d calls", b2.count())
	}
	for _, c := range b2.calls {
		if len(c.body) > 4<<20 {
			t.Fatalf("a body of %d bytes", len(c.body))
		}
	}
}

func TestEventPostsOneLine(t *testing.T) {
	s, b := newSecretsBackend(t)
	b.answer(200, `{"stored":1}`)
	err := s.Logs().Event(context.Background(), core.LogEvent{Service: "deploys", Level: "info", Msg: "Deployed", AboutApp: "movies", AboutEnv: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	c := b.last(t, s)
	if c.uri != "/internal/logs/event" || c.caller.Kind != core.ServiceKindSystem {
		t.Fatalf("%s %+v", c.uri, c.caller)
	}
	for _, want := range []string{`"service":"deploys"`, `"level":"info"`, `"msg":"Deployed"`, `"about_app":"movies"`, `"about_env":"prod"`} {
		if !strings.Contains(c.body, want) {
			t.Errorf("body %s lacks %s", c.body, want)
		}
	}
}

func TestLogsFailWhenTheServiceIsDownOrRefuses(t *testing.T) {
	s, b := newSecretsBackend(t)
	b.answer(400, `{"error":"bad name"}`)
	if err := s.Logs().Event(context.Background(), core.LogEvent{Service: "x", Level: "info", Msg: "m"}); err == nil || !strings.Contains(err.Error(), "bad name") {
		t.Fatalf("%v", err)
	}
	n := b.count()
	s.set(false, "stopped")
	if err := s.Logs().Ingest(context.Background(), core.LogBatch{Lines: []core.LogLine{{Text: "a"}}}, "info"); err == nil {
		t.Fatal("no error while down")
	}
	if b.count() != n {
		t.Fatal("called while down")
	}
}

func TestTheLogsSettingsGoToTheProgramInItsEnvironment(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	prog := filepath.Join(dir, "fake-services")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\nenv > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const cfg = `{"min_level":"warn","services":{"files":"debug"},"max_mb":512}`
	for _, c := range []struct{ opt, want string }{{cfg, "HOMELAB_SERVICES_LOGS=" + cfg}, {"", ""}} {
		_ = os.Remove(out)
		s, err := New(Options{Program: prog, DataDir: dir, Port: 1, LogsConfig: c.opt})
		if err != nil {
			t.Fatal(err)
		}
		_ = s.runOnce(context.Background())
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		has := strings.Contains(string(b), "HOMELAB_SERVICES_LOGS=")
		if c.want == "" && has {
			t.Error("the logs variable was set without settings")
		}
		if c.want != "" && !strings.Contains(string(b), c.want+"\n") {
			t.Errorf("env lacks %s:\n%s", c.want, b)
		}
	}
}
