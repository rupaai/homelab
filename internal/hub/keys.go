package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
	"github.com/rupaai/homelab/internal/state"
)

// API keys (platform services). A key is "hlk_" plus 32 random bytes, base64url. Homelab
// keeps only its SHA-256. Two kinds:
//
//   - app keys: one per app environment, made by Homelab and given to the app as
//     HOMELAB_TOKEN. They only work on the app door (see cmd/homelabd) and act for that
//     app environment only.
//   - admin keys: made by an admin in the companion for a server outside Homelab. They
//     work wherever the companion is reached (this computer or the home network),
//     never on the app door, and stop working when the person who made one is no longer
//     an admin.
//
// Each later service adds its own scope. An app key acts for its own app and environment
// (Caller.App and Caller.Env); services decide what that allows, not scope strings.
const (
	KeyKindApp   = "app"
	KeyKindAdmin = "admin"

	keyPrefix = "hlk_"
	keyBytes  = 32

	// AppTokenSecret is where an app environment's key is kept in the secret store. Names
	// starting with HOMELAB_ are Homelab's own: never listed, set or deleted by people.
	AppTokenSecret = "HOMELAB_TOKEN"
	reservedPrefix = "HOMELAB_"

	// keyTouchEvery is how often "last used" is written for a busy key.
	keyTouchEvery = time.Minute
	// keyLogReadsEvery is how often a key's repeated reads are written to the activity log.
	// Every change it makes is always logged.
	keyLogReadsEvery = 10 * time.Minute
)

// ErrBadKey means a key is unknown, revoked, expired or no longer allowed. Callers get one
// message for all of these, so a guess learns nothing.
var ErrBadKey = errors.New("that API key isn't valid")

// Scopes.
const (
	ScopeUsersRead  = "users:read"
	ScopeFilesRead  = "files:read"  // read every folder's files
	ScopeFilesWrite = "files:write" // also make folders, save, change and delete files

	// Secrets scopes belong to an app's own key and never to an admin key: no admin key can
	// be made with them, and homelab-services refuses admin keys on every secrets route.
	ScopeSecretsRead  = "secrets:read"  // list and read the app's own secrets
	ScopeSecretsWrite = "secrets:write" // also save and delete the app's own secrets
)

// scopes are the scopes an admin key may have.
var scopes = []string{ScopeUsersRead, ScopeFilesRead, ScopeFilesWrite}

// Caller is who is calling with an API key.
type Caller struct {
	KeyID    string
	KeyName  string
	Kind     string // app | admin
	App, Env string // app keys only
	Scopes   []string
	PersonID string // admin keys: the person who made it
}

// Label is how a caller appears in the activity log.
func (c Caller) Label() string {
	if c.Kind == KeyKindApp {
		return "app key " + c.App + "/" + c.Env
	}
	return "key " + c.KeyName
}

// Allows says whether the caller has a scope.
func (c Caller) Allows(scope string) bool {
	return slices.Contains(c.Scopes, scope)
}

// checkScope validates one scope an admin asked for.
func checkScope(s string) error {
	if !slices.Contains(scopes, s) {
		return userErr("%q isn't a scope Homelab knows. Use %s.", s, strings.Join(scopes, ", "))
	}
	return nil
}

// appKeyScopes are the scopes an app's own key has. secrets_api in homelab.yaml decides the
// secrets ones: read (the default) gives secrets:read, write also gives secrets:write, and
// none gives neither.
func appKeyScopes(m *manifest.Manifest) []string {
	out := []string{ScopeUsersRead}
	switch m.SecretsAPI {
	case manifest.SecretsAPINone:
	case manifest.SecretsAPIWrite:
		out = append(out, ScopeSecretsRead, ScopeSecretsWrite)
	default:
		out = append(out, ScopeSecretsRead)
	}
	return out
}

func hashKey(token string) []byte {
	d := sha256.Sum256([]byte(token))
	return d[:]
}

func newKey() (token, id string, err error) {
	b := make([]byte, keyBytes)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	idb := make([]byte, 9)
	if _, err = rand.Read(idb); err != nil {
		return "", "", err
	}
	return keyPrefix + base64.RawURLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(idb), nil
}

// keyPrefixOf is the part of a key shown in the companion: enough to tell keys apart, far
// too little to guess the rest.
func keyPrefixOf(token string) string { return token[:len(keyPrefix)+4] }

func isAdmin(p core.Person) bool {
	if p.Status != "signed_in" {
		return false
	}
	for _, r := range p.Roles {
		if r == "owner" || r == "admin" {
			return true
		}
	}
	return false
}

