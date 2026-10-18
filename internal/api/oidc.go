package api

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

const (
	oidcStateCookie    = "homelab_oidc_state"
	oidcNonceCookie    = "homelab_oidc_nonce"
	oidcVerifierCookie = "homelab_oidc_verifier"
	oidcReturnCookie   = "homelab_oidc_return"
)

type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

func (s *server) authStart(w http.ResponseWriter, r *http.Request) {
	if !loopbackHost(r.Host) {
		http.Error(w, "Open Homelab at http://localhost:7080 on this computer.", http.StatusForbidden)
		return
	}
	issuer, clientID, clientSecret := s.oidc.values()
	if clientID == "" || clientSecret == "" || issuer == "" {
		http.Error(w, "Homelab sign-in is still starting. Try again in a moment.", http.StatusServiceUnavailable)
		return
	}
	provider, err := s.discover(r.Context(), issuer)
	if err != nil {
		s.log.Error("Pocket ID discovery failed", "err", err)
		http.Error(w, "Homelab couldn't reach its sign-in service.", http.StatusBadGateway)
		return
	}
	state, err := randomURLToken(32)
	if err != nil {
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	returnTo := sanitizeReturn(r.URL.Query().Get("return"))
	for name, value := range map[string]string{oidcStateCookie: state, oidcNonceCookie: nonce, oidcVerifierCookie: verifier, oidcReturnCookie: returnTo} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	}
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID},
		"redirect_uri": {s.oidcRedirectURL(r)}, "scope": {"openid profile email"},
		"state": {state}, "nonce": {nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, provider.AuthorizationEndpoint+"?"+q.Encode(), http.StatusSeeOther)
}

func (s *server) authCallback(w http.ResponseWriter, r *http.Request) {
	if !loopbackHost(r.Host) {
		http.Error(w, "Open Homelab on this computer.", http.StatusForbidden)
		return
	}
	state, ok1 := cookie(r, oidcStateCookie)
	nonce, ok2 := cookie(r, oidcNonceCookie)
	verifier, ok3 := cookie(r, oidcVerifierCookie)
	returnTo, _ := cookie(r, oidcReturnCookie)
	for _, name := range []string{oidcStateCookie, oidcNonceCookie, oidcVerifierCookie, oidcReturnCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	}
	if !ok1 || !ok2 || !ok3 || subtle.ConstantTimeCompare([]byte(state), []byte(r.URL.Query().Get("state"))) != 1 || r.URL.Query().Get("error") != "" {
		http.Error(w, "That sign-in link expired. Start again from Homelab.", http.StatusBadRequest)
		return
	}
	issuer, clientID, clientSecret := s.oidc.values()
	if clientID == "" || clientSecret == "" || issuer == "" {
		http.Error(w, "Homelab sign-in is still starting. Try again in a moment.", http.StatusServiceUnavailable)
		return
	}
	provider, err := s.discover(r.Context(), issuer)
	if err != nil {
		s.log.Error("Pocket ID discovery failed", "err", err)
		http.Error(w, "Homelab couldn't reach its sign-in service.", http.StatusBadGateway)
		return
	}
	conf := oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: s.oidcRedirectURL(r), Scopes: []string{"openid", "profile", "email"}, Endpoint: oauth2.Endpoint{AuthURL: provider.AuthorizationEndpoint, TokenURL: provider.TokenEndpoint}}
	ctx := r.Context()
	if s.httpClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
	}
	token, err := conf.Exchange(ctx, r.URL.Query().Get("code"), oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		s.log.Warn("OIDC code exchange failed", "err", err)
		http.Error(w, "Sign-in didn't finish. Please try again.", http.StatusUnauthorized)
		return
	}
	idToken, _ := token.Extra("id_token").(string)
	subject, _, err := verifyIDTokenWithClient(r.Context(), provider, idToken, clientID, nonce, s.httpClient)
	if err != nil {
		s.log.Warn("OIDC token rejected", "err", err)
		http.Error(w, "Homelab couldn't verify that sign-in. Please try again.", http.StatusUnauthorized)
		return
	}
	p, err := s.store.PersonByIdentityID(r.Context(), subject)
	if errors.Is(err, core.ErrNotFound) {
		p, err = s.store.Person(r.Context(), subject)
		if err == nil && p.IdentityID != "" && p.IdentityID != subject {
			err = core.ErrNotFound
		}
	}
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	if err != nil || p.Status == "removed" {
		http.Error(w, "This account hasn't been invited to this Homelab.", http.StatusForbidden)
		return
	}
	if err := s.store.RecordSignIn(r.Context(), p.ID, subject); err != nil {
		s.log.Error("record sign-in failed", "err", err)
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	if err := s.store.RevokeInvites(r.Context(), p.ID); err != nil {
		s.log.Error("revoke accepted invites failed", "err", err)
	}
	if err := s.issueSession(w, r, p.ID); err != nil {
		s.log.Error("create session failed", "err", err)
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	_ = s.store.Log(r.Context(), stateEvent(p.Email, "person.signed_in", "Signed in with a passkey"))
	returnTo = sanitizeReturn(returnTo)
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

func (s *server) discover(ctx context.Context, issuer string) (oidcDiscovery, error) {
	base := strings.TrimRight(issuer, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/.well-known/openid-configuration", nil)
	if err != nil {
		return oidcDiscovery{}, err
	}
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return oidcDiscovery{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return oidcDiscovery{}, fmt.Errorf("discovery returned %s", res.Status)
	}
	var d oidcDiscovery
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&d); err != nil {
		return d, err
	}
	if d.Issuer != base || d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return d, errors.New("incomplete or mismatched OIDC discovery document")
	}
	return d, nil
}

func (s *server) oidcRedirectURL(r *http.Request) string {
	return "http://localhost:7080/auth/callback"
}

func cookie(r *http.Request, name string) (string, bool) {
	c, err := r.Cookie(name)
	return c.Value, err == nil && c.Value != ""
}

func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *server) issueSession(w http.ResponseWriter, r *http.Request, personID string) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	now := time.Now()
	digest := sha256.Sum256(raw)
	if err := s.store.PutSession(r.Context(), digest[:], personID, now, now.Add(30*24*time.Hour)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: "homelab_session", Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 60 * 60})
	return nil
}

