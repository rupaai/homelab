package selfupdate

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestRestartNeedsSystemd(t *testing.T) {
	var stops atomic.Int32
	u := New(Options{Restart: func() { stops.Add(1) }})
	if u.CanRestart() {
		t.Fatal("CanRestart without systemd")
	}
	if err := u.Restart(context.Background()); err == nil {
		t.Fatal("Restart without systemd")
	}
	time.Sleep(1200 * time.Millisecond)
	if stops.Load() != 0 {
		t.Fatal("stopped without systemd")
	}
	u = New(Options{Systemd: true, Restart: func() { stops.Add(1) }})
	if !u.CanRestart() || u.Restart(context.Background()) != nil {
		t.Fatal("Restart under systemd failed")
	}
	time.Sleep(1500 * time.Millisecond)
	if stops.Load() != 1 {
		t.Fatalf("stopped %d times", stops.Load())
	}
}

func TestUnderSystemdReadsInvocationID(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")
	if UnderSystemd() {
		t.Error("systemd without INVOCATION_ID")
	}
	t.Setenv("INVOCATION_ID", "abc")
	if UnderSystemd() != (runtime.GOOS == "linux") {
		t.Error("INVOCATION_ID not honoured")
	}
}
