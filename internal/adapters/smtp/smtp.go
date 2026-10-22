// Package smtp sends Homelab's email through an SMTP server such as Gmail's, with STARTTLS.
package smtp

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	netsmtp "net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Options configures the sender.
type Options struct {
	// Credentials returns the sender address and app password. It is called for every message,
	// so a change in Settings applies without a restart. Empty values mean email isn't set up.
	Credentials func() (address, password string)
	Host        string // default smtp.gmail.com
	Port        int    // default 587
	// Deliver sends one finished message. The default talks to the server; tests replace it.
	Deliver func(ctx context.Context, from, to string, msg []byte) error
	Now     func() time.Time
	Logger  *slog.Logger
}

// Email sends plain-text messages.
type Email struct {
	opt Options
}

// New creates the sender.
func New(opt Options) *Email {
	if opt.Host == "" {
		opt.Host = "smtp.gmail.com"
	}
	if opt.Port == 0 {
		opt.Port = 587
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	e := &Email{opt: opt}
	if e.opt.Deliver == nil {
		e.opt.Deliver = e.deliver
	}
	return e
}

// Send emails body to one address. It returns core.ErrEmailNotConfigured until an address and
// app password are set.
func (e *Email) Send(ctx context.Context, to, subject, body string) error {
	from, pass := "", ""
	if e.opt.Credentials != nil {
		from, pass = e.opt.Credentials()
	}
	if strings.TrimSpace(from) == "" || pass == "" {
		return core.ErrEmailNotConfigured
	}
	msg, fromAddr, toAddr, err := buildMessage(from, to, subject, body, e.opt.Now())
	if err != nil {
		return err
	}
	if err := e.opt.Deliver(ctx, fromAddr, toAddr, msg); err != nil {
		e.opt.Logger.Warn("sending email failed", "err", err)
		return friendly(err)
	}
	return nil
}

// buildMessage makes the full plain-text message. Addresses and the subject can't hold line
// breaks, which would let a caller add headers of their own.
func buildMessage(from, to, subject, body string, now time.Time) (msg []byte, fromAddr, toAddr string, err error) {
	for _, v := range []string{from, to, subject} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, "", "", errors.New("That email can't have line breaks in its address or subject.")
		}
	}
	f, err := mail.ParseAddress(from)
	if err != nil {
		return nil, "", "", errors.New("The sender address in Settings doesn't look right.")
	}
	t, err := mail.ParseAddress(to)
	if err != nil {
		return nil, "", "", errors.New("That email address doesn't look right.")
	}
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", (&mail.Address{Address: f.Address}).String())
	h("To", (&mail.Address{Address: t.Address}).String())
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", now.Format(time.RFC1123Z))
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=UTF-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(body))
	_ = w.Close()
	return b.Bytes(), f.Address, t.Address, nil
}

// deliver sends one message over SMTP with STARTTLS. The password never leaves this call.
func (e *Email) deliver(ctx context.Context, from, to string, msg []byte) error {
	address, pass := e.opt.Credentials()
	// Google shows app passwords in groups with spaces.
	pass = strings.ReplaceAll(pass, " ", "")
	addr := net.JoinHostPort(e.opt.Host, strconv.Itoa(e.opt.Port))
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	c, err := netsmtp.NewClient(conn, e.opt.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.StartTLS(&tls.Config{ServerName: e.opt.Host, MinVersion: tls.VersionTLS12}); err != nil {
		return err
	}
	if err := c.Auth(netsmtp.PlainAuth("", address, pass, e.opt.Host)); err != nil {
		return err
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// friendly turns a delivery error into a sentence for people. It never includes the server's
// own text, which can echo the account name.
func friendly(err error) error {
	var tp *textproto.Error
	if errors.As(err, &tp) && (tp.Code == 534 || tp.Code == 535) {
		return errors.New("Gmail didn't accept the address and app password. Check them in Settings.")
	}
	return fmt.Errorf("Homelab couldn't send the email. Check Settings and the internet connection.")
}

var _ core.Email = (*Email)(nil)
