package hub

import (
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
)

// generatedAlphabet is lowercase letters and digits without look-alikes (no 0 o 1 l i),
// so a value is easy to read out and type.
const generatedAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// newGeneratedSecret makes a value like "k7m3q-x9rt2-h4wpz-c8nfa".
func newGeneratedSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%5 == 0 {
			sb.WriteByte('-')
		}
		// 31 letters; bytes of 248 and up are skipped below by drawing again, so no letter is likelier.
		for c >= 248 {
			var one [1]byte
			if _, err := rand.Read(one[:]); err != nil {
				return "", err
			}
			c = one[0]
		}
		sb.WriteByte(generatedAlphabet[int(c)%len(generatedAlphabet)])
	}
	return sb.String(), nil
}

// ensureGenerated makes a value for each generate_secrets name that has none in this
// environment. A value that exists is never replaced.
func (h *Hub) ensureGenerated(m *manifest.Manifest, app, env string) error {
	missing, err := h.ad.Secrets.Missing(app, env, m.GenerateSecrets)
	if err != nil {
		return fmt.Errorf("read secrets: %w", err)
	}
	for _, name := range missing {
		v, err := newGeneratedSecret()
		if err != nil {
			return err
		}
		if err := h.ad.Secrets.Set(app, env, name, v, core.ActorHomelab); err != nil {
			return fmt.Errorf("save %s: %w", name, err)
		}
	}
	return nil
}
