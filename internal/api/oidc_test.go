package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestVerifyIDTokenChecksSignatureIssuerAudienceNonceAndExpiry(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := "https://issuer.example.test"
	keySet, _ := json.Marshal(jwks{Keys: []jwk{{Kty: "RSA", Kid: "test-key", Alg: "RS256", N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()), E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(keySet))), Header: make(http.Header)}, nil
	})}
	provider := oidcDiscovery{Issuer: issuer, AuthorizationEndpoint: issuer + "/authorize", TokenEndpoint: issuer + "/token", JWKSURI: issuer + "/keys"}
	makeToken := func(issuer, aud, nonce string, expiry time.Time) string {
		header, _ := json.Marshal(jwtHeader{Alg: "RS256", Kid: "test-key"})
		claims, _ := json.Marshal(jwtClaims{Issuer: issuer, Subject: "person-id", Email: "person@example.test", Nonce: nonce, Audience: aud, Exp: expiry.Unix()})
		input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(input))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		return input + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	raw := makeToken(issuer, "homelab", "expected-nonce", time.Now().Add(time.Minute))
	sub, email, err := verifyIDTokenWithClient(context.Background(), provider, raw, "homelab", "expected-nonce", client)
	if err != nil || sub != "person-id" || email != "person@example.test" {
		t.Fatalf("verify: sub=%q email=%q err=%v", sub, email, err)
	}
	for name, token := range map[string]string{
		"issuer":   makeToken("http://wrong.test", "homelab", "expected-nonce", time.Now().Add(time.Minute)),
		"audience": makeToken(issuer, "another-client", "expected-nonce", time.Now().Add(time.Minute)),
		"nonce":    makeToken(issuer, "homelab", "wrong-nonce", time.Now().Add(time.Minute)),
		"expiry":   makeToken(issuer, "homelab", "expected-nonce", time.Now().Add(-time.Minute)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := verifyIDTokenWithClient(context.Background(), provider, token, "homelab", "expected-nonce", client); err == nil {
				t.Fatal("accepted an invalid token")
			}
		})
	}
	parts := strings.Split(raw, ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"` + issuer + `","sub":"attacker","aud":"homelab","nonce":"expected-nonce","exp":9999999999}`))
	bad := strings.Join(parts, ".")
	if _, _, err := verifyIDTokenWithClient(context.Background(), provider, bad, "homelab", "expected-nonce", client); err == nil {
		t.Fatal("accepted an unsigned claim change")
	}
}

func TestOIDCFlowUsesPKCEAndCreatesServerSideSession(t *testing.T) {
	r := newRig(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := "http://localhost:7081"
	clientID := "homelab-client"
	var secretBytes [32]byte
	if _, err := rand.Read(secretBytes[:]); err != nil {
		t.Fatal(err)
	}
	clientSecret := base64.RawURLEncoding.EncodeToString(secretBytes[:])
	keySet, _ := json.Marshal(jwks{Keys: []jwk{{Kty: "RSA", Kid: "active", Alg: "RS256", N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()), E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	makeIDToken := func(subject, email, nonce string) string {
		header, _ := json.Marshal(jwtHeader{Alg: "RS256", Kid: "active"})
		claims, _ := json.Marshal(jwtClaims{Issuer: issuer, Subject: subject, Email: email, Nonce: nonce, Audience: clientID, Exp: time.Now().Add(time.Minute).Unix()})
		input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(input))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		return input + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	var idToken string
	accessToken, err := randomURLToken(32)
	if err != nil {
		t.Fatal(err)
	}
	authorizationCode, err := randomURLToken(32)
	if err != nil {
		t.Fatal(err)
	}
	var verifierSeen bool
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var status int
		var body string
		switch req.URL.Path {
		case "/.well-known/openid-configuration":
			status = http.StatusOK
			body = `{"issuer":"` + issuer + `","authorization_endpoint":"` + issuer + `/authorize","token_endpoint":"` + issuer + `/token","jwks_uri":"` + issuer + `/keys"}`
		case "/token":
			status = http.StatusOK
			form, _ := io.ReadAll(req.Body)
			values, _ := url.ParseQuery(string(form))
			verifierSeen = values.Get("code_verifier") != ""
			body = `{"access_token":"` + accessToken + `","token_type":"Bearer","id_token":"` + idToken + `"}`
		case "/keys":
			status = http.StatusOK
			body = string(keySet)
		default:
			return nil, errors.New("unexpected OIDC request")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	})}
	h := asLocal(New(r.h, Options{Version: "test", Static: testSite(), Store: r.store, OIDCIssuer: issuer, OIDCClientID: clientID, OIDCClientSecret: clientSecret, HTTPClient: client}))
	start := httptest.NewRequest("GET", "/auth/start?return=%2Fhome", nil)
	start.Host = "localhost:7080"
	started := httptest.NewRecorder()
	h.ServeHTTP(started, start)
	if started.Code != http.StatusSeeOther || !strings.Contains(started.Header().Get("Location"), "code_challenge_method=S256") {
		t.Fatalf("start=%d location=%q", started.Code, started.Header().Get("Location"))
	}
	cookies := started.Result().Cookies()
	var values = map[string]string{}
	for _, cookie := range cookies {
		values[cookie.Name] = cookie.Value
	}
	if values[oidcStateCookie] == "" || values[oidcNonceCookie] == "" || values[oidcVerifierCookie] == "" {
		t.Fatalf("missing OIDC cookies: %v", values)
	}
	idToken = makeIDToken("owner", "owner@example.test", values[oidcNonceCookie])
	callback := httptest.NewRequest("GET", "/auth/callback?state="+url.QueryEscape(values[oidcStateCookie])+"&code="+url.QueryEscape(authorizationCode), nil)
	callback.Host = "localhost:7080"
	for _, cookie := range cookies {
		callback.AddCookie(cookie)
	}
	completed := httptest.NewRecorder()
	h.ServeHTTP(completed, callback)
	if completed.Code != http.StatusSeeOther || completed.Header().Get("Location") != "/home" || !verifierSeen {
		t.Fatalf("callback=%d location=%q PKCE=%v body=%s", completed.Code, completed.Header().Get("Location"), verifierSeen, completed.Body)
	}
	var session *http.Cookie
	for _, cookie := range completed.Result().Cookies() {
		if cookie.Name == "homelab_session" {
			session = cookie
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie=%+v", session)
	}
	me := httptest.NewRequest("GET", "/api/me", nil)
	me.Host = "localhost:7080"
	me.AddCookie(session)
	result := httptest.NewRecorder()
	h.ServeHTTP(result, me)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "owner@example.test") {
		t.Fatalf("me=%d %s", result.Code, result.Body)
	}
	started = httptest.NewRecorder()
	start = httptest.NewRequest("GET", "/auth/start", nil)
	start.Host = "localhost:7080"
	h.ServeHTTP(started, start)
	values = map[string]string{}
	for _, cookie := range started.Result().Cookies() {
		values[cookie.Name] = cookie.Value
	}
	idToken = makeIDToken("unrecognized-provider-user", "owner@example.test", values[oidcNonceCookie])
	callback = httptest.NewRequest("GET", "/auth/callback?state="+url.QueryEscape(values[oidcStateCookie])+"&code="+url.QueryEscape(authorizationCode), nil)
	callback.Host = "localhost:7080"
	for _, cookie := range started.Result().Cookies() {
		callback.AddCookie(cookie)
	}
	denied := httptest.NewRecorder()
	h.ServeHTTP(denied, callback)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("unknown provider subject with owner's email: status=%d body=%s", denied.Code, denied.Body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
