package hub

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
	"github.com/rupaai/homelab/internal/state"
)

// ---- Mirrors (G2) ----

// SyncMirrors refreshes the mirror of every app's repo, once.
func (h *Hub) SyncMirrors(ctx context.Context) {
	if h.ad.Mirror == nil {
		return
	}
	apps, err := h.store.Apps(ctx)
	if err != nil {
		return
	}
	for _, a := range apps {
		if err := h.ad.Mirror.Sync(ctx, a.Repo); err != nil {
			h.log.Warn("mirror", "repo", a.Repo, "err", err)
		}
	}
}

// ---- Removing an app ----

// RemoveApp stops an app's environments and forgets it. Its data folders, data copies and
// code mirror stay on this computer.
func (h *Hub) RemoveApp(ctx context.Context, appID string, by core.Viewer) error {
	a, err := h.store.App(ctx, appID)
	if err != nil {
		return userErr("There's no app %q.", appID)
	}
	lock := h.appLock(appID)
	if !lock.TryLock() {
		return ErrBusy
	}
	defer lock.Unlock()
	if err := h.store.RemoveApp(ctx, appID); err != nil {
		return err
	}
	for _, env := range []string{core.Dev, core.Prod} {
		if err := h.ad.Deployer.Stop(ctx, appID, env); err != nil {
			h.log.Warn("remove app: stop", "app", appID, "env", env, "err", err) // the reconciler retries
		}
		h.removeAddress(ctx, manifest.AddressName(appID, env))
	}
	if err := h.ad.Secrets.DeleteApp(appID); err != nil {
		h.log.Error("remove app: secrets", "app", appID, "err", err)
	}
	h.mu.Lock()
	for _, env := range []string{core.Dev, core.Prod} {
		delete(h.status, envKey{appID, env})
	}
	delete(h.builds, appID)
	delete(h.failed, appID)
	delete(h.watches, appID)
	delete(h.smoked, appID)
	delete(h.autoNote, appID)
	h.mu.Unlock()
	h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Action: "app.removed",
		Detail: fmt.Sprintf("Removed %s. Its data is kept in %s", a.Name, filepath.Join(h.cfg.DataDir, "apps", appID))})
	return nil
}

// ---- Secrets (S1-S8) ----

// SecretView is one secret of an app environment, never with its value.
type SecretView struct {
	Name      string     `json:"name"`
	Declared  bool       `json:"declared"` // listed in homelab.yaml
	Set       bool       `json:"set"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
}

// EnvSecrets is the secrets of one environment.
type EnvSecrets struct {
	Env     string       `json:"env"`
	Secrets []SecretView `json:"secrets"`
}

// AppSecrets lists, per environment, the secrets an app declares and any others stored.
func (h *Hub) AppSecrets(ctx context.Context, appID string) ([]EnvSecrets, error) {
	a, err := h.store.App(ctx, appID)
	if err != nil {
		return nil, userErr("There's no app %q.", appID)
	}
	m, err := h.parse(a.Manifest, a.Repo)
	if err != nil {
		return nil, &UserError{Msg: err.Error()}
	}
	var out []EnvSecrets
	for _, env := range []string{core.Dev, core.Prod} {
		stored, err := h.ad.Secrets.List(appID, env)
		if err != nil {
			return nil, err
		}
		byName := map[string]core.SecretInfo{}
		for _, s := range stored {
			if !reservedSecret(s.Name) {
				byName[s.Name] = s
			}
		}
		es := EnvSecrets{Env: env, Secrets: []SecretView{}}
		for _, n := range m.Secrets {
			es.Secrets = append(es.Secrets, secretView(n, true, byName))
			delete(byName, n)
		}
		var extra []string
		for n := range byName {
			extra = append(extra, n)
		}
		sort.Strings(extra)
		for _, n := range extra {
			es.Secrets = append(es.Secrets, secretView(n, false, byName))
		}
		out = append(out, es)
	}
	return out, nil
}

func secretView(name string, declared bool, stored map[string]core.SecretInfo) SecretView {
	v := SecretView{Name: name, Declared: declared}
	if s, ok := stored[name]; ok {
		t := s.UpdatedAt
		v.Set, v.UpdatedAt, v.UpdatedBy = true, &t, s.UpdatedBy
	}
	return v
}

// SetSecret stores a value for one app environment. Builders and admins may set but never
// read values back (S4).
func (h *Hub) SetSecret(ctx context.Context, appID, env, name, value string, by core.Viewer) error {
	if _, err := h.store.App(ctx, appID); err != nil {
		return userErr("There's no app %q.", appID)
	}
	if env != core.Dev && env != core.Prod {
		return userErr("There's no environment %q.", env)
	}
	if err := checkSecret(name, value); err != nil {
		return err
	}
	if err := h.ad.Secrets.Set(appID, env, name, value, by.Label()); err != nil {
		return &UserError{Msg: secretErr(err)}
	}
	h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: env, Action: "secret.set",
		Detail: fmt.Sprintf("Set %s for %s. It's used from the next deploy.", name, env)})
	return nil
}

// DeleteSecret removes a value from one app environment.
func (h *Hub) DeleteSecret(ctx context.Context, appID, env, name string, by core.Viewer) error {
	if _, err := h.store.App(ctx, appID); err != nil {
		return userErr("There's no app %q.", appID)
	}
	if env != core.Dev && env != core.Prod {
		return userErr("There's no environment %q.", env)
	}
	if reservedSecret(name) {
		return userErr("Names starting with HOMELAB_ are Homelab's own and can't be deleted.")
	}
	if err := h.ad.Secrets.Delete(appID, env, name); err != nil {
		return &UserError{Msg: secretErr(err)}
	}
	h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: env, Action: "secret.deleted", Detail: fmt.Sprintf("Deleted %s from %s", name, env)})
	return nil
}

var secretNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkSecret applies the rules every secret follows, whichever store keeps it.
func checkSecret(name, value string) error {
	switch {
	case reservedSecret(name):
		return userErr("Names starting with HOMELAB_ are Homelab's own. Pick another name.")
	case !secretNameRe.MatchString(name):
		return userErr("%q isn't a valid secret name: use letters, digits and _, not starting with a digit.", name)
	case value == "" || len(value) > 8192:
		return userErr("A value must be 1 to 8192 characters.")
	case strings.ContainsAny(value, "\n\r\x00"):
		return userErr("A value can't contain line breaks.")
	}
	return nil
}

// secretErr turns a store error into a message for people; the store's validation
// messages are already written for them.
func secretErr(err error) string {
	m := err.Error()
	if m == "" {
		return "That secret couldn't be saved."
	}
	return strings.ToUpper(m[:1]) + m[1:] + "."
}

// Setting is one platform credential the companion can set (S8).
type Setting struct {
	Name                string `json:"name"`
	Description         string `json:"description"`
	AppliesAfterRestart bool   `json:"appliesAfterRestart"`
}

// Settings lists the platform credentials people can set. Others (such as the identity
// signing key) are Homelab's own and never listed.
var Settings = []Setting{
	{Name: "GITHUB_TOKEN", Description: "Lets Homelab read your repos, builds and images. A classic GitHub token " +
		"(Settings > Developer settings > Tokens (classic)) with the read:packages scope, plus repo for private repos."},
	{Name: "TS_OAUTH_CLIENT_ID", AppliesAfterRestart: true, Description: "Lets Homelab invite people to your tailnet, set who reaches what, " +
		"and give each app its own Tailscale address (only with addresses: tailscale). In the Tailscale admin console: " +
		"Settings > Trust credentials > OAuth client, with the scopes devices:core, auth_keys, policy_file and users, and the tag tag:homelab."},
	{Name: "TS_OAUTH_CLIENT_SECRET", AppliesAfterRestart: true, Description: "The secret of that OAuth client. Tailscale shows it once, when you create the client."},
	{Name: "TS_AUTHKEY", AppliesAfterRestart: true, Description: "Only if you don't set up the OAuth client: lets Homelab give apps Tailscale addresses, " +
		"but not invite people or set access rules. In the Tailscale admin console: Settings > Keys > Generate auth key, reusable and tagged tag:homelab."},
}

// SettingView is a platform credential as the companion shows it.
type SettingView struct {
	Setting
	Set       bool       `json:"set"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
}

