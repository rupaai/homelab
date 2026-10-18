package pocketid

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// Proxy serves Pocket ID at its public address over plain HTTP. Pocket ID marks
// every session cookie Secure, even when APP_URL is http://. Chrome keeps Secure
// cookies on http://localhost, but Safari drops them, so the one-time sign-in
// succeeds and the next page bounces back to /login. Like Homelab's own cookies,
// Pocket ID's cookies are marked Secure only when the request arrived over TLS.
func Proxy(target *url.URL) http.Handler {
	// The default director keeps the incoming Host, so Pocket ID still sees
	// APP_URL's host for WebAuthn origin checks.
	overTLS := httputil.NewSingleHostReverseProxy(target)
	plain := httputil.NewSingleHostReverseProxy(target)
	plain.ModifyResponse = func(res *http.Response) error {
		cookies := res.Header.Values("Set-Cookie")
		if len(cookies) == 0 {
			return nil
		}
		res.Header.Del("Set-Cookie")
		for _, c := range cookies {
			res.Header.Add("Set-Cookie", withoutSecure(c))
		}
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			overTLS.ServeHTTP(w, r)
			return
		}
		plain.ServeHTTP(w, r)
	})
}

func withoutSecure(cookie string) string {
	parts := strings.Split(cookie, ";")
	kept := parts[:1]
	for _, attr := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(attr), "secure") {
			continue
		}
		kept = append(kept, attr)
	}
	return strings.Join(kept, ";")
}
