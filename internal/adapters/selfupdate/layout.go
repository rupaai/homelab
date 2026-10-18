// Package selfupdate lets a Homelab installed by `homelabd setup` update itself from the
// releases of its GitHub repo, and go back to the previous version on its own when a new
// one doesn't stay up (milestone 6). It implements core.Updater.
//
// The layout of an installed Homelab, in <home>/bin:
//
//	homelabd             symlink to the version that runs (systemd starts this)
//	homelabd-<version>   every version installed so far (the last few are kept)
//	previous             symlink to the version that ran before the last update
//	update-pending.json  an update that hasn't proven itself yet
//	update-result.json   how the last update went
//
// systemd runs `homelabd update-guard` (from a copy outside this folder) before every
// start. Each start of an unproven version counts as a try; after maxTries the guard points
// homelabd back at previous. A version proves itself by running for ProveAfter, and then
// Confirm forgets the pending update.
package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	current     = "homelabd"
	previous    = "previous"
	pendingFile = "update-pending.json"
	resultFile  = "update-result.json"
	// maxTries is how many starts a new version gets before the guard gives up on it.
	maxTries = 3
	// keepVersions is how many installed versions stay in bin.
	keepVersions = 3
)

// ProveAfter is how long a new version must keep running before it counts as good.
const ProveAfter = 2 * time.Minute

// BinDir is where an installed Homelab keeps its program.
func BinDir(home string) string { return filepath.Join(home, "bin") }