func knownSetting(name string) bool {
	for _, s := range Settings {
		if s.Name == name {
			return true
		}
	}
	return false
}

// PlatformSettings lists the platform credentials and whether each is set.
func (h *Hub) PlatformSettings(ctx context.Context) ([]SettingView, error) {
	stored, err := h.ad.Secrets.List("", "")
	if err != nil {
		return nil, err
	}
	byName := map[string]core.SecretInfo{}
	for _, s := range stored {
		byName[s.Name] = s
	}
	out := []SettingView{}
	for _, s := range Settings {
		v := SettingView{Setting: s}
		if i, ok := byName[s.Name]; ok {
			t := i.UpdatedAt
			v.Set, v.UpdatedAt, v.UpdatedBy = true, &t, i.UpdatedBy
		}
		out = append(out, v)
	}
	return out, ctx.Err()
}

// SetPlatformSetting stores a platform credential.
func (h *Hub) SetPlatformSetting(ctx context.Context, name, value string, by core.Viewer) error {
	if !knownSetting(name) {
		return userErr("There's no setting %q.", name)
	}
	if err := checkSecret(name, value); err != nil {
		return err
	}
	if err := h.ad.Secrets.Set("", "", name, value, by.Label()); err != nil {
		return &UserError{Msg: secretErr(err)}
	}
	h.logEvent(ctx, state.Event{Actor: by.Label(), Action: "setting.set", Detail: "Set " + name})
	if name == "GITHUB_TOKEN" {
		go h.CheckBuilds(context.WithoutCancel(ctx))
	}
	return nil
}

// DeletePlatformSetting removes a platform credential.
func (h *Hub) DeletePlatformSetting(ctx context.Context, name string, by core.Viewer) error {
	if !knownSetting(name) {
		return userErr("There's no setting %q.", name)
	}
	if err := h.ad.Secrets.Delete("", "", name); err != nil {
		return err
	}
	h.logEvent(ctx, state.Event{Actor: by.Label(), Action: "setting.deleted", Detail: "Deleted " + name})
	return nil
}

// mask replaces every known secret value of an app environment (and the platform
// credentials) in text with dots, so logs shown in the companion don't leak them (S6).
func (h *Hub) mask(ctx context.Context, a state.App, env, text string) string {
	var values []string
	if m, err := h.parse(a.Manifest, a.Repo); err == nil {
		if vals, err := h.ad.Secrets.Inject(a.ID, env, m.Secrets); err == nil {
			for _, v := range vals {
				values = append(values, v)
			}
		}
	}
	for _, s := range Settings {
		if v, ok := h.ad.Secrets.Platform(s.Name); ok {
			values = append(values, v)
		}
	}
	// Longest first, so a value that contains another is masked whole.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		if len(v) >= 4 {
			text = strings.ReplaceAll(text, v, "••••••")
		}
	}
	return text
}
