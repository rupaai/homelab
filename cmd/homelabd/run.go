package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	"github.com/rupaai/homelab/internal/adapters/httphealth"
	"github.com/rupaai/homelab/internal/adapters/local"
	"github.com/rupaai/homelab/internal/adapters/tailscale"
	"github.com/rupaai/homelab/internal/api"
	"github.com/rupaai/homelab/internal/companion"
	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
	"github.com/rupaai/homelab/internal/state"
)

// localCompanionPort is the port the local adapter would give the name "homelab". Homelab
// never asks for that name in local mode (the listen address serves the companion), so
// this just keeps it out of the way of the real listen port.
const localCompanionPort = 7079

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
	secrets := filesecrets.New(filepath.Join(home, "secrets"))

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
	token, ok := secrets.Platform("GITHUB_TOKEN")
	if !ok || token == "" {
		log.Warn("GITHUB_TOKEN is not set in secrets/platform.env; private repos and ghcr.io images won't work")
	}
	gh := github.New(github.Options{Token: token, Logger: log})

	h := hub.New(hub.Config{DataDir: home, Logger: log}, store, hub.Adapters{
		Source: gh, Builder: gh, Registry: gh, Deployer: deployer,
		Addresses: addrs, Health: httphealth.New(nil), Secrets: secrets,
	})

	apiHandler := api.New(h, api.Options{Owners: cfg.Owners, Version: version, Static: companion.Site(), Logger: log})

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
	fmt.Printf("Companion: http://%s\n", ln.Addr())

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

// onThisComputer marks every request on the loopback listener as coming from the owner
// at this computer. Only use it on a loopback listener (checkLoopback).
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
