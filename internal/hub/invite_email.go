package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

const msgEmailOff = "Email isn't set up, so send the link yourself."

// emailInvite emails the link when email is set up. A failure never fails the invite: it is
// written to inv so the page can show it beside the link.
func (h *Hub) emailInvite(ctx context.Context, inv *Invitation, p core.Person) {
	if p.Email == "" {
		inv.EmailError = "There's no email address for them, so send the link yourself."
		return
	}
	subject := "You're invited to Homelab"
	body := fmt.Sprintf("%s invited you to Homelab.\n\nOpen this link to join:\n%s\n\nThe link works for 7 days.\n", h.inviterName(ctx), inv.URL)
	switch err := h.sendEmail(ctx, p.Email, subject, body); {
	case err == nil:
		inv.Emailed = true
	case errors.Is(err, core.ErrEmailNotConfigured):
		inv.EmailError = msgEmailOff
	default:
		h.log.Warn("invite email failed", "err", err)
		inv.EmailError = err.Error()
	}
}

func (h *Hub) sendEmail(ctx context.Context, to, subject, body string) error {
	if h.ad.Email == nil {
		return core.ErrEmailNotConfigured
	}
	return h.ad.Email.Send(ctx, to, subject, body)
}

// inviterName is who is sending the invite, for the email.
func (h *Hub) inviterName(ctx context.Context) string {
	if v, ok := core.ViewerFrom(ctx); ok {
		if v.Name != "" {
			return v.Name
		}
		if v.PersonID != "" {
			if p, err := h.store.Person(ctx, v.PersonID); err == nil && p.Name != "" {
				return p.Name
			}
		}
	}
	return "The Homelab owner"
}

// SendTestEmail sends a short message to the signed-in person's own address.
func (h *Hub) SendTestEmail(ctx context.Context, by core.Viewer) error {
	p, err := h.store.Person(ctx, by.PersonID)
	if err != nil || p.Email == "" {
		return userErr("Add an email address to your account first.")
	}
	err = h.sendEmail(ctx, p.Email, "Homelab test email", "This is a test from Homelab. Email is working.\n")
	switch {
	case err == nil:
		h.logEvent(ctx, state.Event{Actor: by.Label(), Action: "email.test", Detail: "Sent a test email"})
		return nil
	case errors.Is(err, core.ErrEmailNotConfigured):
		return userErr("Email isn't set up yet. Add your Gmail address and app password first.")
	default:
		h.log.Warn("test email failed", "err", err)
		return userErr("%s", err.Error())
	}
}

// Password length limits. bcrypt only reads the first 72 bytes.
const (
	minPassword = 10
	maxPassword = 72
)

// SetInvitePassword lets an invited person choose their own password and signs them up.
// It returns the person so the caller can start their session.
func (h *Hub) SetInvitePassword(ctx context.Context, token, password string) (core.Person, error) {
	if n := utf8.RuneCountInString(password); n < minPassword || len(password) > maxPassword {
		return core.Person{}, userErr("Choose a password of %d to %d characters.", minPassword, maxPassword)
	}
	id, err := h.store.ConsumeInvite(ctx, inviteDigest(token))
	if err != nil {
		return core.Person{}, errInviteGone
	}
	p, err := h.store.Person(ctx, id)
	if err != nil || p.Status != "invited" {
		return core.Person{}, errInviteGone
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return core.Person{}, err
	}
	if err := h.store.ActivateWithPassword(ctx, p.ID, providerID(p), string(hash)); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return core.Person{}, errInviteGone
		}
		return core.Person{}, err
	}
	h.logEvent(ctx, state.Event{Actor: p.Email, Action: "person.signed_in", Detail: "Joined with a password"})
	p.Status = "signed_in"
	return p, nil
}

var errInviteGone = userErr("This invite has expired or was already used. Ask the owner to send a new one.")

// CheckPersonPassword checks an email and password. ok is false for an unknown email, a person
// who hasn't signed up, or a wrong password. The work is the same in each case.
func (h *Hub) CheckPersonPassword(ctx context.Context, email, password string, fallback []byte) (core.Person, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	p, err := h.store.PersonByEmail(ctx, email)
	hash := dummyHash()
	found := err == nil && p.Status == "signed_in"
	if found {
		if own, err := h.store.PasswordHash(ctx, p.ID); err == nil {
			hash = []byte(own)
		} else if contains(p.Roles, "owner") && len(fallback) > 0 {
			hash = fallback
		} else {
			found = false
		}
	}
	match := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	return p, found && match
}

// dummyHash is compared against when there is no real hash, so timing doesn't show whether an
// email is known.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("homelab-no-such-person"), bcrypt.DefaultCost)
	return h
})