// Managed says whether the Homelab in home was installed by `homelabd setup` and so can
// update itself. On a Mac people update with `git pull && make build`.
func Managed(home string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	st, err := os.Lstat(filepath.Join(BinDir(home), current))
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

// VersionFile is the file name a version is installed under.
func VersionFile(version string) string { return current + "-" + version }

// checkVersion refuses version strings that could escape bin.
func checkVersion(v string) error {
	if v == "" || len(v) > 64 || strings.ContainsAny(v, `/\`) || strings.HasPrefix(v, ".") {
		return fmt.Errorf("%q isn't a version Homelab can install", v)
	}
	return nil
}

// Install writes a version's program into bin without switching to it.
func Install(binDir, version string, program []byte) (string, error) {
	if err := checkVersion(version); err != nil {
		return "", err
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(binDir, VersionFile(version))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, program, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// Running is the version homelabd points at, from its symlink.
func Running(binDir string) (string, error) {
	target, err := os.Readlink(filepath.Join(binDir, current))
	if err != nil {
		return "", err
	}
	v, ok := strings.CutPrefix(filepath.Base(target), current+"-")
	if !ok {
		return "", fmt.Errorf("%s points at %s, which isn't an installed version", current, target)
	}
	return v, nil
}

// pending is an update that hasn't proven itself yet.
type pending struct {
	From  string    `json:"from"`
	To    string    `json:"to"`
	Tries int       `json:"tries"`
	At    time.Time `json:"at"`
}

// Result is how the last update went.
type Result struct {
	From string    `json:"from"`
	To   string    `json:"to"`
	OK   bool      `json:"ok"`
	At   time.Time `json:"at"`
}

// Message is the result in words.
func (r Result) Message() string {
	if r.OK {
		return "Updated to " + r.To + "."
	}
	return r.To + " didn't stay up, so Homelab went back to " + r.From + "."
}

// Switch makes version the one that runs from the next start, remembering the current one
// as previous and marking the update as pending.
func Switch(binDir, version string, now time.Time) error {
	if err := checkVersion(version); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(binDir, VersionFile(version))); err != nil {
		return fmt.Errorf("%s isn't installed: %w", version, err)
	}
	from, err := Running(binDir)
	if err != nil {
		return err
	}
	if err := relink(binDir, previous, VersionFile(from)); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(binDir, pendingFile), pending{From: from, To: version, At: now}); err != nil {
		return err
	}
	return relink(binDir, current, VersionFile(version))
}

// Guard runs before every start. It counts a start of an unproven version and, after
// maxTries, goes back to the previous version. It says what it did, or "" when nothing.
func Guard(binDir string, now time.Time) (string, error) {
	var p pending
	if err := readJSON(filepath.Join(binDir, pendingFile), &p); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		// An unreadable marker can't be trusted to count tries; drop it rather than loop.
		os.Remove(filepath.Join(binDir, pendingFile))
		return "", fmt.Errorf("unreadable %s, removed it: %w", pendingFile, err)
	}
	if running, err := Running(binDir); err != nil || running != p.To {
		// Someone switched by hand (for example by running setup again); not ours to judge.
		return "", os.Remove(filepath.Join(binDir, pendingFile))
	}
	p.Tries++
	if p.Tries <= maxTries {
		if err := writeJSON(filepath.Join(binDir, pendingFile), p); err != nil {
			return "", err
		}
		return fmt.Sprintf("starting %s (try %d of %d)", p.To, p.Tries, maxTries), nil
	}
	if err := relink(binDir, current, VersionFile(p.From)); err != nil {
		return "", err
	}
	if err := writeJSON(filepath.Join(binDir, resultFile), Result{From: p.From, To: p.To, OK: false, At: now}); err != nil {
		return "", err
	}
	if err := os.Remove(filepath.Join(binDir, pendingFile)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s didn't stay up after %d tries; went back to %s", p.To, maxTries, p.From), nil
}

// Confirm marks a pending update to running as good. It is a no-op without one.
func Confirm(binDir, running string, now time.Time) (bool, error) {
	var p pending
	if err := readJSON(filepath.Join(binDir, pendingFile), &p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if p.To != running {
		return false, nil
	}
	if err := writeJSON(filepath.Join(binDir, resultFile), Result{From: p.From, To: p.To, OK: true, At: now}); err != nil {
		return false, err
	}
	if err := os.Remove(filepath.Join(binDir, pendingFile)); err != nil {
		return false, err
	}
	prune(binDir)
	return true, nil
}

// Pending says whether an update hasn't proven itself yet.
func Pending(binDir string) bool {
	_, err := os.Stat(filepath.Join(binDir, pendingFile))
	return err == nil
}

// LastResult is how the last update went, if there was one.
func LastResult(binDir string) (Result, bool) {
	var r Result
	if err := readJSON(filepath.Join(binDir, resultFile), &r); err != nil {
		return r, false
	}
	return r, true
}

// prune deletes old versions, keeping keepVersions plus whatever homelabd and previous
// point at.
func prune(binDir string) {
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, l := range []string{current, previous} {
		if t, err := os.Readlink(filepath.Join(binDir, l)); err == nil {
			keep[filepath.Base(t)] = true
		}
	}
	type version struct {
		name string
		n    int
	}
	var vs []version
	for _, e := range entries {
		v, ok := strings.CutPrefix(e.Name(), current+"-")
		if !ok || e.Type()&os.ModeSymlink != 0 || strings.HasSuffix(v, ".tmp") {
			continue
		}
		n, _ := BuildNumber(v)
		vs = append(vs, version{e.Name(), n})
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].n > vs[j].n })
	for i, v := range vs {
		if i >= keepVersions && !keep[v.name] {
			os.Remove(filepath.Join(binDir, v.name))
		}
	}
}

// BuildNumber reads the number of a release version such as "build-12".
func BuildNumber(v string) (int, bool) {
	s, ok := strings.CutPrefix(v, "build-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

// Newer says whether latest should replace current. A Homelab built by hand (not a
// numbered release) always takes a release.
func Newer(latest, current string) bool {
	l, ok := BuildNumber(latest)
	if !ok {
		return false
	}
	c, ok := BuildNumber(current)
	return !ok || l > c
}

// relink points binDir/name at target in one step.
func relink(binDir, name, target string) error {
	tmp := filepath.Join(binDir, "."+name+".tmp")
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(binDir, name))
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// Activate makes an installed version the one that runs, as `homelabd setup` does: the
// version that ran before becomes previous, and no update is pending.
func Activate(binDir, version string) error {
	if err := checkVersion(version); err != nil {
		return err
	}
	if old, err := Running(binDir); err == nil && old != version {
		if err := relink(binDir, previous, VersionFile(old)); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(binDir, pendingFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return relink(binDir, current, VersionFile(version))
}
