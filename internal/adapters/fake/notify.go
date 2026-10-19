package fake

import (
	"context"
	"errors"
	"sync"

	"github.com/rupaai/homelab/internal/core"
)

// Notifier records pushes instead of sending them.
type Notifier struct {
	mu   sync.Mutex
	Sent []Push
	Gone map[string]bool // endpoints the push service no longer knows
}

// Push is one recorded notification.
type Push struct {
	Endpoint string
	core.Notification
}

func NewNotifier() *Notifier { return &Notifier{Gone: map[string]bool{}} }

func (n *Notifier) Send(_ context.Context, sub core.PushSubscription, msg core.Notification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Gone[sub.Endpoint] {
		return core.ErrGone
	}
	n.Sent = append(n.Sent, Push{Endpoint: sub.Endpoint, Notification: msg})
	return nil
}

// Titles returns the titles sent so far.
func (n *Notifier) Titles() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := []string{}
	for _, p := range n.Sent {
		out = append(out, p.Title)
	}
	return out
}

// Resources reports fixed numbers.
type Resources struct {
	mu     sync.Mutex
	Usage  core.DeviceUsage
	AppUse []core.AppUsage
}

func (r *Resources) Device(context.Context, string) (core.DeviceUsage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Usage, nil
}

func (r *Resources) Apps(context.Context) ([]core.AppUsage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.AppUsage{}, r.AppUse...), nil
}

// SetDisk changes the disk numbers.
func (r *Resources) SetDisk(used, total uint64) {
	r.mu.Lock()
	r.Usage.DiskUsed, r.Usage.DiskTotal = used, total
	r.mu.Unlock()
}

// System reports a fixed device.
type System struct{ Report core.SystemInfo }

func (s *System) Info(context.Context) (core.SystemInfo, error) { return s.Report, nil }

// Updater pretends to update Homelab.
type Updater struct {
	mu       sync.Mutex
	State    core.UpdateStatus
	Applied  int
	TestList []core.TestVersion
}

func (u *Updater) Status(context.Context) (core.UpdateStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.State, nil
}

func (u *Updater) Apply(context.Context) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.State.Available {
		return "", errors.New("already on the newest version")
	}
	u.Applied++
	return u.State.Latest, nil
}

func (u *Updater) Tests(context.Context) ([]core.TestVersion, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.TestList, nil
}

func (u *Updater) Try(_ context.Context, version string) (core.TestVersion, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, t := range u.TestList {
		if t.Version == version {
			u.State.Testing = &core.TestRun{TestVersion: t, From: u.State.Current}
			return t, nil
		}
	}
	return core.TestVersion{}, errors.New("no such test version")
}

func (u *Updater) GoBack(context.Context) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.State.Testing == nil {
		return "", errors.New("no test version is running")
	}
	from := u.State.Testing.From
	u.State.Testing = nil
	return from, nil
}

func (u *Updater) Keep(context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.State.Testing == nil {
		return errors.New("no test version is running")
	}
	u.State.Testing = nil
	return nil
}
