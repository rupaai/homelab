package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ObserveApp asks the engine for one app's containers only, and inspects only those.
func TestObserveAppFiltersByApp(t *testing.T) {
	var mu sync.Mutex
	var filters, inspected []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/containers/json" {
			filters = append(filters, r.URL.Query().Get("filters"))
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "m1", "State": "running", "Labels": map[string]string{
				labelApp: "movies", labelEnv: "prod", "com.docker.compose.service": serviceName}}})
			return
		}
		inspected = append(inspected, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"State": map[string]any{"StartedAt": "2026-01-02T03:04:05Z"}})
	}))
	defer srv.Close()
	api, err := newEngine("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	d := &Deployer{api: api}
	got, err := d.ObserveApp(context.Background(), "movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].App != "movies" || got[0].Env != "prod" || got[0].StartedAt.IsZero() {
		t.Fatalf("got %+v", got)
	}
	if len(filters) != 1 || !strings.Contains(filters[0], `"`+labelApp+`=movies"`) {
		t.Errorf("list filters = %v, want the label %s=movies", filters, labelApp)
	}
	if len(inspected) != 1 || inspected[0] != "/containers/m1/json" {
		t.Errorf("inspected %v, want only m1", inspected)
	}
}
