package httphealth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

func TestCheckStatus(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantOK     bool
		wantCode   int
		wantDetail string
	}{
		{"200 is healthy", http.StatusOK, true, 200, "HTTP 200"},
		{"204 is healthy", http.StatusNoContent, true, 204, "HTTP 204"},
		{"299 is healthy", 299, true, 299, "HTTP 299"},
		{"404 is not", http.StatusNotFound, false, 404, "HTTP 404"},
		{"500 is not", http.StatusInternalServerError, false, 500, "HTTP 500"},
		{"503 is not", http.StatusServiceUnavailable, false, 503, "HTTP 503"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("body"))
			}))
			defer srv.Close()

			got := New(nil).Check(context.Background(), srv.URL+"/healthz")
			if got.OK != tc.wantOK || got.Code != tc.wantCode || got.Detail != tc.wantDetail {
				t.Fatalf("Check = %+v; want OK=%v Code=%d Detail=%q", got, tc.wantOK, tc.wantCode, tc.wantDetail)
			}
			if got.Latency <= 0 {
				t.Errorf("Latency = %v; want > 0", got.Latency)
			}
		})
	}
}

func TestCheckErrors(t *testing.T) {
	// A server that has been closed gives a port nothing listens on.
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer slow.Close()
	defer close(release)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name       string
		checker    *Checker
		ctx        context.Context
		target     string
		wantDetail string
	}{
		{"connection refused", New(nil), context.Background(), closedURL, "connection refused"},
		{"timed out", New(&http.Client{Timeout: 50 * time.Millisecond}), context.Background(), slow.URL, "timed out"},
		{"context canceled", New(nil), canceled, slow.URL, "canceled"},
		{"invalid URL", New(nil), context.Background(), "://bad", "missing protocol scheme"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.checker.Check(tc.ctx, tc.target)
			if got.OK || got.Code != 0 {
				t.Fatalf("Check = %+v; want not OK with Code 0", got)
			}
			if got.Detail != tc.wantDetail {
				t.Fatalf("Detail = %q; want %q", got.Detail, tc.wantDetail)
			}
		})
	}
}

func TestCheckRedirects(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/healthz", http.StatusFound)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/healthz", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tests := []struct {
		name     string
		path     string
		wantOK   bool
		wantCode int
	}{
		{"same-host redirect is followed", "/same", true, 200},
		{"redirect to another host is not followed", "/elsewhere", false, 302},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := New(nil).Check(context.Background(), srv.URL+tc.path)
			if got.OK != tc.wantOK || got.Code != tc.wantCode {
				t.Fatalf("Check = %+v; want OK=%v Code=%d", got, tc.wantOK, tc.wantCode)
			}
		})
	}
}

func TestCheckLargeBodyIsDiscarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1<<20)
		for i := 0; i < 4; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	got := New(nil).Check(context.Background(), srv.URL)
	if !got.OK || got.Code != 200 {
		t.Fatalf("Check = %+v; want OK with Code 200", got)
	}
}

func TestNewNilClientDefaults(t *testing.T) {
	c := New(nil)
	if c.client.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v; want 5s", c.client.Timeout)
	}
	if c.client.CheckRedirect == nil {
		t.Errorf("CheckRedirect is nil; want a same-host check")
	}
}

var _ core.HealthChecker = (*Checker)(nil)
