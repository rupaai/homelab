package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rupaai/homelab/internal/adapters/docker"
	"github.com/rupaai/homelab/internal/adapters/github"
	"github.com/rupaai/homelab/internal/adapters/gitmirror"
	"github.com/rupaai/homelab/internal/adapters/httphealth"
	"github.com/rupaai/homelab/internal/adapters/local"
	"github.com/rupaai/homelab/internal/adapters/pocketid"
	"github.com/rupaai/homelab/internal/adapters/secretstore"
	"github.com/rupaai/homelab/internal/adapters/selfupdate"
	"github.com/rupaai/homelab/internal/adapters/services"
	"github.com/rupaai/homelab/internal/adapters/smtp"
	"github.com/rupaai/homelab/internal/adapters/sysinfo"
	"github.com/rupaai/homelab/internal/adapters/sysres"
	"github.com/rupaai/homelab/internal/adapters/webpush"
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
	pocketIDListen            = "127.0.0.1:7081"
	// The container listens here; Homelab serves pocketIDURL in front of it.
	pocketIDContainerURL  = "http://127.0.0.1:7082"
	pocketIDContainerPort = 7082
	pocketIDImage         = "ghcr.io/pocket-id/pocket-id:v2.18.0"
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
	if len(cfg.Unknown) > 0 {
		log.Warn("config.yaml has settings this version of Homelab doesn't know, so it skips them. Update Homelab to use them.", "settings", strings.Join(cfg.Unknown, ", "))
	}
	if err := checkListen(cfg); err != nil {
		return err
	}
	if selfupdate.Managed(home) {
		// Before opening any data: after a test version, put back the data from before it.
		if msg, err := selfupdate.FinishTest(home, time.Now()); err != nil {
			return fmt.Errorf("ending the test version: %w", err)
		} else if msg != "" {
			log.Info(msg)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, home, cfg, log)
}

