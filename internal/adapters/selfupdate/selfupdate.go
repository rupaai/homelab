package selfupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

const (
	defaultRepo = "rupaai/homelab"
	defaultAPI  = "https://api.github.com"
	// checkEvery is how long an answer about the newest release is trusted.
	checkEvery = 6 * time.Hour
	// maxProgram is the largest program Homelab downloads.
	maxProgram = 300 << 20
	checksums  = "checksums.txt"
)

// AssetName is the release file holding the program for this kind of computer.
func AssetName(goos, goarch string) string { return "homelabd-" + goos + "-" + goarch }

// Options configures an Updater.
type Options struct {
	Home    string        // the Homelab folder
	Version string        // the running version
	Repo    string        // default rupaai/homelab
	API     string        // default https://api.github.com
	Token   func() string // GitHub token; the repo may be private
	HTTP    *http.Client  // default: 60s timeout
	// Restart stops Homelab so systemd starts it again on the new version.
	Restart func()
	Logger  *slog.Logger
	Now     func() time.Time
}

// Updater is the core.Updater implementation.
type Updater struct {
	o Options

	mu        sync.Mutex
	latest    release
	checkedAt time.Time
	applying  bool
}

var _ core.Updater = (*Updater)(nil)

// New returns an Updater.
func New(o Options) *Updater {
	if o.Repo == "" {
		o.Repo = defaultRepo
	}
	if o.API == "" {
		o.API = defaultAPI
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if o.Token == nil {
		o.Token = func() string { return "" }
	}
	if o.Restart == nil {
		o.Restart = func() {}
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Updater{o: o}
}

type release struct {
	Tag    string  `json:"tag_name"`
	Assets []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"url"` // API address; with Accept: application/octet-stream it downloads
}

func (r release) asset(name string) (asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return asset{}, false
}

// Status reports the running and newest versions.
func (u *Updater) Status(ctx context.Context) (core.UpdateStatus, error) {
	s := core.UpdateStatus{Current: u.o.Version, Managed: Managed(u.o.Home)}
	bin := BinDir(u.o.Home)
	if r, ok := LastResult(bin); ok {
		s.LastResult = r.Message()
	}
	if !s.Managed {
		s.Note = "This Homelab wasn't installed with `homelabd setup`, so it doesn't update itself. Update it with `git pull && make build`."
		return s, nil
	}
	if Pending(bin) {
		s.Note = "Homelab is checking that the new version stays up."
	}
	u.mu.Lock()
	stale := u.o.Now().Sub(u.checkedAt) > checkEvery
	u.mu.Unlock()
	if stale {
		if _, err := u.check(ctx); err != nil {
			u.o.Logger.Warn("couldn't check for a Homelab update", "err", err)
			s.Note = "Couldn't check for a new version: " + err.Error()
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.checkedAt.IsZero() {
		t := u.checkedAt
		s.CheckedAt = &t
		s.Latest = u.latest.Tag
		s.Available = Newer(u.latest.Tag, u.o.Version)
	}
	return s, nil
}

// check asks GitHub for the newest release and remembers it.
func (u *Updater) check(ctx context.Context) (release, error) {
	var r release
	body, err := u.get(ctx, u.o.API+"/repos/"+u.o.Repo+"/releases/latest", "application/vnd.github+json", 1<<20)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return r, fmt.Errorf("GitHub's answer about releases was unreadable: %w", err)
	}
	u.mu.Lock()
	u.latest, u.checkedAt = r, u.o.Now()
	u.mu.Unlock()
	return r, nil
}

// Apply downloads the newest release, checks it, switches to it and restarts Homelab.
func (u *Updater) Apply(ctx context.Context) (string, error) {
	if !Managed(u.o.Home) {
		return "", errors.New("this Homelab wasn't installed with `homelabd setup`, so it can't update itself")
	}
	bin := BinDir(u.o.Home)
	if Pending(bin) {
		return "", errors.New("the last update is still being checked; try again in a few minutes")
	}
	u.mu.Lock()
	if u.applying {
		u.mu.Unlock()
		return "", errors.New("an update is already being installed")
	}
	u.applying = true
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.applying = false; u.mu.Unlock() }()

	r, err := u.check(ctx)
	if err != nil {
		return "", err
	}
	if !Newer(r.Tag, u.o.Version) {
		return "", fmt.Errorf("Homelab is already on the newest version (%s)", u.o.Version)
	}
	name := AssetName(runtime.GOOS, runtime.GOARCH)
	prog, ok := r.asset(name)
	if !ok {
		return "", fmt.Errorf("release %s has no %s", r.Tag, name)
	}
	sums, ok := r.asset(checksums)
	if !ok {
		return "", fmt.Errorf("release %s has no %s", r.Tag, checksums)
	}
	sumText, err := u.get(ctx, sums.URL, "application/octet-stream", 1<<20)
	if err != nil {
		return "", err
	}
	want, err := checksumFor(sumText, name)
	if err != nil {
		return "", err
	}
	u.o.Logger.Info("downloading Homelab update", "version", r.Tag)
	data, err := u.get(ctx, prog.URL, "application/octet-stream", maxProgram)
	if err != nil {
		return "", err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("the download of %s didn't match its checksum, so it wasn't installed", r.Tag)
	}
	path, err := Install(bin, r.Tag, data)
	if err != nil {
		return "", err
	}
	if err := checkProgram(ctx, path, r.Tag); err != nil {
		return "", err
	}
	if err := Switch(bin, r.Tag, u.o.Now()); err != nil {
		return "", err
	}
	u.o.Logger.Info("restarting on the new version", "version", r.Tag)
	// Give the request that asked for this a moment to get its answer.
	time.AfterFunc(time.Second, u.o.Restart)
	return r.Tag, nil
}

// checkProgram runs the new program's version command to make sure it runs here at all.
func checkProgram(ctx context.Context, path, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return fmt.Errorf("the new version doesn't run on this device: %w", err)
	}
	if !strings.Contains(string(out), version) {
		return fmt.Errorf("the new program says it is %q, not %s", strings.TrimSpace(string(out)), version)
	}
	return nil
}

// checksumFor finds name's SHA-256 in a sha256sum-style file.
func checksumFor(text []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(text))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s has no checksum for %s", checksums, name)
}

// get fetches url from GitHub with the token, reading at most limit bytes.
func (u *Updater) get(ctx context.Context, url, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if t := u.o.Token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	// Downloads redirect to storage on another host; Go drops the token there.
	res, err := u.o.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach GitHub: %w", err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return nil, errors.New("GitHub has no release of Homelab yet, or the GitHub token in Settings can't read the homelab repo")
	case res.StatusCode == http.StatusUnauthorized:
		return nil, errors.New("GitHub refused the token in Settings")
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub answered %s", res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download from GitHub broke off: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, errors.New("the download from GitHub is bigger than expected")
	}
	return b, nil
}
