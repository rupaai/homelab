package smtp

import (
	"context"
	"errors"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

type sent struct {
	from, to string
	msg      string
}

func newTest(address, password string, out *[]sent, deliverErr error) *Email {
	return New(Options{
		Credentials: func() (string, string) { return address, password },
		Now:         func() time.Time { return time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC) },
		Deliver: func(_ context.Context, from, to string, msg []byte) error {
			*out = append(*out, sent{from, to, string(msg)})
			return deliverErr
		},
	})
}

func TestSendBuildsAPlainTextMessage(t *testing.T) {
	var out []sent
	e := newTest("home@gmail.com", "app pass", &out, nil)
	body := "Labiba invited you to Homelab.\n\nhttp://192.168.1.183:7080/invite/abc\n\nThe link works for 7 days.\n"
	if err := e.Send(context.Background(), "sam@example.test", "You're invited to Homelab", body); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].from != "home@gmail.com" || out[0].to != "sam@example.test" {
		t.Fatalf("sent = %+v", out)
	}
	msg := out[0].msg
	head, text, _ := strings.Cut(msg, "\r\n\r\n")
	for _, want := range []string{
		"From: <home@gmail.com>\r\n", "To: <sam@example.test>\r\n", "Subject: You're invited to Homelab\r\n",
		"Date: Thu, 22 Oct 2026 09:00:00 +0000\r\n", "MIME-Version: 1.0\r\n", "Content-Type: text/plain; charset=UTF-8\r\n",
	} {
		if !strings.Contains(head+"\r\n", want) {
			t.Errorf("missing header %q in %q", want, head)
		}
	}
	if !strings.Contains(text, "http://192.168.1.183:7080/invite/abc") || !strings.Contains(text, "The link works for 7 days.") {
		t.Errorf("body = %q", text)
	}
	if strings.Contains(msg, "app pass") {
		t.Error("message holds the password")
	}
}

func TestSendRefusesLineBreaks(t *testing.T) {
	var out []sent
	e := newTest("home@gmail.com", "pw", &out, nil)
	ctx := context.Background()
	for _, c := range []struct{ to, subject string }{
		{"sam@example.test\r\nBcc: evil@example.test", "Hi"},
		{"sam@example.test\nBcc: evil@example.test", "Hi"},
		{"sam@example.test", "Hi\r\nBcc: evil@example.test"},
	} {
		if err := e.Send(ctx, c.to, c.subject, "x"); err == nil {
			t.Errorf("accepted %q / %q", c.to, c.subject)
		}
	}
	if err := newTest("home@gmail.com\r\nBcc: x@y.z", "pw", &out, nil).Send(ctx, "sam@example.test", "Hi", "x"); err == nil {
		t.Error("accepted a sender with a line break")
	}
	if err := e.Send(ctx, "not an address", "Hi", "x"); err == nil {
		t.Error("accepted a bad address")
	}
	if len(out) != 0 {
		t.Fatalf("sent %d messages", len(out))
	}
}

func TestSendNotConfigured(t *testing.T) {
	var out []sent
	for _, c := range [][2]string{{"", ""}, {"home@gmail.com", ""}, {"", "pw"}} {
		err := newTest(c[0], c[1], &out, nil).Send(context.Background(), "sam@example.test", "Hi", "x")
		if !errors.Is(err, core.ErrEmailNotConfigured) {
			t.Errorf("%v: err = %v", c, err)
		}
	}
	if len(out) != 0 {
		t.Fatal("sent without being set up")
	}
	// No credentials function at all.
	if err := New(Options{}).Send(context.Background(), "sam@example.test", "Hi", "x"); !errors.Is(err, core.ErrEmailNotConfigured) {
		t.Errorf("err = %v", err)
	}
}

func TestSendErrorsAreFriendlyAndKeepNoSecrets(t *testing.T) {
	var out []sent
	err := newTest("home@gmail.com", "hunter2hunter2", &out, &textproto.Error{Code: 535, Msg: "bad credentials for home@gmail.com"}).
		Send(context.Background(), "sam@example.test", "Hi", "x")
	if err == nil || !strings.Contains(err.Error(), "app password") || strings.Contains(err.Error(), "home@gmail.com") {
		t.Errorf("auth error = %v", err)
	}
	err = newTest("home@gmail.com", "hunter2hunter2", &out, errors.New("dial tcp: i/o timeout")).Send(context.Background(), "sam@example.test", "Hi", "x")
	if err == nil || strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("network error = %v", err)
	}
}

func TestSubjectAndBodyAreUTF8Safe(t *testing.T) {
	msg, _, _, err := buildMessage("a@b.test", "c@d.test", "Hello, Zoë", "Café\n", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "Subject: =?utf-8?q?Hello,_Zo=C3=AB?=") {
		t.Errorf("subject not encoded: %s", msg)
	}
}
