package pocketid

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
