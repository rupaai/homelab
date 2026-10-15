package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rupaai/homelab/internal/adapters/docker"
	"github.com/rupaai/homelab/internal/adapters/filesecrets"
	"github.com/rupaai/homelab/internal/adapters/github"
	"github.com/rupaai/homelab/internal/adapters/gitmirror"
	"github.com/rupaai/homelab/internal/adapters/httphealth"
	"github.com/rupaai/homelab/internal/adapters/local"
	"github.com/rupaai/homelab/internal/adapters/noop"
	"github.com/rupaai/homelab/internal/adapters/pocketid"
	"github.com/rupaai/homelab/internal/adapters/secretstore"
	"github.com/rupaai/homelab/internal/adapters/tailscale"
	"github.com/rupaai/homelab/internal/api"
	"github.com/rupaai/homelab/internal/companion"
	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
	"github.com/rupaai/homelab/internal/identity"
	"github.com/rupaai/homelab/internal/state"
)

// localCompanionPort is the port the local adapter would give the name "homelab". Homelab
// never asks for that name in local mode (the listen address serves the companion), so
// this just keeps it out of the way of the real listen port.
const localCompanionPort = 7079

const (
	pocketIDAPIKeyName        = "POCKET_ID_API_KEY"
	pocketIDEncryptionKeyName = "POCKET_ID_ENCRYPTION_KEY"
	homelabOIDCSecretName     = "HOMELAB_OIDC_CLIENT_SECRET"
	pocketIDURL               = "http://localhost:7081"
	pocketIDImage             = "ghcr.io/pocket-id/pocket-id:v2.18.0"
)

func cmdRun(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	homeFlag := flags.String("home", "", "Homelab folder (default ~/.homelab)")
	level := flags.String("log-level", "info", "debug, info, warn or error")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(*level)); err != nil {
		return fmt.Errorf("--log-level %q: use debug, info, warn or error", *level)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))

	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(home)
	if err != nil {
		return err
	}
	if err := checkLoopback(cfg.Listen); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, home, cfg, log)
}

func run(ctx context.Context, home string, cfg Config, log *slog.Logger) error {
	secretsDir := filepath.Join(home, "secrets")
	secrets, err := secretstore.Open(filepath.Join(secretsDir, "secrets.db"), filepath.Join(secretsDir, "master.key"))
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	defer secrets.Close()
	apiKey, err := ensurePlatformSecret(secrets, pocketIDAPIKeyName)
	if err != nil {
		return fmt.Errorf("Pocket ID API key: %w", err)
	}
	encryptionKey, err := ensurePlatformSecret(secrets, pocketIDEncryptionKeyName)
	if err != nil {
		return fmt.Errorf("Pocket ID encryption key: %w", err)
	}
	oidcSecret, err := ensurePlatformSecret(secrets, homelabOIDCSecretName)
	if err != nil {
		return fmt.Errorf("Homelab sign-in key: %w", err)
	}
	if err := importSecretFiles(secretsDir, secrets, os.Stdout); err != nil {
		return fmt.Errorf("import secrets files: %w", err)
	}
	signer, err := identitySigner(secrets)
	if err != nil {
		return fmt.Errorf("identity key: %w", err)
	}

	store, err := state.Open(filepath.Join(home, "homelab.db"))
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	defer store.Close()

	var addrs core.Addresses
	switch cfg.Addresses {
	case "tailscale":
		key, _ := secrets.Platform("TS_AUTHKEY")
		a, err := tailscale.New(tailscale.Options{
			StateDir: filepath.Join(home, "tailscale"), AuthKey: key, Tags: cfg.Tailscale.Tags, Logger: log,
		})
		if err != nil {
			return fmt.Errorf("tailscale: %w", err)
		}
		addrs = a
	default:
		a, err := local.New(local.Options{Dir: filepath.Join(home, "local"), CompanionPort: localCompanionPort, Logger: log})
		if err != nil {
			return fmt.Errorf("local addresses: %w", err)
		}
		addrs = a
	}
	defer addrs.Close()

	deployer, err := docker.New(docker.Options{Root: filepath.Join(home, "apps"), Logger: log})
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	idp, err := pocketid.New(pocketid.Options{BaseURL: pocketIDURL, APIKey: apiKey})
	if err != nil {
		return err
	}
	token := func() string { t, _ := secrets.Platform("GITHUB_TOKEN"); return t }
	if token() == "" {
		log.Warn("GITHUB_TOKEN is not set: private repos and ghcr.io images won't work. Set it in the companion under Settings")
	}
	gh := github.New(github.Options{TokenFunc: token, Logger: log})
	mirror, err := gitmirror.New(gitmirror.Options{Dir: filepath.Join(home, "mirrors"), Token: token, Logger: log})
	if err != nil {
		return fmt.Errorf("mirrors: %w", err)
	}

	h := hub.New(hub.Config{DataDir: home, Logger: log, Owners: cfg.Owners, Identity: signer}, store, hub.Adapters{
		Source: gh, Builder: gh, Registry: gh, Deployer: deployer,
		Addresses: addrs, Health: httphealth.New(nil), Secrets: secrets,
		Smoke: deployer, Mirror: mirror, Identity: idp, Email: noop.Email{},
	})

	oidcConfig := api.NewOIDCConfig("", "", "")
	apiHandler := api.New(h, api.Options{Owners: cfg.Owners, Version: version, Static: companion.Site(), Logger: log, Store: store,
		OIDCConfig: oidcConfig})

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w (is Homelab already running?)", cfg.Listen, err)
	}
	srv := &http.Server{
		Handler:           onThisComputer(apiHandler),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	_, listenPort, _ := net.SplitHostPort(ln.Addr().String())
	fmt.Printf("Companion: http://localhost:%s\n", listenPort)
	go configurePocketID(ctx, deployer, idp, apiKey, encryptionKey, oidcSecret, oidcConfig, log, home)

	if cfg.Addresses == "tailscale" {
		if err := h.ServeCompanion(ctx, apiHandler); err != nil {
			return fmt.Errorf("serve the companion on Tailscale: %w", err)
		}
		go announceTailscale(ctx, addrs)
	}

	hubDone := make(chan struct{})
	go func() { defer close(hubDone); h.Run(ctx) }()

	var result error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("companion server: %w", err)
		}
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(stopCtx)
	<-hubDone
	return result
}

