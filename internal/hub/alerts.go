package hub

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

// Alerts (milestone 5, O5 and O12) are push notifications to the people who can act: builders
// of an app and admins for the app's problems, admins alone for the device. Each incident is
// told once and kept in state, so a restart doesn't repeat it.

const (
	downAfter     = 2 * time.Minute // prod must be down this long before anyone is woken
	diskFullAt    = 0.85
	diskEvery     = 5 * time.Minute
	alertDiskFull = "disk:full"
)

// checkAlerts looks at prod health and the disk, and tells people about new and ended problems.
func (h *Hub) checkAlerts(ctx context.Context) {
	if h.ad.Notifier == nil {
		return
	}
	apps, err := h.store.Apps(ctx)
	if err != nil {
		return
	}
	now := h.cfg.Now()
	for _, a := range apps {
		e, err := h.store.Env(ctx, a.ID, core.Prod)
		if err != nil || e.Want == nil {
			continue
		}
		h.mu.Lock()
		s := h.st(a.ID, core.Prod)
		health, detail, since := s.health, s.detail, s.since
		h.mu.Unlock()
		key := "down:" + a.ID
		switch health {
		case core.Down, core.Degraded:
			started, sent, err := h.store.OpenAlert(ctx, key, since)
			if err != nil || sent || now.Sub(started) < downAfter {
				continue
			}
			h.notify(ctx, h.appAudience(ctx, a.ID), core.Notification{
				Title: a.Name + " is down",
				Body:  fmt.Sprintf("%s has been down since %s. %s", a.Name, started.Local().Format("15:04"), detail),
				URL:   "/apps/" + a.ID, Tag: key,
			})
			_ = h.store.MarkAlertSent(ctx, key, now)
		case core.Healthy:
			if told, err := h.store.CloseAlert(ctx, key); err == nil && told {
				h.notify(ctx, h.appAudience(ctx, a.ID), core.Notification{
					Title: a.Name + " is back", Body: a.Name + " is running and healthy again.", URL: "/apps/" + a.ID, Tag: key,
				})
			}
		}
	}
	h.checkDisk(ctx)
}

func (h *Hub) checkDisk(ctx context.Context) {
	if h.ad.Resources == nil {
		return
	}
	h.mu.Lock()
	due := h.cfg.Now().Sub(h.diskCheckedAt) >= diskEvery
	if due {
		h.diskCheckedAt = h.cfg.Now()
	}
	h.mu.Unlock()
	if !due {
		return
	}
	u, err := h.ad.Resources.Device(ctx, h.cfg.DataDir)
	if err != nil || u.DiskTotal == 0 {
		return
	}
	full := float64(u.DiskUsed) / float64(u.DiskTotal)
	if full < diskFullAt-0.05 { // a little slack, so hovering around 85% doesn't flap
		_, _ = h.store.CloseAlert(ctx, alertDiskFull)
		return
	}
	if full < diskFullAt {
		return
	}
	_, sent, err := h.store.OpenAlert(ctx, alertDiskFull, h.cfg.Now())
	if err != nil || sent {
		return
	}
	h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, Action: "disk.full", Detail: fmt.Sprintf("The disk is %.0f%% full", full*100)})
	h.notify(ctx, h.adminAudience(ctx), core.Notification{
		Title: "The disk is almost full",
		Body:  fmt.Sprintf("The disk Homelab keeps its data on is %.0f%% full. Remove old apps or backups before it runs out.", full*100),
		URL:   "/resources", Tag: alertDiskFull,
	})
	_ = h.store.MarkAlertSent(ctx, alertDiskFull, h.cfg.Now())
}

// alertRollback tells people prod went back on its own.
func (h *Hub) alertRollback(ctx context.Context, a state.App, from, to core.Release) {
	h.notify(ctx, h.appAudience(ctx, a.ID), core.Notification{
		Title: a.Name + " was rolled back",
		Body:  fmt.Sprintf("%s turned unhealthy after it was promoted, so prod went back to %s.", from.Short(), to.Short()),
		URL:   "/apps/" + a.ID, Tag: "rollback:" + a.ID,
	})
}

