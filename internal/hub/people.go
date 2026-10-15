package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

// SetupOwner creates the first household account and returns its passkey registration link.
func (h *Hub) SetupOwner(ctx context.Context, p core.Person, recoveryHash string) (string, error) {
	exists, err := h.store.OwnerExists(ctx)
	if err != nil {
		return "", err
	}
	if exists {
		return "", userErr("Homelab is already set up. Sign in with your passkey.")
	}
	if h.ad.Identity == nil {
		return "", userErr("Homelab's sign-in service is still starting. Try again in a moment.")
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Email = strings.ToLower(strings.TrimSpace(p.Email))
	if p.Name == "" || len([]rune(p.Name)) > 100 || p.Email == "" {
		return "", userErr("Add your name (up to 100 characters) and email address.")
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	p.ID = fmt.Sprintf("%x", b[:])
	p.Roles = []string{"owner", "admin"}
	p.Grants = map[string]string{}
	p.Status = "invited"
	if err := h.store.SavePerson(ctx, p); err != nil {
		return "", err
	}
	identityID, err := h.ad.Identity.Ensure(ctx, p, personGroups(p))
	if err != nil {
		_ = h.store.DeletePerson(ctx, p.ID)
		return "", err
	}
	if identityID == "" {
		_ = h.store.DeletePerson(ctx, p.ID)
		return "", fmt.Errorf("sign-in service didn't return an account id")
	}
	if err := h.store.RecordIdentityID(ctx, p.ID, identityID); err != nil {
		_ = h.store.DeletePerson(ctx, p.ID)
		return "", err
	}
	link, err := h.ad.Identity.LoginLink(ctx, identityID, time.Hour)
	if err != nil {
		_ = h.store.DeletePerson(ctx, p.ID)
		return "", err
	}
	if err := h.store.SetRecovery(ctx, p.ID, recoveryHash); err != nil {
		_ = h.store.DeletePerson(ctx, p.ID)
		return "", err
	}
	_ = h.store.SetTailscaleLogin(ctx, p.ID, p.Email)
	h.logEvent(ctx, state.Event{Actor: "homelab", Action: "owner.created", Detail: "Created the household owner account"})
	return link, nil
}

// People lists household accounts.
func (h *Hub) People(ctx context.Context) ([]core.Person, error) { return h.store.People(ctx) }

// PasskeyLink creates a fresh Pocket ID link for a signed-in person to register a passkey.
func (h *Hub) PasskeyLink(ctx context.Context, personID string) (string, error) {
	if h.ad.Identity == nil {
		return "", userErr("Homelab's sign-in service is still starting. Try again in a moment.")
	}
	p, err := h.store.Person(ctx, personID)
	if err != nil || p.Status != "signed_in" {
		return "", userErr("Sign in to set up a passkey.")
	}
	return h.ad.Identity.LoginLink(ctx, providerID(p), time.Hour)
}

// InitialOwnerPasskeyLink reissues the setup link while the initial owner has
// not completed their first Pocket ID sign-in.
func (h *Hub) InitialOwnerPasskeyLink(ctx context.Context) (string, error) {
	people, err := h.store.People(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range people {
		if contains(p.Roles, "owner") {
			if p.Status != "invited" || p.IdentityID == "" {
				return "", userErr("The owner has already signed in. Use recovery to sign in and set up a passkey from Me.")
			}
			if h.ad.Identity == nil {
				return "", userErr("Homelab's sign-in service is still starting. Try again in a moment.")
			}
			return h.ad.Identity.LoginLink(ctx, p.IdentityID, time.Hour)
		}
	}
	return "", userErr("Homelab hasn't been set up yet.")
}

// Invitation is what an admin hands to a new person.
type Invitation struct {
	ID  string `json:"id"`
	URL string `json:"url"` // Homelab's one-time link, for this computer
	// TailscaleURL lets them join the tailnet, so they can reach Homelab from anywhere.
	TailscaleURL   string `json:"tailscaleUrl,omitempty"`
	TailscaleError string `json:"tailscaleError,omitempty"`
}

// InvitePerson creates a person and returns their invitation links.
func (h *Hub) InvitePerson(ctx context.Context, p core.Person) (Invitation, error) {
	link, err := h.invitePerson(ctx, &p)
	if err != nil {
		return Invitation{}, err
	}
	inv := Invitation{ID: p.ID, URL: link}
	inv.TailscaleURL, inv.TailscaleError = h.inviteRemote(ctx, p)
	return inv, nil
}

func (h *Hub) invitePerson(ctx context.Context, pp *core.Person) (string, error) {
	p := *pp
	defer func() { *pp = p }()
	if h.ad.Identity == nil {
		return "", fmt.Errorf("sign-in service is not ready")
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Email = strings.TrimSpace(strings.ToLower(p.Email))
	if p.Name == "" || len([]rune(p.Name)) > 100 {
		return "", userErr("Add a name between 1 and 100 characters.")
	}
	if err := h.validatePersonAccess(ctx, p, false); err != nil {
		return "", err
	}
	if p.ID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		p.ID = fmt.Sprintf("%x", b[:])
	}
	p.Status = "invited"
	if err := h.store.SavePerson(ctx, p); err != nil {
		return "", err
	}
	identityID, err := h.ad.Identity.Ensure(ctx, p, personGroups(p))
	if err != nil {
		_ = h.store.DeletePerson(ctx, p.ID)
		return "", err
	}
	if identityID == "" {
		_ = h.store.DeletePerson(ctx, p.ID)
		return "", fmt.Errorf("sign-in service didn't return an account id")
	}
	if err := h.store.RecordIdentityID(ctx, p.ID, identityID); err != nil {
		_ = h.store.DeletePerson(ctx, p.ID)
		return "", err
	}
	if err := h.store.RevokeInvites(ctx, p.ID); err != nil {
		return "", err
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(token[:])
	sum := sha256.Sum256([]byte(value))
	if err := h.store.StoreInvite(ctx, sum[:], p.ID, h.cfg.Now().Add(7*24*time.Hour)); err != nil {
		return "", err
	}
	h.logEvent(ctx, state.Event{Actor: "owner", Action: "person.invited", Detail: "Invited " + p.Name})
	return "http://localhost:7080/invite/" + value, nil
}

func personGroups(p core.Person) []string {
	out := make([]string, 0, len(p.Roles)+len(p.Grants))
	for _, r := range p.Roles {
		out = append(out, "role:"+r)
	}
	for app := range p.Grants {
		out = append(out, "app:"+app)
	}
	sort.Strings(out)
	return out
}

// RedeemInvite issues a fresh identity-provider link for an unexpired invite.
func (h *Hub) RedeemInvite(ctx context.Context, token string) (string, error) {
	sum := sha256.Sum256([]byte(token))
	id, err := h.store.ConsumeInvite(ctx, sum[:])
	if err != nil {
		return "", userErr("This invite has expired or was already used. Ask the owner to send a new one.")
	}
	p, err := h.store.Person(ctx, id)
	if err != nil || p.Status != "invited" {
		return "", userErr("This invite has expired or was already used. Ask the owner to send a new one.")
	}
	return h.ad.Identity.LoginLink(ctx, providerID(p), time.Hour)
}

// ResendInvite revokes the previous link and creates another one for an invited person.
func (h *Hub) ResendInvite(ctx context.Context, id string) (string, error) {
	p, err := h.store.Person(ctx, id)
	if err != nil {
		return "", userErr("That person is no longer in Homelab.")
	}
	if p.Status != "invited" {
		return "", userErr("That person has already signed in. They can use their passkey to return.")
	}
	if err := h.store.RevokeInvites(ctx, id); err != nil {
		return "", err
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(token[:])
	sum := sha256.Sum256([]byte(value))
	if err := h.store.StoreInvite(ctx, sum[:], id, h.cfg.Now().Add(7*24*time.Hour)); err != nil {
		return "", err
	}
	h.logEvent(ctx, state.Event{Actor: "admin", Action: "person.invite_resent", Detail: "Resent an invite for " + p.Name})
	return "http://localhost:7080/invite/" + value, nil
}

// UpdatePerson replaces a person's name, roles and grants.
func (h *Hub) UpdatePerson(ctx context.Context, p core.Person) error {
	old, err := h.store.Person(ctx, p.ID)
	if err != nil {
		return userErr("That person is no longer in Homelab.")
	}
	if contains(old.Roles, "owner") && !contains(p.Roles, "owner") {
		return userErr("The household owner can't be changed here.")
	}
	if !contains(old.Roles, "owner") && contains(p.Roles, "owner") {
		return userErr("An admin can't make another owner.")
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len([]rune(p.Name)) > 100 {
		return userErr("Add a name between 1 and 100 characters.")
	}
	if err := h.validatePersonAccess(ctx, p, contains(old.Roles, "owner")); err != nil {
		return err
	}
	p.Email = strings.ToLower(strings.TrimSpace(p.Email))
	p.TailscaleLogin = strings.ToLower(strings.TrimSpace(p.TailscaleLogin))
	if strings.ContainsAny(p.TailscaleLogin, " \t,") || len(p.TailscaleLogin) > 200 {
		return userErr("Use the Tailscale login exactly as Tailscale shows it, like sam@gmail.com.")
	}
	if p.TailscaleLogin != "" && !strings.EqualFold(p.TailscaleLogin, old.TailscaleLogin) {
		if other, err := h.store.PersonByTailscaleLogin(ctx, p.TailscaleLogin); err == nil && other.ID != p.ID {
			return userErr("%s already uses that Tailscale login.", other.Name)
		}
	}
	p.Status = old.Status
	p.IdentityID = old.IdentityID
	if err = h.store.SavePerson(ctx, p); err != nil {
		return err
	}
	if p.TailscaleLogin != old.TailscaleLogin {
		if err := h.store.SetTailscaleLogin(ctx, p.ID, p.TailscaleLogin); err != nil {
			return err
		}
	}
	_ = h.syncPolicy(ctx, true) // on failure the reconciler keeps trying
	if h.ad.Identity != nil {
		identityID, err := h.ad.Identity.Ensure(ctx, p, personGroups(p))
		if err != nil {
			return err
		}
		return h.store.RecordIdentityID(ctx, p.ID, identityID)
	}
	return nil
}

func providerID(p core.Person) string {
	if p.IdentityID != "" {
		return p.IdentityID
	}
	return p.ID
}

func (h *Hub) validatePersonAccess(ctx context.Context, p core.Person, keepOwner bool) error {
	if len(p.Roles) == 0 {
		return userErr("Choose at least one role.")
	}
	allowed := map[string]bool{"admin": true, "builder": true, "member": true}
	if keepOwner {
		allowed["owner"] = true
	}
	seen := map[string]bool{}
	for _, role := range p.Roles {
		if !allowed[role] || seen[role] {
			return userErr("Choose a role Homelab knows about.")
		}
		seen[role] = true
	}
	for app, level := range p.Grants {
		if _, err := h.store.App(ctx, app); err != nil {
			return userErr("There's no app %q.", app)
		}
		if level != "use" && level != "build" {
			return userErr("Choose use or build access for each app.")
		}
	}
	return nil
}

// RemovalStep records one result in the remove-person checklist.
type RemovalStep struct {
	Name   string `json:"name"`
	Done   bool   `json:"done"`
	Detail string `json:"detail,omitempty"`
}

// RemovePerson disables sign-in, ends Homelab sessions and deletes local access.
func (h *Hub) RemovePerson(ctx context.Context, id string) ([]RemovalStep, error) {
	p, err := h.store.Person(ctx, id)
	if err != nil {
		return nil, userErr("That person is no longer in Homelab.")
	}
	if contains(p.Roles, "owner") {
		return nil, userErr("The household owner can't be removed.")
	}
	steps := h.removeRemote(ctx, p)
	if steps == nil {
		steps = []RemovalStep{}
	}
	if h.ad.Identity != nil {
		err = h.ad.Identity.Disable(ctx, providerID(p))
	} else {
		err = fmt.Errorf("sign-in service is unavailable")
	}
	steps = append(steps, RemovalStep{Name: "Disable Pocket ID sign-in", Done: err == nil, Detail: errorText(err)})
	steps = append(steps, RemovalStep{Name: "End existing Pocket ID sessions", Done: true, Detail: "Pocket ID keeps its own browser session, but the disabled account and removed Homelab sessions prevent further access."})
	err = h.store.DeleteSessions(ctx, id)
	if err == nil {
		err = h.store.DeleteAppSessions(ctx, id)
	}
	steps = append(steps, RemovalStep{Name: "End Homelab and app sessions", Done: err == nil, Detail: errorText(err)})
	err = h.store.DeletePerson(ctx, id)
	steps = append(steps, RemovalStep{Name: "Remove app access and invitation links", Done: err == nil, Detail: errorText(err)})
	if h.ad.Remote != nil {
		err = h.syncPolicy(ctx, true)
		steps = append(steps, RemovalStep{Name: "Update the tailnet's access rules", Done: err == nil, Detail: remoteErrorText(err)})
	}
	h.logEvent(ctx, state.Event{Actor: "admin", Action: "person.removed", Detail: "Removed " + p.Name})
	return steps, nil
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return "Homelab couldn't finish this step. Check the service and try again."
}
