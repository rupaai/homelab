package pocketid

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestProxyKeepsSessionCookieOnPlainHTTPLocalhost(t *testing.T) {
	var gotHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		// Pocket ID v2.18 sets these attributes even when APP_URL is http://.
		w.Header().Add("Set-Cookie", "access_token=secret-value; Path=/; Max-Age=3600; HttpOnly; Secure; SameSite=Lax")
		w.Header().Add("Set-Cookie", "session=other; Path=/; secure; HttpOnly")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	proxy := Proxy(target)

	req := httptest.NewRequest(http.MethodPost, "http://localhost:7081/api/one-time-access-token/abc", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if gotHost != "localhost:7081" {
		t.Fatalf("Pocket ID saw Host %q, want APP_URL's host so WebAuthn origin checks pass", gotHost)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("got %d cookies, want 2: %v", len(cookies), rec.Header().Values("Set-Cookie"))
	}
	for _, c := range cookies {
		if c.Secure {
			t.Errorf("cookie %s is still Secure over plain HTTP; Safari will drop it", c.Name)
		}
		if !c.HttpOnly {
			t.Errorf("cookie %s lost HttpOnly", c.Name)
		}
	}
	if cookies[0].Value != "secret-value" || cookies[0].MaxAge != 3600 || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Errorf("access_token attributes changed: %+v", cookies[0])
	}
}

func TestProxyKeepsSecureOverTLS(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "__Host-access_token=v; Path=/; HttpOnly; Secure; SameSite=Lax")
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "https://id.example.test/", nil)
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	Proxy(target).ServeHTTP(rec, req)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("Secure must be kept over TLS, got %v", rec.Header().Values("Set-Cookie"))
	}
}