// CreateAdminKey makes a key for a server outside Homelab. Only admins may, and the key
// can't do more than they can. The key itself is returned once and never stored.
func (h *Hub) CreateAdminKey(ctx context.Context, name string, scopes []string, expires *time.Time, by core.Viewer) (string, state.APIKey, error) {
	p, err := h.store.Person(ctx, by.PersonID)
	if err != nil || !isAdmin(p) {
		return "", state.APIKey{}, userErr("Only admins can make API keys.")
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 60 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", state.APIKey{}, userErr("Give the key a name of up to 60 characters, for example \"photo service\".")
	}
	if len(scopes) == 0 {
		return "", state.APIKey{}, userErr("Pick at least one thing the key may do.")
	}
	seen := map[string]bool{}
	var clean []string
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if err := checkScope(s); err != nil {
			return "", state.APIKey{}, err
		}
		if !seen[s] {
			seen[s] = true
			clean = append(clean, s)
		}
	}
	sort.Strings(clean)
	if expires != nil && !expires.After(h.cfg.Now()) {
		return "", state.APIKey{}, userErr("The end date must be in the future.")
	}
	token, id, err := newKey()
	if err != nil {
		return "", state.APIKey{}, err
	}
	k := state.APIKey{ID: id, Hash: hashKey(token), Prefix: keyPrefixOf(token), Name: name, Kind: KeyKindAdmin,
		Scopes: clean, CreatedBy: p.ID, CreatedAt: h.cfg.Now(), Expires: expires}
	if err := h.store.PutAPIKey(ctx, k); err != nil {
		return "", state.APIKey{}, err
	}
	h.logEvent(ctx, state.Event{Actor: by.Label(), Action: "key.created",
		Detail: fmt.Sprintf("Made the API key %q (%s)", name, strings.Join(clean, ", "))})
	return token, k, nil
}

// KeyView is a key as the companion shows it.
type KeyView struct {
	state.APIKey
	CreatedByName string `json:"createdByName,omitempty"`
}

// APIKeys lists the keys that work: admin keys and the apps' own keys.
func (h *Hub) APIKeys(ctx context.Context) (map[string][]KeyView, error) {
	out := map[string][]KeyView{}
	for _, kind := range []string{KeyKindAdmin, KeyKindApp} {
		keys, err := h.store.APIKeys(ctx, kind)
		if err != nil {
			return nil, err
		}
		views := []KeyView{}
		for _, k := range keys {
			v := KeyView{APIKey: k}
			if k.CreatedBy != "" {
				if p, err := h.store.Person(ctx, k.CreatedBy); err == nil {
					v.CreatedByName = p.Name
				}
			}
			views = append(views, v)
		}
		out[kind] = views
	}
	return out, nil
}

// RevokeKey stops a key working. An app's own key is replaced with a new one the next time
// that environment is deployed (Deploy again to get it at once).
func (h *Hub) RevokeKey(ctx context.Context, id string, by core.Viewer) error {
	k, err := h.store.APIKeyByID(ctx, id)
	if err != nil || k.Revoked != nil {
		return userErr("There's no API key %q.", id)
	}
	if err := h.store.RevokeAPIKey(ctx, id); err != nil {
		return err
	}
	h.forgetKey(id)
	detail := fmt.Sprintf("Revoked the API key %q", k.Name)
	if k.Kind == KeyKindApp {
		detail += ". The app gets a new one when it's deployed again"
	}
	h.logEvent(ctx, state.Event{Actor: by.Label(), App: k.App, Env: k.Env, Action: "key.revoked", Detail: detail})
	return nil
}

// appKey returns an app environment's key, making one if it has none that works. The key's
// scopes follow the manifest: if they have changed (secrets_api, or a key made before the
// secrets API existed), they are updated on the same key, so the app keeps its token.
func (h *Hub) appKey(ctx context.Context, app, env string, scopes []string) (string, error) {
	vals, err := h.ad.Secrets.Inject(app, env, []string{AppTokenSecret})
	if err != nil {
		return "", fmt.Errorf("read the app's API key: %w", err)
	}
	if token := vals[AppTokenSecret]; token != "" {
		k, err := h.store.APIKeyByHash(ctx, hashKey(token))
		if err == nil && k.Kind == KeyKindApp && k.App == app && k.Env == env && k.Revoked == nil {
			if !slices.Equal(k.Scopes, scopes) {
				if err := h.store.SetAPIKeyScopes(ctx, k.ID, scopes); err != nil {
					return "", err
				}
				h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: app, Env: env, Action: "key.scopes",
					Detail: "Changed what the app's own API key may do: " + strings.Join(scopes, ", ")})
			}
			return token, nil
		}
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return "", err
		}
	}
	token, id, err := newKey()
	if err != nil {
		return "", err
	}
	// The secret is written first: if storing the key fails, the next deploy sees a
	// token with no key and makes a fresh one.
	if err := h.ad.Secrets.Set(app, env, AppTokenSecret, token, core.ActorHomelab); err != nil {
		return "", fmt.Errorf("save the app's API key: %w", err)
	}
	k := state.APIKey{ID: id, Hash: hashKey(token), Prefix: keyPrefixOf(token), Name: app + " " + env, Kind: KeyKindApp,
		App: app, Env: env, Scopes: scopes, CreatedAt: h.cfg.Now()}
	if err := h.store.PutAPIKey(ctx, k); err != nil {
		return "", err
	}
	h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: app, Env: env, Action: "key.created",
		Detail: "Made the app's own API key (HOMELAB_TOKEN)"})
	return token, nil
}

