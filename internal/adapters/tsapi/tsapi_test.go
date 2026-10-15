package tsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

const testSecret = "tskey-client-SECRET-VALUE"

type fakeTS struct {
	t        *testing.T
	mu       sync.Mutex
	tokens   int
	handler  func(w http.ResponseWriter, r *http.Request, body []byte)
	requests []string
}

func newFake(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body []byte)) (*fakeTS, *Client, *time.Time) {
	t.Helper()
	f := &fakeTS{t: t, handler: h}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/api/v2/oauth/token" {
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			_ = r.ParseForm()
			if r.Form.Get("client_id") != "id" || r.Form.Get("client_secret") != testSecret || r.Form.Get("grant_type") != "client_credentials" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			f.tokens++
			_, _ = w.Write([]byte(`{"access_token":"tok` + string(rune('0'+f.tokens)) + `","expires_in":3600}`))
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.handler(w, r, body)
	}))
	t.Cleanup(srv.Close)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c, err := New(Options{ClientID: "id", ClientSecret: testSecret, BaseURL: srv.URL, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return f, c, &now
}

func TestNewNeedsCredentials(t *testing.T) {
	_, err := New(Options{ClientID: "id"})
	if err == nil || !strings.Contains(err.Error(), "Settings") {
		t.Fatalf("got %v", err)
	}
}

func TestTokenIsCachedAndRefreshed(t *testing.T) {
	var seen []string
	f, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		seen = append(seen, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"users":[]}`))
	})
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := c.RemoveUser(ctx, "a@b.c"); err != nil {
			t.Fatal(err)
		}
	}
	if f.tokens != 1 {
		t.Fatalf("token fetched %d times, want 1", f.tokens)
	}
	now = now.Add(2 * time.Hour)
	if err := c.RemoveUser(ctx, "a@b.c"); err != nil {
		t.Fatal(err)
	}
	if f.tokens != 2 || seen[2] != "Bearer tok2" || seen[0] != "Bearer tok1" {
		t.Fatalf("tokens=%d seen=%v", f.tokens, seen)
	}
}

func TestAuthKeyBody(t *testing.T) {
	_, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/tailnet/-/keys" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var in struct {
			Capabilities struct {
				Devices struct {
					Create struct {
						Reusable      bool     `json:"reusable"`
						Preauthorized bool     `json:"preauthorized"`
						Tags          []string `json:"tags"`
					} `json:"create"`
				} `json:"devices"`
			} `json:"capabilities"`
			ExpirySeconds int `json:"expirySeconds"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			t.Error(err)
		}
		cr := in.Capabilities.Devices.Create
		if cr.Reusable || !cr.Preauthorized || len(cr.Tags) != 1 || cr.Tags[0] != "tag:homelab" || in.ExpirySeconds != 3600 {
			t.Errorf("bad body: %s", body)
		}
		_, _ = w.Write([]byte(`{"key":"tskey-auth-xyz"}`))
	})
	key, err := c.AuthKey(context.Background(), []string{"tag:homelab"})
	if err != nil || key != "tskey-auth-xyz" {
		t.Fatalf("key=%q err=%v", key, err)
	}
	if _, err := c.AuthKey(context.Background(), nil); err == nil {
		t.Fatal("expected error without tags")
	}
}