func run(ctx context.Context, home string, cfg Config, log *slog.Logger) error {
	// Stopping (for an update) ends run normally; systemd then starts the new version.
	ctx, restart := context.WithCancel(ctx)
	defer restart()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Secrets live in homelab-services (encrypted there). homelabd makes the master key,
	// starts homelab-services with the key's PATH (never the key), and waits for it: nothing
	// below can start without its secrets, and nothing falls back to plain text.
	secretsDir := filepath.Join(home, "secrets")
	keyPath, oldStore := filepath.Join(secretsDir, "master.key"), filepath.Join(secretsDir, "secrets.db")
	servicesDir := filepath.Join(home, "services")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	if err := secretstore.EnsureMasterKey(keyPath, oldStore, filepath.Join(servicesDir, "secrets.db")); err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	storage := storageOrDefault(cfg, home)
	svc, err := services.New(services.Options{Program: services.Program(exe), DataDir: servicesDir, Port: cfg.ServicesPort,
		Storage: storage, MaxUpload: cfg.MaxUploadMB << 20, SecretsKey: keyPath, SecretsImport: oldStore, Logger: log})
	if err != nil {
		return err
	}
	secrets := svc.Secrets()
	// The GitHub token is a secret too: until the secrets service is up there is none.
	token := func() string { t, _ := secrets.Platform("GITHUB_TOKEN"); return t }
	updater := selfupdate.New(selfupdate.Options{Home: home, Version: version, Token: token, Logger: log, Restart: restart})
	svcCtx, stopServices := context.WithCancel(ctx)
	defer stopServices()
	svcDone := make(chan struct{})
	go func() {
		defer close(svcDone)
		// A version installed by an older Homelab came without homelab-services.
		if _, err := os.Stat(services.Program(exe)); err != nil && selfupdate.Managed(home) {
			if err := updater.FetchServices(svcCtx); err != nil {
				log.Warn("couldn't fetch homelab-services", "err", err)
			}
		}
		svc.Run(svcCtx)
	}()
	defer func() { stopServices(); <-svcDone }()
	if err := waitForSecrets(ctx, secrets, svc, secretsWait, log); err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
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
	signer, err := identitySigner(secrets)
	if err != nil {
		return fmt.Errorf("identity key: %w", err)
	}

	store, err := state.Open(filepath.Join(home, "homelab.db"))
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	defer store.Close()

	// With addresses: ip the app ports are open to other devices too; otherwise only this computer.
	bind := ""
	if cfg.Addresses == "ip" {
		bind = "0.0.0.0"
	}
	addrs, err := local.New(local.Options{Dir: filepath.Join(home, "local"), Bind: bind, CompanionPort: localCompanionPort, Logger: log})
	if err != nil {
		return fmt.Errorf("local addresses: %w", err)
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
	if token() == "" {
		log.Warn("GITHUB_TOKEN is not set: private repos and ghcr.io images won't work. Set it in the companion under Settings")
	}
	gh := github.New(github.Options{TokenFunc: token, Logger: log})
	mirror, err := gitmirror.New(gitmirror.Options{Dir: filepath.Join(home, "mirrors"), Token: token, Logger: log})
	if err != nil {
		return fmt.Errorf("mirrors: %w", err)
	}

	// Email reads its address and app password from Settings for each message.
	mailer := smtp.New(smtp.Options{Logger: log, Credentials: func() (string, string) {
		a, _ := secrets.Platform(hub.EmailAddressSetting)
		p, _ := secrets.Platform(hub.EmailPasswordSetting)
		return a, p
	}})

	var notifier core.Notifier
	pushPrivate, pushPublic, err := pushKeys(secrets)
	if err == nil {
		notifier, err = webpush.New(webpush.Options{PublicKey: pushPublic, PrivateKey: pushPrivate})
	}
	if err != nil {
		log.Warn("push notifications are off", "err", err)
		notifier = nil
	}
	resources := sysres.New(sysres.Options{Logger: log})
	appAPIURL := "http://host.docker.internal:" + strconv.Itoa(cfg.AppAPIPort)
	h := hub.New(hub.Config{DataDir: home, Logger: log, Identity: signer, PushPublicKey: pushPublic,
		AppAPIURL: appAPIURL, OpenApps: cfg.Addresses == "ip", Folders: cfg.Folders, Storage: storage}, store, hub.Adapters{
		Source: gh, Builder: gh, Registry: gh, Deployer: deployer,
		Addresses: addrs, Health: httphealth.New(nil), Secrets: secrets,
		Smoke: deployer, Mirror: mirror, Identity: idp, Email: mailer,
		Notifier: notifier, Resources: resources, System: sysinfo.New(sysinfo.Options{}), Updater: updater,
		Services: svc, Databases: svc.Databases(), Versions: svc.Versions(),
	})

	oidcConfig := api.NewOIDCConfig("", "", "")
	apiHandler := api.New(h, api.Options{Version: version, Static: companion.Site(), Logger: log, Store: store,
		OIDCConfig: oidcConfig, PasswordFile: passwordFile(home), Services: svc})

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w (is Homelab already running?)", cfg.Listen, err)
	}
	srv := &http.Server{
		Handler:           onThisComputer(apiHandler, cfg.Addresses == "ip"),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	doorSrv := &http.Server{
		Handler:           api.Door(h, api.Options{Version: version, Logger: log, Store: store, Services: svc}),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	go openAppDoor(ctx, doorSrv, deployer, cfg.AppAPIPort, log)
	_, listenPort, _ := net.SplitHostPort(ln.Addr().String())
	fmt.Printf("Companion: http://localhost:%s\n", listenPort)
	if cfg.Addresses == "ip" {
		fmt.Printf("Companion on other devices: http://<this computer's IP>:%s (sign in with the password; set it with `homelabd password`)\n", listenPort)
	}
	go configurePocketID(ctx, deployer, idp, apiKey, encryptionKey, oidcSecret, oidcConfig, log, home)

	if selfupdate.Managed(home) {
		go confirmUpdate(ctx, home, h, log)
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
	_ = doorSrv.Shutdown(stopCtx)
	stopServices()
	<-svcDone
	<-hubDone
	// Let a promote or rollback that is running finish, so it isn't cut off halfway.
	idleCtx, idleCancel := context.WithTimeout(context.Background(), actionWait)
	defer idleCancel()
	h.WaitIdle(idleCtx)
	return result
}

// actionWait is how long a shutdown waits for running actions before it gives up.
const actionWait = 2 * time.Minute

// secretsWait is how long a start waits for the secrets service. A first start after an
// update may have to fetch homelab-services first.
const secretsWait = 3 * time.Minute

// waitForSecrets waits until secrets work. A service that is up but can't use secrets (no
// key, the wrong key) is a problem waiting won't fix, so that stops the start with the reason.
func waitForSecrets(ctx context.Context, secrets *services.Secrets, svc *services.Services, wait time.Duration, log *slog.Logger) error {
	deadline := time.Now().Add(wait)
	warned := false
	for {
		err := secrets.Check()
		if err == nil {
			return nil
		}
		if services.IsPermanent(err) || !errors.Is(err, core.ErrSecretsUnavailable) {
			return err
		}
		if time.Now().After(deadline) {
			if p := svc.Problem(); p != "" {
				return fmt.Errorf("%w (%s)", core.ErrSecretsUnavailable, p)
			}
			return err
		}
		if !warned {
			log.Info("waiting for Homelab's secrets service to start")
			warned = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// confirmUpdate marks a just-installed version as good once it has kept running a while.
func confirmUpdate(ctx context.Context, home string, h *hub.Hub, log *slog.Logger) {
	bin := selfupdate.BinDir(home)
	if !selfupdate.Pending(bin) {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(selfupdate.ProveAfter):
	}
	ok, err := selfupdate.Confirm(bin, version, time.Now())
	if err != nil {
		log.Warn("couldn't confirm the update", "err", err)
		return
	}
	if ok {
		log.Info("update confirmed", "version", version)
		h.RecordUpdate(ctx, "Homelab is now on "+version)
	}
}

func configurePocketID(ctx context.Context, deployer *docker.Deployer, idp *pocketid.Client, apiKey, encryptionKey, oidcSecret string, oidcConfig *api.OIDCConfig, log *slog.Logger, home string) {
	delay := time.Second
	proxying := false
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := deployer.EnsureSupportingService(attemptCtx, docker.SupportingService{
			Name: "pocketid", Image: pocketIDImage, HostPort: pocketIDContainerPort, ContainerPort: 1411,
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
		// Listen only after the container has moved off pocketIDListen; older
		// Homelab versions published Pocket ID there directly.
		if err == nil && !proxying {
			err = servePocketIDProxy(ctx, log)
			proxying = err == nil
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

// pushKeys returns Homelab's web push keys, making them on first start (O4).
func pushKeys(secrets core.Secrets) (private, public string, err error) {
	if private, err = secrets.LoadPlatform(pushPrivateKeyName); err != nil {
		return "", "", err
	}
	if public, err = secrets.LoadPlatform(pushPublicKeyName); err != nil {
		return "", "", err
	}
	if private != "" && public != "" {
		return private, public, nil
	}
	if private, public, err = webpush.GenerateKeys(); err != nil {
		return "", "", err
	}
	if err := secrets.Set("", "", pushPrivateKeyName, private, core.ActorHomelab); err != nil {
		return "", "", err
	}
	if err := secrets.Set("", "", pushPublicKeyName, public, core.ActorHomelab); err != nil {
		return "", "", err
	}
	return private, public, nil
}

const (
	pushPrivateKeyName = "HOMELAB_PUSH_PRIVATE_KEY"
	pushPublicKeyName  = "HOMELAB_PUSH_PUBLIC_KEY"
)

func ensurePlatformSecret(secrets core.Secrets, name string) (string, error) {
	value, err := secrets.LoadPlatform(name)
	if err != nil {
		return "", fmt.Errorf("can't read %s from the secret store, so Homelab won't make a new one: %w", name, err)
	}
	if value != "" {
		return value, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	value = base64.RawURLEncoding.EncodeToString(raw)
	if err := secrets.Set("", "", name, value, core.ActorHomelab); err != nil {
		return "", err
	}
	return value, nil
}

// servePocketIDProxy serves Pocket ID at pocketIDURL until ctx ends.
func servePocketIDProxy(ctx context.Context, log *slog.Logger) error {
	target, err := url.Parse(pocketIDContainerURL)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", pocketIDListen)
	if err != nil {
		return fmt.Errorf("serve Pocket ID on %s: %w", pocketIDListen, err)
	}
	srv := &http.Server{
		Handler:           pocketid.Proxy(target),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	go func() {
		<-ctx.Done()
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(stopCtx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("Pocket ID proxy stopped", "err", err)
		}
	}()
	return nil
}

func waitForPocketID(ctx context.Context) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pocketIDContainerURL+"/healthz", nil)
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
// With network set (addresses: ip), a request from another device is marked as coming over
// the network instead, so it signs in with the password.
func onThisComputer(next http.Handler, network bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := core.Viewer{Local: true}
		if network && !fromLoopback(r.RemoteAddr) {
			v = core.Viewer{Network: true}
		}
		next.ServeHTTP(w, r.WithContext(core.WithViewer(r.Context(), v)))
	})
}

func fromLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// identityKeyName is the platform secret holding the key that signs identity headers. Homelab
// makes it itself (S5); it's never listed in the companion.
const identityKeyName = "HOMELAB_IDENTITY_KEY"

// identitySigner loads the identity key, making one the first time.
func identitySigner(secrets core.Secrets) (*identity.Signer, error) {
	key, err := secrets.LoadPlatform(identityKeyName)
	if err != nil {
		return nil, fmt.Errorf("can't read the identity key from the secret store, so Homelab won't make a new one: %w", err)
	}
	if key == "" {
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

// openAppDoor opens the app door, trying again until it can (Docker may still be starting
// at boot), so the companion never waits for it. On the Mac, Docker runs in a small
// virtual machine that already forwards host.docker.internal to this computer's
// 127.0.0.1. On Linux, the door listens on Docker's bridge address. Apps reach it there;
// anything else needs a route to that address and still needs an app key.
func openAppDoor(ctx context.Context, srv *http.Server, d *docker.Deployer, port int, log *slog.Logger) {
	p := strconv.Itoa(port)
	for warned := false; ; {
		addr := net.JoinHostPort("127.0.0.1", p)
		var err error
		if runtime.GOOS != "darwin" {
			var gw string
			if gw, err = d.BridgeGateway(ctx); err == nil {
				addr = net.JoinHostPort(gw, p)
			}
		}
		var ln net.Listener
		if err == nil {
			ln, err = net.Listen("tcp", addr)
		}
		if err == nil {
			log.Info("app door open", "listen", ln.Addr().String())
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("app door stopped", "err", err)
			}
			return
		}
		if !warned {
			log.Warn("apps can't call the Homelab API yet: the app door couldn't open; trying again", "err", err)
			warned = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}
