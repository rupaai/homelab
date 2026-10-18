package fake

import (
	"context"
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