func configurePocketID(ctx context.Context, deployer *docker.Deployer, idp *pocketid.Client, apiKey, encryptionKey, oidcSecret string, oidcConfig *api.OIDCConfig, log *slog.Logger, home string) {
	delay := time.Second
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := deployer.EnsureSupportingService(attemptCtx, docker.SupportingService{
			Name: "pocketid", Image: pocketIDImage, HostPort: 7081, ContainerPort: 1411,
			DataDir: filepath.Join(home, "services", "pocketid", "data"),
			Environment: map[string]string{
				"APP_URL": pocketIDURL, "ENCRYPTION_KEY": encryptionKey, "STATIC_API_KEY": apiKey,
				"ANALYTICS_DISABLED": "true", "VERSION_CHECK_DISABLED": "true",
				"ALLOW_OWN_ACCOUNT_EDIT": "false", "UI_CONFIG_DISABLED": "true",
			},
		})
		if err == nil {
			err = waitForPocketID(attemptCtx)
		}
		var clientID string
		if err == nil {
			clientID, err = idp.EnsureOIDCClient(attemptCtx, "http://localhost:7080/auth/callback", oidcSecret)
		}
		cancel()
		if err == nil {
			oidcConfig.Set(pocketIDURL, clientID, oidcSecret)
			log.Info("Pocket ID is ready for Homelab sign-in")
			return
		}
		if ctx.Err() != nil {
			return
		}
		log.Warn("Pocket ID is not ready; Homelab will keep serving and retry", "attempt", attempt, "retry_in", delay, "err", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func ensurePlatformSecret(secrets core.Secrets, name string) (string, error) {
	if value, ok := secrets.Platform(name); ok && value != "" {
		return value, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	if err := secrets.Set("", "", name, value, core.ActorHomelab); err != nil {
		return "", err
	}
	return value, nil
}

func waitForPocketID(ctx context.Context) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:7081/healthz", nil)
		if err == nil {
			res, e := client.Do(req)
			if e == nil {
				res.Body.Close()
				if res.StatusCode == http.StatusNoContent || res.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("Pocket ID didn't become ready in 30 seconds")
		case <-ticker.C:
		}
	}
}

// onThisComputer marks the request as local for Host checks. It does not grant a session.
func onThisComputer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(core.WithViewer(r.Context(), core.Viewer{Local: true})))
	})
}

// announceTailscale prints the companion's Tailscale address once it's ready.
func announceTailscale(ctx context.Context, addrs core.Addresses) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if a, ok := addrs.Observe()["homelab"]; ok && a.Ready && strings.TrimSpace(a.URL) != "" {
				fmt.Printf("Companion (Tailscale): %s\n", a.URL)
				return
			}
		}
	}
}

// identityKeyName is the platform secret holding the key that signs identity headers. Homelab
// makes it itself (S5); it's never listed in the companion.
const identityKeyName = "HOMELAB_IDENTITY_KEY"

// identitySigner loads the identity key, making one the first time.
func identitySigner(secrets core.Secrets) (*identity.Signer, error) {
	key, ok := secrets.Platform(identityKeyName)
	if !ok {
		k, err := identity.NewKey()
		if err != nil {
			return nil, err
		}
		if err := secrets.Set("", "", identityKeyName, k, core.ActorHomelab); err != nil {
			return nil, err
		}
		key = k
	}
	return identity.NewSigner(key)
}

// importSecretFiles moves milestone 1's plain secrets files into the encrypted store, once.
// Values already in the store win. Each imported file is renamed to <file>.imported, and
// the command to delete them is printed.
func importSecretFiles(dir string, secrets core.Secrets, out io.Writer) error {
	files, err := filesecrets.New(dir).Files()
	if err != nil {
		return err
	}
	var done []string
	for _, f := range files {
		have, err := secrets.List(f.App, f.Env)
		if err != nil {
			return err
		}
		stored := map[string]bool{}
		for _, i := range have {
			stored[i.Name] = true
		}
		n := 0
		for name, value := range f.Values {
			if value == "" || stored[name] {
				continue
			}
			if err := secrets.Set(f.App, f.Env, name, value, "imported from "+filepath.Base(f.Path)); err != nil {
				return fmt.Errorf("%s: %s: %w", filepath.Base(f.Path), name, err)
			}
			n++
		}
		if err := os.Rename(f.Path, f.Path+".imported"); err != nil {
			return err
		}
		fmt.Fprintf(out, "Moved %d secret(s) from %s into the encrypted store.\n", n, f.Path)
		done = append(done, f.Path+".imported")
	}
	if len(done) > 0 {
		fmt.Fprintf(out, "The old files aren't read any more. Once Homelab works, delete them:\n  rm %s\n", strings.Join(done, " "))
	}
	return nil
}
