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
	"net/url"
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
	Tag        string  `json:"tag_name"`
	Name       string  `json:"name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Assets     []asset `json:"assets"`
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
	if t, ok := ReadTest(bin); ok {
		s.Testing = &core.TestRun{TestVersion: core.TestVersion{Version: t.Version, PR: t.PR, Title: t.Title}, From: t.From, At: t.At}
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
// It refuses while a test version runs: a failed update would go back to the test build
// and lose the data saved before the test.
func (u *Updater) Apply(ctx context.Context) (string, error) {
	done, err := u.begin()
	if err != nil {
		return "", err
	}
	defer done()
	if t, ok := ReadTest(BinDir(u.o.Home)); ok {
		return "", fmt.Errorf("PR #%d's test version is still running; go back or keep it before updating", t.PR)
	}
	r, err := u.check(ctx)
	if err != nil {
		return "", err
	}
	if !Newer(r.Tag, u.o.Version) {
		return "", fmt.Errorf("Homelab is already on the newest version (%s)", u.o.Version)
	}
	if err := u.install(ctx, r); err != nil {
		return "", err
	}
	if err := Switch(BinDir(u.o.Home), r.Tag, u.o.Now()); err != nil {
		return "", err
	}
	u.restart(r.Tag)
	return r.Tag, nil
}

// Tests lists the test versions: pre-releases named pr-<number>-<commit> that have a
// program for this computer.
func (u *Updater) Tests(ctx context.Context) ([]core.TestVersion, error) {
	body, err := u.get(ctx, u.o.API+"/repos/"+u.o.Repo+"/releases?per_page=50", "application/vnd.github+json", 4<<20)
	if err != nil {
		return nil, err
	}
	var rs []release
	if err := json.Unmarshal(body, &rs); err != nil {
		return nil, fmt.Errorf("GitHub's answer about releases was unreadable: %w", err)
	}
	tests := []core.TestVersion{}
	for _, r := range rs {
		pr, ok := testRelease(r)
		if !ok {
			continue
		}
		if _, ok := r.asset(AssetName(runtime.GOOS, runtime.GOARCH)); ok {
			tests = append(tests, core.TestVersion{Version: r.Tag, PR: pr, Title: r.Name})
		}
	}
	return tests, nil
}

func testRelease(r release) (int, bool) {
	if !r.Prerelease || r.Draft {
		return 0, false
	}
	return TestPR(r.Tag)
}

// Try saves Homelab's data, installs a test version and restarts on it.
func (u *Updater) Try(ctx context.Context, version string) (core.TestVersion, error) {
	var tv core.TestVersion
	if _, ok := TestPR(version); !ok {
		return tv, fmt.Errorf("%q isn't a test version", version)
	}
	done, err := u.begin()
	if err != nil {
		return tv, err
	}
	defer done()
	bin := BinDir(u.o.Home)
	if t, ok := ReadTest(bin); ok {
		return tv, fmt.Errorf("PR #%d's test version is still running; go back or keep it first", t.PR)
	}
	body, err := u.get(ctx, u.o.API+"/repos/"+u.o.Repo+"/releases/tags/"+url.PathEscape(version), "application/vnd.github+json", 1<<20)
	if err != nil {
		return tv, err
	}
	var r release
	if err := json.Unmarshal(body, &r); err != nil {
		return tv, fmt.Errorf("GitHub's answer about %s was unreadable: %w", version, err)
	}
	pr, ok := testRelease(r)
	if !ok || r.Tag != version {
		return tv, fmt.Errorf("%s isn't a test version on GitHub", version)
	}
	tv = core.TestVersion{Version: r.Tag, PR: pr, Title: r.Name}
	if err := u.install(ctx, r); err != nil {
		return tv, err
	}
	if err := SaveData(ctx, u.o.Home); err != nil {
		return tv, err
	}
	if err := StartTest(bin, Test{Version: tv.Version, PR: tv.PR, Title: tv.Title}, u.o.Now()); err != nil {
		return tv, err
	}
	u.restart(r.Tag)
	return tv, nil
}

// GoBack ends a test: Homelab restarts on the version from before, which puts the saved
// data back as it starts.
func (u *Updater) GoBack(context.Context) (string, error) {
	if !Managed(u.o.Home) {
		return "", errors.New("this Homelab wasn't installed with `homelabd setup`")
	}
	// Going back is allowed while the test version is still proving itself.
	u.mu.Lock()
	if u.applying {
		u.mu.Unlock()
		return "", errors.New("an update is already being installed")
	}
	u.mu.Unlock()
	t, err := GoBack(BinDir(u.o.Home))
	if err != nil {
		return "", err
	}
	u.restart(t.From)
	return t.From, nil
}

// Keep ends a test on the test version and deletes the saved data.
func (u *Updater) Keep(context.Context) error {
	if !Managed(u.o.Home) {
		return errors.New("this Homelab wasn't installed with `homelabd setup`")
	}
	// Until the test version has proven itself, the guard may still go back to the old
	// version, which needs the saved data.
	if Pending(BinDir(u.o.Home)) {
		return errors.New("the test version is still being checked; keep it in a few minutes")
	}
	_, err := Keep(BinDir(u.o.Home))
	return err
}

// begin claims the right to change the program; done gives it back. Only one change runs
// at a time, and none while the last one is still proving itself.
func (u *Updater) begin() (done func(), err error) {
	if !Managed(u.o.Home) {
		return nil, errors.New("this Homelab wasn't installed with `homelabd setup`, so it can't update itself")
	}
	if Pending(BinDir(u.o.Home)) {
		return nil, errors.New("the last update is still being checked; try again in a few minutes")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.applying {
		return nil, errors.New("an update is already being installed")
	}
	u.applying = true
	return func() { u.mu.Lock(); u.applying = false; u.mu.Unlock() }, nil
}

// install downloads a release's program for this computer, checks it against the
// release's checksums and puts it into bin without switching to it.
func (u *Updater) install(ctx context.Context, r release) error {
	name := AssetName(runtime.GOOS, runtime.GOARCH)
	prog, ok := r.asset(name)
	if !ok {
		return fmt.Errorf("release %s has no %s", r.Tag, name)
	}
	sums, ok := r.asset(checksums)
	if !ok {
		return fmt.Errorf("release %s has no %s", r.Tag, checksums)
	}
	sumText, err := u.get(ctx, sums.URL, "application/octet-stream", 1<<20)
	if err != nil {
		return err
	}
	want, err := checksumFor(sumText, name)
	if err != nil {
		return err
	}
	u.o.Logger.Info("downloading Homelab", "version", r.Tag)
	data, err := u.get(ctx, prog.URL, "application/octet-stream", maxProgram)
	if err != nil {
		return err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("the download of %s didn't match its checksum, so it wasn't installed", r.Tag)
	}
	path, err := Install(BinDir(u.o.Home), r.Tag, data)
	if err != nil {
		return err
	}
	return checkProgram(ctx, path, r.Tag)
}

// restart stops Homelab so systemd starts it on the version homelabd now points at.
func (u *Updater) restart(version string) {
	u.o.Logger.Info("restarting on another version", "version", version)
	// Give the request that asked for this a moment to get its answer.
	time.AfterFunc(time.Second, u.o.Restart)
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