func TestPolicyJSONGolden(t *testing.T) {
	got, err := PolicyJSON(core.AccessPolicy{
		Groups:    map[string][]string{"family": {"b@x.com", "a@x.com", "a@x.com"}, "guests": {}},
		TagOwners: []string{"tag:photos", "tag:files"},
		Grants:    []core.AccessGrant{{Groups: []string{"family"}, Tags: []string{"tag:photos"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "groups": {
    "group:family": [
      "a@x.com",
      "b@x.com"
    ],
    "group:guests": []
  },
  "tagOwners": {
    "tag:files": [
      "autogroup:admin",
      "tag:homelab"
    ],
    "tag:homelab": [
      "autogroup:admin"
    ],
    "tag:photos": [
      "autogroup:admin",
      "tag:homelab"
    ]
  },
  "grants": [
    {
      "src": [
        "autogroup:admin"
      ],
      "dst": [
        "tag:files",
        "tag:photos"
      ],
      "ip": [
        "*"
      ]
    },
    {
      "src": [
        "group:family"
      ],
      "dst": [
        "tag:photos"
      ],
      "ip": [
        "*"
      ]
    }
  ],
  "ssh": []
}`
	if string(got) != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestPolicyJSONRejectsBadNames(t *testing.T) {
	bad := []core.AccessPolicy{
		{Groups: map[string][]string{"Family": nil}},
		{Groups: map[string][]string{"a b": nil}},
		{TagOwners: []string{"photos"}},
		{TagOwners: []string{"tag:Photos"}},
		{TagOwners: []string{"tag:"}},
		{TagOwners: []string{"tag:a"}, Grants: []core.AccessGrant{{Groups: []string{"nope"}, Tags: []string{"tag:a"}}}},
		{TagOwners: []string{"tag:a"}, Grants: []core.AccessGrant{{Groups: nil, Tags: []string{"tag:b"}}}},
	}
	for i, p := range bad {
		if _, err := PolicyJSON(p); err == nil {
			t.Errorf("case %d: expected an error", i)
		}
	}
}

var testPolicy = core.AccessPolicy{
	Groups:    map[string][]string{"family": {"a@x.com"}},
	TagOwners: []string{"tag:photos"},
	Grants:    []core.AccessGrant{{Groups: []string{"family"}, Tags: []string{"tag:photos"}}},
}

func TestSetPolicySendsIfMatchAndRetries(t *testing.T) {
	gets, posts := 0, 0
	want, _ := PolicyJSON(testPolicy)
	_, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.Method {
		case http.MethodGet:
			gets++
			if r.Header.Get("Accept") != "application/json" {
				t.Error("missing Accept header")
			}
			w.Header().Set("ETag", `"etag`+string(rune('0'+gets))+`"`)
			_, _ = w.Write([]byte(`{"acls":[]}`))
		case http.MethodPost:
			posts++
			if got, wantTag := r.Header.Get("If-Match"), `"etag`+string(rune('0'+gets))+`"`; got != wantTag {
				t.Errorf("If-Match %q, want %q", got, wantTag)
			}
			if posts == 1 {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			if string(body) != string(want) {
				t.Errorf("body mismatch: %s", body)
			}
		}
	})
	if err := c.SetPolicy(context.Background(), testPolicy); err != nil {
		t.Fatal(err)
	}
	if gets != 2 || posts != 2 {
		t.Fatalf("gets=%d posts=%d", gets, posts)
	}
}

func TestSetPolicyGivesUpAfterSecond412(t *testing.T) {
	_, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.SetPolicy(context.Background(), testPolicy); err == nil {
		t.Fatal("expected error")
	}
}

func TestSetPolicySkipsWhenUnchanged(t *testing.T) {
	want, _ := PolicyJSON(testPolicy)
	// Same document, different formatting.
	var v any
	_ = json.Unmarshal(want, &v)
	compact, _ := json.Marshal(v)
	f, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodPost {
			t.Error("should not POST an unchanged policy")
		}
		w.Header().Set("ETag", `"e"`)
		_, _ = w.Write(compact)
	})
	if err := c.SetPolicy(context.Background(), testPolicy); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 1 {
		t.Fatalf("requests: %v", f.requests)
	}
}

func TestObserve(t *testing.T) {
	_, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch r.URL.Path {
		case "/api/v2/tailnet/-/users":
			_, _ = w.Write([]byte(`{"users":[{"id":"u1","loginName":"a@x.com","displayName":"Ada","status":"active"}]}`))
		case "/api/v2/tailnet/-/devices":
			_, _ = w.Write([]byte(`{"devices":[
{"id":"1","nodeId":"nABC","name":"phone.ts.net","user":"a@x.com","tags":[],"lastSeen":"2026-01-01T10:00:00Z","connectedToControl":true},
{"id":"2","name":"photos.ts.net","user":"a@x.com","tags":["tag:photos"],"connectedToControl":false}]}`))
		case "/api/v2/tailnet/-/user-invites":
			_, _ = w.Write([]byte(`[{"id":"i1","inviteUrl":"https://login.tailscale.com/uinv/x","email":"b@x.com"}]`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	st, err := c.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Users) != 1 || st.Users[0] != (core.RemoteUser{ID: "u1", Login: "a@x.com", Name: "Ada"}) {
		t.Fatalf("users: %+v", st.Users)
	}
	if len(st.Devices) != 2 {
		t.Fatalf("devices: %+v", st.Devices)
	}
	p, h := st.Devices[0], st.Devices[1]
	if p.ID != "nABC" || p.Login != "a@x.com" || !p.Online || p.LastSeen == nil || !p.LastSeen.Equal(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("phone: %+v", p)
	}
	if h.ID != "2" || h.Login != "" || h.Online || h.LastSeen != nil || len(h.Tags) != 1 {
		t.Fatalf("tagged: %+v", h)
	}
	if len(st.Invites) != 1 || st.Invites[0].ID != "i1" || !strings.HasPrefix(st.Invites[0].URL, "https://") {
		t.Fatalf("invites: %+v", st.Invites)
	}
}

func TestInviteAndCancel(t *testing.T) {
	var deleted string
	_, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.Method {
		case http.MethodPost:
			if strings.TrimSpace(string(body)) != `[{"email":"b@x.com","role":"member"}]` {
				t.Errorf("body: %s", body)
			}
			_, _ = w.Write([]byte(`[{"id":"i1","inviteUrl":"https://login.tailscale.com/uinv/x"}]`))
		case http.MethodDelete:
			deleted = r.URL.Path
		}
	})
	inv, err := c.Invite(context.Background(), "b@x.com")
	if err != nil || inv.ID != "i1" || inv.URL == "" {
		t.Fatalf("inv=%+v err=%v", inv, err)
	}
	if err := c.CancelInvite(context.Background(), "i1"); err != nil || deleted != "/api/v2/user-invites/i1" {
		t.Fatalf("deleted=%q err=%v", deleted, err)
	}
}

func TestInviteWithoutURLFails(t *testing.T) {
	_, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) { _, _ = w.Write([]byte(`[]`)) })
	if _, err := c.Invite(context.Background(), "b@x.com"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRemoveDevicesOnlyThatPersonsUntagged(t *testing.T) {
	var deleted []string
	_, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodDelete {
			deleted = append(deleted, r.URL.Path)
			if r.URL.Path == "/api/v2/device/bad" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
			}
			return
		}
		_, _ = w.Write([]byte(`{"devices":[
{"id":"1","nodeId":"n1","name":"a-phone","user":"Ada@X.com","tags":[]},
{"id":"2","name":"a-laptop","user":"ada@x.com"},
{"id":"3","name":"photos","user":"ada@x.com","tags":["tag:photos"]},
{"id":"4","name":"b-phone","user":"bob@x.com"},
{"id":"bad","name":"a-tablet","user":"ada@x.com"}]}`))
	})
	n, err := c.RemoveDevices(context.Background(), "ada@x.com")
	if n != 2 {
		t.Fatalf("removed %d", n)
	}
	if err == nil || !strings.Contains(err.Error(), "a-tablet") {
		t.Fatalf("err=%v", err)
	}
	want := []string{"/api/v2/device/n1", "/api/v2/device/2", "/api/v2/device/bad"}
	if strings.Join(deleted, ",") != strings.Join(want, ",") {
		t.Fatalf("deleted %v", deleted)
	}
}

func TestRemoveUser(t *testing.T) {
	var posted []string
	_, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodPost {
			posted = append(posted, r.URL.Path)
			return
		}
		_, _ = w.Write([]byte(`{"users":[{"id":"u1","loginName":"Ada@x.com"}]}`))
	})
	ctx := context.Background()
	if err := c.RemoveUser(ctx, "nobody@x.com"); err != nil || len(posted) != 0 {
		t.Fatalf("not found: err=%v posted=%v", err, posted)
	}
	if err := c.RemoveUser(ctx, "ada@x.com"); err != nil || len(posted) != 1 || posted[0] != "/api/v2/users/u1/delete" {
		t.Fatalf("err=%v posted=%v", err, posted)
	}
}

func TestErrorsAreFriendlyAndKeepSecrets(t *testing.T) {
	status := http.StatusForbidden
	_, c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"nope"}`))
	})
	ctx := context.Background()
	_, err := c.AuthKey(ctx, []string{"tag:a"})
	if err == nil || !strings.Contains(err.Error(), "Settings") || !strings.Contains(err.Error(), "auth_keys") {
		t.Fatalf("403: %v", err)
	}
	_, err = c.Observe(ctx)
	if err == nil || !strings.Contains(err.Error(), "users") {
		t.Fatalf("scope: %v", err)
	}
	status = http.StatusTooManyRequests
	if err = c.RemoveUser(ctx, "a@b.c"); err == nil || !strings.Contains(err.Error(), "slow down") {
		t.Fatalf("429: %v", err)
	}
	status = http.StatusBadRequest
	if err = c.RemoveUser(ctx, "a@b.c"); err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("400: %v", err)
	}
	for _, e := range []error{err} {
		if strings.Contains(e.Error(), testSecret) || strings.Contains(e.Error(), "tok1") {
			t.Fatalf("error leaks a credential: %v", e)
		}
	}
}

func TestBadCredentialsAtSignIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	c, _ := New(Options{ClientID: "id", ClientSecret: testSecret, BaseURL: srv.URL})
	_, err := c.AuthKey(context.Background(), []string{"tag:a"})
	if err == nil || !strings.Contains(err.Error(), "Settings") || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("err=%v", err)
	}
}