func stateEvent(actor, action, detail string) state.Event {
	return state.Event{Actor: actor, Action: action, Detail: detail}
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}
type jwtClaims struct {
	Issuer   string `json:"iss"`
	Subject  string `json:"sub"`
	Email    string `json:"email"`
	Nonce    string `json:"nonce"`
	Audience any    `json:"aud"`
	Exp      int64  `json:"exp"`
}
type jwks struct {
	Keys []jwk `json:"keys"`
}
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Crv string `json:"crv"`
}

func verifyIDToken(ctx context.Context, provider oidcDiscovery, raw, audience, nonce string) (string, string, error) {
	return verifyIDTokenWithClient(ctx, provider, raw, audience, nonce, http.DefaultClient)
}

func verifyIDTokenWithClient(ctx context.Context, provider oidcDiscovery, raw, audience, nonce string, client *http.Client) (string, string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", "", errors.New("malformed ID token")
	}
	decode := func(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
	hb, err := decode(parts[0])
	if err != nil {
		return "", "", err
	}
	pb, err := decode(parts[1])
	if err != nil {
		return "", "", err
	}
	sig, err := decode(parts[2])
	if err != nil {
		return "", "", err
	}
	var h jwtHeader
	var c jwtClaims
	if json.Unmarshal(hb, &h) != nil || json.Unmarshal(pb, &c) != nil {
		return "", "", errors.New("invalid ID token JSON")
	}
	if c.Issuer != provider.Issuer || c.Subject == "" || c.Nonce != nonce || c.Exp <= time.Now().Unix() || !hasAudience(c.Audience, audience) {
		return "", "", errors.New("ID token claims did not match")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.JWKSURI, nil)
	if err != nil {
		return "", "", err
	}
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", "", fmt.Errorf("JWKS returned %s", res.Status)
	}
	var set jwks
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&set); err != nil {
		return "", "", err
	}
	var key *jwk
	for i := range set.Keys {
		if set.Keys[i].Kid == h.Kid {
			key = &set.Keys[i]
			break
		}
	}
	if key == nil {
		return "", "", errors.New("signing key not found")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch h.Alg {
	case "RS256":
		if key.Kty != "RSA" {
			return "", "", errors.New("wrong RSA key type")
		}
		nb, e1 := decode(key.N)
		eb, e2 := decode(key.E)
		if e1 != nil || e2 != nil {
			return "", "", errors.New("invalid RSA key")
		}
		ev := 0
		for _, b := range eb {
			ev = ev<<8 | int(b)
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: ev}
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
			return "", "", errors.New("bad ID token signature")
		}
	case "ES256":
		if key.Kty != "EC" || key.Crv != "P-256" || len(sig) != 64 {
			return "", "", errors.New("wrong EC key or signature")
		}
		xb, e1 := decode(key.X)
		yb, e2 := decode(key.Y)
		if e1 != nil || e2 != nil {
			return "", "", errors.New("invalid EC key")
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(xb), Y: new(big.Int).SetBytes(yb)}
		if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			return "", "", errors.New("bad ID token signature")
		}
	default:
		return "", "", fmt.Errorf("unsupported ID token algorithm %q", h.Alg)
	}
	return c.Subject, strings.ToLower(strings.TrimSpace(c.Email)), nil
}

func hasAudience(raw any, want string) bool {
	switch a := raw.(type) {
	case string:
		return a == want
	case []any:
		for _, v := range a {
			if s, ok := v.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}