// Authenticate checks an API key and says who is calling. Every failure is ErrBadKey.
func (h *Hub) Authenticate(ctx context.Context, token string) (Caller, error) {
	if !strings.HasPrefix(token, keyPrefix) {
		return Caller{}, ErrBadKey
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, keyPrefix))
	if err != nil || len(raw) != keyBytes {
		return Caller{}, ErrBadKey
	}
	hash := hashKey(token)
	k, err := h.store.APIKeyByHash(ctx, hash)
	if err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			h.log.Error("api key lookup", "err", err)
		}
		return Caller{}, ErrBadKey
	}
	if k.Revoked != nil {
		return Caller{}, ErrBadKey
	}
	now := h.cfg.Now()
	if k.Expires != nil && !now.Before(*k.Expires) {
		return Caller{}, ErrBadKey
	}
	c := Caller{KeyID: k.ID, KeyName: k.Name, Kind: k.Kind, App: k.App, Env: k.Env, Scopes: k.Scopes, PersonID: k.CreatedBy}
	switch k.Kind {
	case KeyKindAdmin:
		p, err := h.store.Person(ctx, k.CreatedBy)
		if err != nil || !isAdmin(p) {
			return Caller{}, ErrBadKey
		}
	case KeyKindApp:
		// Removing an app deletes its keys (ON DELETE CASCADE).
	default:
		return Caller{}, ErrBadKey
	}
	h.touchKey(ctx, k.ID, now)
	return c, nil
}

func (h *Hub) touchKey(ctx context.Context, id string, now time.Time) {
	h.keyMu.Lock()
	last, ok := h.keyTouched[id]
	if ok && now.Sub(last) < keyTouchEvery {
		h.keyMu.Unlock()
		return
	}
	h.keyTouched[id] = now
	h.keyMu.Unlock()
	if err := h.store.TouchAPIKey(context.WithoutCancel(ctx), id, now); err != nil {
		h.log.Warn("api key last used", "err", err)
	}
}

func (h *Hub) forgetKey(id string) {
	h.keyMu.Lock()
	defer h.keyMu.Unlock()
	delete(h.keyTouched, id)
	for k := range h.keyLogged {
		if strings.HasPrefix(k, id+"\x00") {
			delete(h.keyLogged, k)
		}
	}
}

// LogKeyUse writes what a key did to the activity log. Changes are always written; the
// same read by the same key is written at most once every ten minutes, so an app that
// polls doesn't bury everything else.
func (h *Hub) LogKeyUse(ctx context.Context, c Caller, what string, change bool) {
	if !change {
		now := h.cfg.Now()
		k := c.KeyID + "\x00" + what
		h.keyMu.Lock()
		last, ok := h.keyLogged[k]
		if ok && now.Sub(last) < keyLogReadsEvery {
			h.keyMu.Unlock()
			return
		}
		h.keyLogged[k] = now
		h.keyMu.Unlock()
	}
	h.logEvent(context.WithoutCancel(ctx), state.Event{Actor: c.Label(), App: c.App, Env: c.Env, Action: "key.used", Detail: what})
}

// ---- Users API ----

// UserView is a person as the users API shows them.
type UserView struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Email  string   `json:"email,omitempty"`
	Roles  []string `json:"roles"`
	Status string   `json:"status"`
	Access string   `json:"access,omitempty"` // for one app: admin, build or use
}

func userView(p core.Person, app string) UserView {
	v := UserView{ID: p.ID, Name: p.Name, Email: p.Email, Roles: p.Roles, Status: p.Status}
	if v.Roles == nil {
		v.Roles = []string{}
	}
	if app != "" {
		v.Access = appAccess(p, app)
	}
	return v
}

// appAccess is a person's access to an app: admin, build, use or "".
func appAccess(p core.Person, app string) string {
	for _, r := range p.Roles {
		if r == "owner" || r == "admin" {
			return "admin"
		}
	}
	return p.Grants[app]
}

// UserByID returns one person.
func (h *Hub) UserByID(ctx context.Context, id, app string) (UserView, error) {
	p, err := h.store.Person(ctx, id)
	if err != nil {
		return UserView{}, err
	}
	return userView(p, app), nil
}

// Users lists people: everyone, or (app not "") those with access to that app.
func (h *Hub) Users(ctx context.Context, app string) ([]UserView, error) {
	if app != "" {
		if _, err := h.store.App(ctx, app); err != nil {
			return nil, userErr("There's no app %q.", app)
		}
	}
	people, err := h.store.People(ctx)
	if err != nil {
		return nil, err
	}
	out := []UserView{}
	for _, p := range people {
		if app != "" && appAccess(p, app) == "" {
			continue
		}
		out = append(out, userView(p, app))
	}
	return out, nil
}

// reservedSecret says whether a secret name is Homelab's own (HOMELAB_*).
func reservedSecret(name string) bool { return strings.HasPrefix(name, reservedPrefix) }