// appAudience is everyone who can act on an app: its builders and the admins.
func (h *Hub) appAudience(ctx context.Context, app string) []string {
	people, err := h.store.People(ctx)
	if err != nil {
		return nil
	}
	var ids []string
	for _, p := range people {
		if Can(p, ActionProjects, app) {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

func (h *Hub) adminAudience(ctx context.Context) []string {
	people, err := h.store.People(ctx)
	if err != nil {
		return nil
	}
	var ids []string
	for _, p := range people {
		if Can(p, ActionManage, "") {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// notify pushes n to every subscribed device of the given people. Devices that unsubscribed
// are forgotten.
func (h *Hub) notify(ctx context.Context, people []string, n core.Notification) int {
	if h.ad.Notifier == nil || len(people) == 0 {
		return 0
	}
	devices, err := h.store.PushDevices(ctx, people)
	if err != nil {
		return 0
	}
	sent := 0
	for _, d := range devices {
		err := h.ad.Notifier.Send(ctx, d.Sub, n)
		switch {
		case err == nil:
			sent++
		case errors.Is(err, core.ErrGone):
			_ = h.store.DeletePushSubscription(ctx, "", d.Sub.Endpoint)
		default:
			h.log.Warn("push notification", "person", d.PersonID, "err", err)
		}
	}
	return sent
}

// SubscribePush saves a device that wants notifications for person.
func (h *Hub) SubscribePush(ctx context.Context, personID, device string, sub core.PushSubscription) error {
	if h.ad.Notifier == nil {
		return userErr("Notifications aren't set up on this Homelab.")
	}
	if sub.Endpoint == "" || sub.P256dh == "" || sub.Auth == "" || len(sub.Endpoint) > 2048 || len(device) > 100 {
		return userErr("Your browser didn't give Homelab what it needs for notifications. Try again.")
	}
	if !validPushEndpoint(sub.Endpoint) {
		return userErr("That notification address isn't one Homelab can send to.")
	}
	return h.store.SavePushSubscription(ctx, personID, device, sub)
}

// UnsubscribePush forgets one of person's devices.
func (h *Hub) UnsubscribePush(ctx context.Context, personID, endpoint string) error {
	return h.store.DeletePushSubscription(ctx, personID, endpoint)
}

// TestPush sends person a notification on every device they turned notifications on.
func (h *Hub) TestPush(ctx context.Context, personID string) (int, error) {
	if h.ad.Notifier == nil {
		return 0, userErr("Notifications aren't set up on this Homelab.")
	}
	n := h.notify(ctx, []string{personID}, core.Notification{Title: "Homelab", Body: "Notifications work on this device.", URL: "/me", Tag: "test"})
	if n == 0 {
		return 0, userErr("No device got the test. Turn notifications on again on this device.")
	}
	return n, nil
}

// pushServices are the browsers' push services. Only these are sent to, so Homelab can't be
// made to post to addresses on its own network.
var pushServices = []string{".googleapis.com", ".push.apple.com", ".mozilla.com", ".notify.windows.com"}

func validPushEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range pushServices {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// PushPublicKey is the key browsers need to subscribe to Homelab's notifications ("" when off).
func (h *Hub) PushPublicKey() string {
	if h.ad.Notifier == nil {
		return ""
	}
	return h.cfg.PushPublicKey
}

// ResourcesView is the device's and each app's current usage.
type ResourcesView struct {
	Device   *core.DeviceUsage `json:"device,omitempty"`
	Apps     []AppUsageView    `json:"apps"`
	Problems []string          `json:"problems,omitempty"`
}

// AppUsageView is one app environment's usage, with the app's name.
type AppUsageView struct {
	core.AppUsage
	Name string `json:"name"`
}

// Resources reports current usage. A part that can't be read is left out and explained.
func (h *Hub) Resources(ctx context.Context) (ResourcesView, error) {
	v := ResourcesView{Apps: []AppUsageView{}}
	if h.ad.Resources == nil {
		return v, userErr("Resource numbers aren't available on this Homelab.")
	}
	if d, err := h.ad.Resources.Device(ctx, h.cfg.DataDir); err == nil {
		v.Device = &d
	} else {
		v.Problems = append(v.Problems, "Couldn't read the device's numbers: "+err.Error())
	}
	usage, err := h.ad.Resources.Apps(ctx)
	if err != nil {
		v.Problems = append(v.Problems, err.Error())
	}
	names := map[string]string{}
	if apps, err := h.store.Apps(ctx); err == nil {
		for _, a := range apps {
			names[a.ID] = a.Name
		}
	}
	for _, u := range usage {
		v.Apps = append(v.Apps, AppUsageView{AppUsage: u, Name: firstNonEmpty(names[u.App], u.App)})
	}
	return v, nil
}
