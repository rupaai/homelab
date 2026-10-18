// Package webpush sends push notifications to the companion on people's devices (web push
// with VAPID, RFC 8030/8291/8292), as core.Notifier.
package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	wp "github.com/SherClockHolmes/webpush-go"

	"github.com/rupaai/homelab/internal/core"
)

var _ core.Notifier = (*Pusher)(nil)

// Options configures a Pusher. The keys come from GenerateKeys and are kept as secrets.
type Options struct {
	PublicKey  string
	PrivateKey string
	// Subscriber identifies who sends the pushes to the push services: an https URL or an email.
	Subscriber string
	HTTPClient *http.Client
}

// Pusher is the web push core.Notifier.
type Pusher struct{ o Options }

// GenerateKeys makes a new VAPID key pair (base64url, as browsers expect).
func GenerateKeys() (privateKey, publicKey string, err error) { return wp.GenerateVAPIDKeys() }

// New creates a Pusher.
func New(o Options) (*Pusher, error) {
	if o.PublicKey == "" || o.PrivateKey == "" {
		return nil, errors.New("webpush: keys are missing")
	}
	if o.Subscriber == "" {
		o.Subscriber = "https://github.com/rupaai/homelab"
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Pusher{o: o}, nil
}

// Send delivers n to one device. A subscription the push service no longer knows gives core.ErrGone.
func (p *Pusher) Send(ctx context.Context, sub core.PushSubscription, n core.Notification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return err
	}
	resp, err := wp.SendNotificationWithContext(ctx, body, &wp.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     wp.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &wp.Options{
		HTTPClient:      p.o.HTTPClient,
		Subscriber:      p.o.Subscriber,
		VAPIDPublicKey:  p.o.PublicKey,
		VAPIDPrivateKey: p.o.PrivateKey,
		TTL:             24 * 60 * 60,
		Urgency:         wp.UrgencyHigh,
		Topic:           topic(n.Tag),
	})
	if err != nil {
		return fmt.Errorf("couldn't reach the push service: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return core.ErrGone
	case resp.StatusCode >= 300:
		return fmt.Errorf("the push service refused the notification (%s)", resp.Status)
	}
	return nil
}

// topic turns a tag into a push Topic (at most 32 URL-safe characters), so a newer push
// replaces an undelivered older one with the same tag.
func topic(tag string) string {
	out := make([]byte, 0, 32)
	for i := 0; i < len(tag) && len(out) < 32; i++ {
		c := tag[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			out = append(out, c)
		}
	}
	return string(out)
}
