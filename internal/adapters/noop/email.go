// Package noop contains optional adapters that intentionally do nothing.
package noop

import (
	"context"
	"log/slog"

	"github.com/rupaai/homelab/internal/core"
)

// Email is the default offline email adapter. Link-based invites remain available.
type Email struct{}

// Send deliberately does nothing. TODO: add Resend and SMTP when configured.
func (Email) Send(context.Context, string, string, string) error {
	slog.Debug("email delivery is not configured")
	return nil
}

var _ core.Email = Email{}
