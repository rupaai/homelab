package fake

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Identity is an in-memory sign-in provider for hub tests.
type Identity struct {
	mu           sync.Mutex
	Users        map[string]core.IdentityUser
	IDs          map[string]string
	CanonicalID  string
	LoginLinkTTL time.Duration
	// LoginLinkCalls counts one-time links issued; each call is a new provider token.
	LoginLinkCalls int
	// EnsureCalls counts calls to Ensure.
	EnsureCalls int
	Fail        bool
}

func NewIdentity() *Identity {
	return &Identity{Users: map[string]core.IdentityUser{}, IDs: map[string]string{}}
}
func (i *Identity) Ensure(_ context.Context, p core.Person, groups []string) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.EnsureCalls++
	if i.Fail {
		return "", errors.New("identity provider is unavailable")
	}
	id := p.IdentityID
	if id == "" {
		id = i.IDs[p.ID]
		if id == "" {
			id = i.CanonicalID
		}
		if id == "" {
			id = p.ID
		}
	}
	i.Users[id] = core.IdentityUser{ID: id, Email: p.Email, Name: p.Name, Groups: append([]string(nil), groups...)}
	return id, nil
}
func (i *Identity) Disable(_ context.Context, id string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.Fail {
		return errors.New("identity provider is unavailable")
	}
	u, ok := i.Users[id]
	if ok {
		u.Disabled = true
		i.Users[id] = u
	}
	return nil
}
func (i *Identity) LoginLink(_ context.Context, id string, ttl time.Duration) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.Fail {
		return "", errors.New("identity provider is unavailable")
	}
	i.LoginLinkTTL = ttl
	i.LoginLinkCalls++
	return "http://localhost:1411/login/" + id, nil
}
func (i *Identity) Observe(context.Context) ([]core.IdentityUser, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.Fail {
		return nil, errors.New("identity provider is unavailable")
	}
	out := make([]core.IdentityUser, 0, len(i.Users))
	for _, u := range i.Users {
		u.Groups = append([]string(nil), u.Groups...)
		out = append(out, u)
	}
	return out, nil
}

// Email discards messages for tests and offline households.
type Email struct{}

func (Email) Send(context.Context, string, string, string) error { return nil }
