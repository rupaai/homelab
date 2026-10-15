package fake

import (
	"context"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Identity is an in-memory sign-in provider for hub tests.
type Identity struct {
	mu    sync.Mutex
	Users map[string]core.IdentityUser
}

func NewIdentity() *Identity { return &Identity{Users: map[string]core.IdentityUser{}} }
func (i *Identity) Ensure(_ context.Context, p core.Person, groups []string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.Users[p.ID] = core.IdentityUser{ID: p.ID, Email: p.Email, Name: p.Name, Groups: append([]string(nil), groups...)}
	return nil
}
func (i *Identity) Disable(_ context.Context, id string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	u, ok := i.Users[id]
	if ok {
		u.Disabled = true
		i.Users[id] = u
	}
	return nil
}
func (i *Identity) LoginLink(_ context.Context, id string, _ time.Duration) (string, error) {
	return "http://localhost:1411/login/" + id, nil
}
func (i *Identity) Observe(context.Context) ([]core.IdentityUser, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
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
