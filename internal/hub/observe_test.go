package hub

import (
	"context"
	"testing"

	"github.com/rupaai/homelab/internal/state"
)

// A round asks the container engine about each app once, not about everything once per app.
func TestRoundObservesEachAppOnce(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	for _, id := range []string{"alpha", "beta", "gamma"} {
		if _, err := r.store.AddApp(ctx, state.App{ID: id, Name: id, Repo: "a/" + id, Manifest: []byte("id: " + id), Promotion: "manual"}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(round string, wantFull int) {
		t.Helper()
		if r.dep.FullObserves != wantFull {
			t.Errorf("%s: %d full observes, want %d", round, r.dep.FullObserves, wantFull)
		}
		for _, id := range []string{"alpha", "beta", "gamma"} {
			if n := r.dep.AppObserves[id]; n != 1 {
				t.Errorf("%s: %s observed %d times, want 1", round, id, n)
			}
		}
		r.dep.FullObserves, r.dep.AppObserves = 0, nil
	}
	r.hub.Reconcile(ctx)
	check("reconcile", 1) // the one full look finds environments of apps no longer registered
	r.hub.CheckHealth(ctx)
	check("health", 0)
}
