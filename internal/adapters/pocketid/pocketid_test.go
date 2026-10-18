package pocketid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

func TestEnsureReturnsAndReusesCanonicalProviderID(t *testing.T) {
	var userCreated bool
	client, err := New(Options{BaseURL: "http://pocket-id.test", APIKey: "test-key", HTTPClient: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			status, body := http.StatusOK, `{"data":[{"id":"group-owner","name":"role:owner"}]}`
			switch {
			case req.Method == http.MethodGet && req.URL.Path == "/api/user-groups":
			case req.Method == http.MethodGet && req.URL.Path == "/api/users/local-person":
				status, body = http.StatusNotFound, `{"error":"not found"}`
			case req.Method == http.MethodGet && req.URL.Path == "/api/users":
				body = `{"data":[]}`
			case req.Method == http.MethodGet && req.URL.Path == "/api/users":
				body = `{"data":[]}`
			case req.Method == http.MethodPost && req.URL.Path == "/api/users":
				userCreated = true
				status, body = http.StatusCreated, `{"id":"provider-generated-id","username":"homelab-local-person"}`
			case req.Method == http.MethodGet && req.URL.Path == "/api/users/provider-generated-id":
				if !userCreated {
					t.Fatal("looked up provider id before creating user")
				}
				body = `{"id":"provider-generated-id","username":"homelab-local-person"}`
			case req.Method == http.MethodPut && req.URL.Path == "/api/users/provider-generated-id":
				body = `{"id":"provider-generated-id","username":"homelab-local-person"}`
			default:
				t.Fatalf("unexpected Pocket ID request: %s %s", req.Method, req.URL)
			}
			return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	p := core.Person{ID: "local-person", Name: "Sam", Email: "sam@example.test"}
	id, err := client.Ensure(context.Background(), p, []string{"role:owner"})
	if err != nil || id != "provider-generated-id" {
		t.Fatalf("Ensure id=%q err=%v", id, err)
	}
	p.IdentityID = id
	id, err = client.Ensure(context.Background(), p, []string{"role:owner"})
	if err != nil || id != "provider-generated-id" {
		t.Fatalf("second Ensure id=%q err=%v", id, err)
	}
}

func TestLoginLinkRequestsLongOneTimeAccessToken(t *testing.T) {
	client, err := New(Options{BaseURL: "http://pocket-id.test", APIKey: "test-key", HTTPClient: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost || req.URL.Path != "/api/users/provider-id/one-time-access-token" {
				t.Fatalf("unexpected Pocket ID request: %s %s", req.Method, req.URL)
			}
			var body map[string]int64
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if body["ttl"] != 3600 {
				t.Fatalf("ttl = %d, want 3600 seconds to request a long login code", body["ttl"])
			}
			return &http.Response{StatusCode: http.StatusCreated, Status: "201 Created", Body: io.NopCloser(strings.NewReader(`{"token":"123456789012"}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}

	link, err := client.LoginLink(context.Background(), "provider-id", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if link != "http://pocket-id.test/lc/123456789012" {
		t.Fatalf("LoginLink = %q, want long one-time access link", link)
	}
}

func TestEnsureDeletesDisabledHomelabAccountBeforeReinvite(t *testing.T) {
	client, err := New(Options{BaseURL: "http://pocket-id.test", APIKey: "test-key", HTTPClient: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			status, body := http.StatusOK, `{"data":[{"id":"group-member","name":"role:member"}]}`
			switch {
			case req.Method == http.MethodGet && req.URL.Path == "/api/user-groups":
			case req.Method == http.MethodGet && req.URL.Path == "/api/users/new-person":
				status, body = http.StatusNotFound, `{"error":"not found"}`
			case req.Method == http.MethodGet && req.URL.Path == "/api/users":
				if req.URL.Query().Get("search") != "return@example.test" {
					t.Fatalf("email search = %q", req.URL.RawQuery)
				}
				body = `{"data":[{"id":"directory-user","username":"directory-user","email":"return@example.test"},{"id":"old-provider-id","username":"homelab-old-person","email":"return@example.test","disabled":true}]}`
			case req.Method == http.MethodDelete && req.URL.Path == "/api/users/old-provider-id":
			case req.Method == http.MethodPost && req.URL.Path == "/api/users":
				var created map[string]any
				if err := json.NewDecoder(req.Body).Decode(&created); err != nil {
					t.Fatalf("decode new account request: %v", err)
				}
				if created["disabled"] != false || created["username"] != "homelab-new-person" || created["email"] != "return@example.test" {
					t.Fatalf("incorrect new account request: %#v", created)
				}
				status, body = http.StatusCreated, `{"id":"new-provider-id","username":"homelab-new-person","email":"return@example.test","disabled":false}`
			default:
				t.Fatalf("unexpected Pocket ID request: %s %s", req.Method, req.URL)
			}
			return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := client.Ensure(context.Background(), core.Person{ID: "new-person", Name: "Returning person", Email: "return@example.test"}, []string{"role:member"})
	if err != nil || id != "new-provider-id" {
		t.Fatalf("Ensure id=%q err=%v, want newly created provider id", id, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
