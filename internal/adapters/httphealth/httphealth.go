// Package httphealth probes an app's health endpoint with HTTP GET.
package httphealth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

const (
	defaultTimeout = 5 * time.Second
	maxBody        = 64 << 10 // read at most this much of a response and discard it
	maxRedirects   = 10
)

// Checker probes URLs with HTTP GET.
type Checker struct {
	client *http.Client
}

var _ core.HealthChecker = (*Checker)(nil)

// New returns a Checker that uses client. A nil client gets a 5 second timeout and does
// not follow redirects to other hosts.
func New(client *http.Client) *Checker {
	if client == nil {
		client = &http.Client{
			Timeout:       defaultTimeout,
			CheckRedirect: sameHostOnly,
		}
	}
	return &Checker{client: client}
}

// sameHostOnly follows redirects that stay on the original host. For any other host it
// stops and returns the redirect response itself, which is not 2xx and so counts as down.
func sameHostOnly(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("too many redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		return http.ErrUseLastResponse
	}
	return nil
}

// Check does a GET of target. It is OK when the response status is 2xx.
func (c *Checker) Check(ctx context.Context, target string) core.HealthResult {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return core.HealthResult{Detail: shortError(err), Latency: time.Since(start)}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return core.HealthResult{Detail: shortError(err), Latency: time.Since(start)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))

	return core.HealthResult{
		OK:      resp.StatusCode >= 200 && resp.StatusCode < 300,
		Code:    resp.StatusCode,
		Detail:  fmt.Sprintf("HTTP %d", resp.StatusCode),
		Latency: time.Since(start),
	}
}

// shortError turns a request error into a few readable words for the status page.
func shortError(err error) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "timed out"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection refused"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "no such host"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}
