package identity

import (
	"net/http"
	"testing"
	"time"
)

func TestSignAndVerify(t *testing.T) {
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	r, _ := http.NewRequest("GET", "http://x/", nil)
	r.Header.Set("X-Homelab-User", "forged")
	p := &Person{User: "labiba@example.com", Name: "Labiba Ö", Groups: []string{"role:owner"}}
	if err := s.Apply(r, "movies", "dev", p, now); err != nil {
		t.Fatal(err)
	}
	got, err := Verify(r.Header, s.PublicKey(), "movies", "dev", now.Add(time.Minute))
	if err != nil || got == nil || got.User != p.User || got.Name != p.Name || len(got.Groups) != 1 || got.Groups[0] != "role:owner" {
		t.Fatalf("verify = %+v, %v", got, err)
	}
	// Signed for one app, not valid for another or for prod.
	if _, err := Verify(r.Header, s.PublicKey(), "music", "dev", now); err == nil {
		t.Error("another app accepted the headers")
	}
	if _, err := Verify(r.Header, s.PublicKey(), "movies", "prod", now); err == nil {
		t.Error("prod accepted dev's headers")
	}
	if _, err := Verify(r.Header, s.PublicKey(), "movies", "dev", now.Add(10*time.Minute)); err == nil {
		t.Error("old signature accepted")
	}
	r.Header.Set(HeaderGroups, "role:owner,role:admin")
	if _, err := Verify(r.Header, s.PublicKey(), "movies", "dev", now); err == nil {
		t.Error("changed groups accepted")
	}
}

func TestApplyAnonymousStripsHeaders(t *testing.T) {
	key, _ := NewKey()
	s, _ := NewSigner(key)
	r, _ := http.NewRequest("GET", "http://x/", nil)
	r.Header.Set("X-Homelab-User", "forged")
	r.Header.Set("X-Homelab-Signature", "abc")
	if err := s.Apply(r, "movies", "dev", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(r.Header) != 0 {
		t.Fatalf("headers left: %v", r.Header)
	}
	if p, err := Verify(r.Header, s.PublicKey(), "movies", "dev", time.Now()); p != nil || err != nil {
		t.Fatalf("anonymous = %v, %v", p, err)
	}
}

func TestApplyRejectsLineBreaks(t *testing.T) {
	key, _ := NewKey()
	s, _ := NewSigner(key)
	r, _ := http.NewRequest("GET", "http://x/", nil)
	if err := s.Apply(r, "a", "dev", &Person{User: "a\nb"}, time.Now()); err == nil {
		t.Fatal("accepted a line break")
	}
}
