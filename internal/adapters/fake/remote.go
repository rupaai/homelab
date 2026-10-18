package fake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rupaai/homelab/internal/core"
)

// Remote is an in-memory tailnet for hub tests.
type Remote struct {
	mu       sync.Mutex
	Policy   *core.AccessPolicy
	Policies int               // how many times the policy was sent
	Invites  map[string]string // invite id -> email
	State    core.RemoteState
	Keys     [][]string // tags of each auth key minted
	Fail     bool
	next     int
}

func NewRemote() *Remote { return &Remote{Invites: map[string]string{}} }

var errRemote = errors.New("tailnet unavailable")

func (r *Remote) Invite(_ context.Context, email string) (core.RemoteInvite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Fail {
		return core.RemoteInvite{}, errRemote
	}
	r.next++
	id := fmt.Sprintf("inv%d", r.next)
	r.Invites[id] = email
	return core.RemoteInvite{ID: id, URL: "https://login.tailscale.com/uinv/" + id}, nil
}

func (r *Remote) CancelInvite(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Fail {
		return errRemote
	}
	delete(r.Invites, id)
	return nil
}

func (r *Remote) SetPolicy(_ context.Context, p core.AccessPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Fail {
		return errRemote
	}
	r.Policy = &p
	r.Policies++
	return nil
}

func (r *Remote) AuthKey(_ context.Context, tags []string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Fail {
		return "", errRemote
	}
	r.Keys = append(r.Keys, append([]string{}, tags...))
	return "tskey-auth-fake", nil
}

func (r *Remote) Observe(context.Context) (core.RemoteState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Fail {
		return core.RemoteState{}, errRemote
	}
	return r.State, nil
}

func (r *Remote) RemoveDevices(_ context.Context, login string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Fail {
		return 0, errRemote
	}
	kept, n := r.State.Devices[:0], 0
	for _, d := range r.State.Devices {
		if len(d.Tags) == 0 && strings.EqualFold(d.Login, login) {
			n++
			continue
		}
		kept = append(kept, d)
	}
	r.State.Devices = kept
	return n, nil
}

func (r *Remote) RemoveUser(_ context.Context, login string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Fail {
		return errRemote
	}
	for i, u := range r.State.Users {
		if strings.EqualFold(u.Login, login) {
			r.State.Users = append(r.State.Users[:i], r.State.Users[i+1:]...)
			return nil
		}
	}
	return core.ErrNotFound
}

// Groups returns a copy of the members of a policy group.
func (r *Remote) Groups(name string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Policy == nil {
		return nil
	}
	return append([]string{}, r.Policy.Groups[name]...)
}
