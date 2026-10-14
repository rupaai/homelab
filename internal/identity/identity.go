// Package identity signs the headers that tell an app who is using it (A2). Homelab's proxy
// is the only way into an app, and it adds these headers to every request it forwards:
//
//	X-Homelab-User       login, e.g. labiba@example.com, or "owner" on this computer
//	X-Homelab-Name       display name, percent-encoded UTF-8
//	X-Homelab-Groups     comma-separated, e.g. role:owner
//	X-Homelab-Time       when the proxy signed it, Unix seconds
//	X-Homelab-Signature  base64 (standard, padded) Ed25519 signature of Payload
//
// Apps get Homelab's public key as HOMELAB_IDENTITY_KEY ("ed25519:" + base64 of the 32-byte
// key) and verify the signature, the app and env it was signed for, and that the time is
// within MaxSkew. Holding only the public key, one app can't forge identities for another.
// Requests from people Homelab doesn't know carry none of these headers.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Header names.
const (
	HeaderUser      = "X-Homelab-User"
	HeaderName      = "X-Homelab-Name"
	HeaderGroups    = "X-Homelab-Groups"
	HeaderTime      = "X-Homelab-Time"
	HeaderSignature = "X-Homelab-Signature"

	// EnvPublicKey is the environment variable that carries the public key to apps.
	EnvPublicKey = "HOMELAB_IDENTITY_KEY"
	// MaxSkew is how far the signing time may be from the app's clock.
	MaxSkew = 5 * time.Minute

	version   = "homelab-identity-v1"
	keyPrefix = "ed25519:"
)

// Person is who a request is from.
type Person struct {
	User   string
	Name   string
	Groups []string
}

// Payload is the exact text that is signed. Every field is on its own line, so no value
// can be shifted into another; values can't contain line breaks (Sign refuses them).
func Payload(app, env string, p Person, at int64) string {
	return strings.Join([]string{version, app, env, p.User, p.Name, strings.Join(p.Groups, ","), strconv.FormatInt(at, 10)}, "\n")
}

// Signer signs identity headers with Homelab's private key.
type Signer struct{ key ed25519.PrivateKey }

// NewKey makes a new private key, encoded for storing as a secret.
func NewKey() (string, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(priv.Seed()), nil
}

// NewSigner loads a key made by NewKey.
func NewSigner(encoded string) (*Signer, error) {
	seed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("identity key is not a valid Ed25519 seed")
	}
	return &Signer{key: ed25519.NewKeyFromSeed(seed)}, nil
}

// PublicKey is the value apps get in HOMELAB_IDENTITY_KEY.
func (s *Signer) PublicKey() string {
	return keyPrefix + base64.StdEncoding.EncodeToString(s.key.Public().(ed25519.PublicKey))
}

// Apply removes any identity headers already on r and, when p is not nil, adds signed ones
// for app/env.
func (s *Signer) Apply(r *http.Request, app, env string, p *Person, now time.Time) error {
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-homelab-") {
			r.Header.Del(k)
		}
	}
	if p == nil {
		return nil
	}
	for _, v := range append([]string{p.User, p.Name}, p.Groups...) {
		if strings.ContainsAny(v, "\r\n") {
			return errors.New("identity values can't contain line breaks")
		}
	}
	for _, g := range p.Groups {
		if strings.Contains(g, ",") {
			return fmt.Errorf("group %q can't contain a comma", g)
		}
	}
	at := now.Unix()
	sig := ed25519.Sign(s.key, []byte(Payload(app, env, *p, at)))
	r.Header.Set(HeaderUser, p.User)
	r.Header.Set(HeaderName, url.PathEscape(p.Name))
	r.Header.Set(HeaderGroups, strings.Join(p.Groups, ","))
	r.Header.Set(HeaderTime, strconv.FormatInt(at, 10))
	r.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	return nil
}

// Verify checks a request's identity headers the way an app should, and returns the
// person. It returns (nil, nil) when the request carries no identity. Homelab uses it in
// tests; it is also the reference for the template's helpers.
func Verify(h http.Header, publicKey, app, env string, now time.Time) (*Person, error) {
	sig := h.Get(HeaderSignature)
	if sig == "" {
		return nil, nil
	}
	if !strings.HasPrefix(publicKey, keyPrefix) {
		return nil, errors.New("public key must start with " + keyPrefix)
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(publicKey, keyPrefix))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("public key is not a valid Ed25519 key")
	}
	rawSig, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return nil, errors.New("signature is not base64")
	}
	at, err := strconv.ParseInt(h.Get(HeaderTime), 10, 64)
	if err != nil {
		return nil, errors.New("time is missing")
	}
	if d := now.Sub(time.Unix(at, 0)); d > MaxSkew || d < -MaxSkew {
		return nil, errors.New("signature is too old or from the future")
	}
	name, err := url.PathUnescape(h.Get(HeaderName))
	if err != nil {
		return nil, errors.New("name is not percent-encoded")
	}
	p := Person{User: h.Get(HeaderUser), Name: name}
	if g := h.Get(HeaderGroups); g != "" {
		p.Groups = strings.Split(g, ",")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(Payload(app, env, p, at)), rawSig) {
		return nil, errors.New("signature doesn't match")
	}
	return &p, nil
}
