package selfupdate

// Test versions: builds of open pull requests that can be tried on the device. Before
// switching, Homelab copies its own data into bin/test-saved; going back puts the old
// version and that copy back. While a test runs, bin also holds:
//
//	test.json    the test version and the version to go back to
//	test-saved/  Homelab's data from before the test
//
// The copy is restored by the old version when it starts (FinishTest), so it works the
// same whether people go back from the companion or the update guard goes back on its own.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const (
	testFile  = "test.json"
	testSaved = "test-saved"
	// testPrefix starts the version of every test build: pr-<number>-<commit> (or v<x.y.z>-pr.<number>[.<commit>]).
	testPrefix = "pr-"
)

// dataFiles are the databases that hold Homelab's own data, relative to its folder. Apps
// and Pocket ID keep their data elsewhere, and a test doesn't touch it.
var dataFiles = []string{"homelab.db", filepath.Join("secrets", "secrets.db")}

// Test is the test version that runs now.
type Test struct {
	Version string    `json:"version"`
	PR      int       `json:"pr"`
	Title   string    `json:"title"`
	From    string    `json:"from"`
	At      time.Time `json:"at"`
}

// TestPR reads the pull request number from a test version such as "pr-12-1a2b3c4" or
// "v0.20.0-pr.12.1a2b3c4".
func TestPR(v string) (int, bool) { return testPR(v) }

// ReadTest is the test that runs now, if there is one.
func ReadTest(binDir string) (Test, bool) {
	var t Test
	if err := readJSON(filepath.Join(binDir, testFile), &t); err != nil {
		return t, false
	}
	return t, true
}

// SaveData copies Homelab's databases into bin/test-saved. VACUUM INTO makes a consistent
// copy while Homelab keeps running.
func SaveData(ctx context.Context, home string) error {
	dir := filepath.Join(BinDir(home), testSaved)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, f := range dataFiles {
		src := filepath.Join(home, f)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := copyDB(ctx, src, filepath.Join(dir, filepath.Base(f))); err != nil {
			return fmt.Errorf("couldn't save a copy of %s: %w", f, err)
		}
	}
	return nil
}

func copyDB(ctx context.Context, src, dst string) error {
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", dst)
	return err
}

// StartTest switches to an installed test version (as Switch does) and remembers the test.
func StartTest(binDir string, t Test, now time.Time) error {
	if _, ok := TestPR(t.Version); !ok {
		return fmt.Errorf("%q isn't a test version", t.Version)
	}
	from, err := Running(binDir)
	if err != nil {
		return err
	}
	t.From, t.At = from, now
	if err := writeJSON(filepath.Join(binDir, testFile), t); err != nil {
		return err
	}
	if err := Switch(binDir, t.Version, now); err != nil {
		os.Remove(filepath.Join(binDir, testFile))
		return err
	}
	return nil
}

// GoBack points homelabd at the version from before the test. The data comes back when
// that version starts (FinishTest).
func GoBack(binDir string) (Test, error) {
	t, ok := ReadTest(binDir)
	if !ok {
		return t, errors.New("no test version is running")
	}
	if _, err := os.Stat(filepath.Join(binDir, VersionFile(t.From))); err != nil {
		return t, fmt.Errorf("%s is no longer installed: %w", t.From, err)
	}
	if err := os.Remove(filepath.Join(binDir, pendingFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return t, err
	}
	return t, relink(binDir, current, VersionFile(t.From))
}

// Keep ends the test on the test version: its data stays and the saved copy is deleted.
func Keep(binDir string) (Test, error) {
	t, ok := ReadTest(binDir)
	if !ok {
		return t, errors.New("no test version is running")
	}
	if err := os.RemoveAll(filepath.Join(binDir, testSaved)); err != nil {
		return t, err
	}
	return t, os.Remove(filepath.Join(binDir, testFile))
}

// FinishTest runs when Homelab starts, before it opens its data. If a test was running and
// Homelab is back on the version from before it, the saved data is put back. If Homelab
// moved on to another version, the test is simply over. It says what it did, or "".
func FinishTest(home string, now time.Time) (string, error) {
	bin := BinDir(home)
	t, ok := ReadTest(bin)
	if !ok {
		return "", nil
	}
	running, err := Running(bin)
	if err != nil {
		return "", err
	}
	if running == t.Version {
		return "", nil
	}
	var msg string
	if running == t.From {
		for _, f := range dataFiles {
			if err := restoreDB(filepath.Join(bin, testSaved, filepath.Base(f)), filepath.Join(home, f)); err != nil {
				return "", fmt.Errorf("couldn't put back %s from before the test: %w", f, err)
			}
		}
		msg = fmt.Sprintf("Went back from %s (PR #%d) to %s, with Homelab's data from before the test.", t.Version, t.PR, t.From)
		if err := writeJSON(filepath.Join(bin, resultFile), Result{From: t.Version, To: t.From, OK: true, At: now, Note: msg}); err != nil {
			return "", err
		}
	}
	if err := os.RemoveAll(filepath.Join(bin, testSaved)); err != nil {
		return "", err
	}
	return msg, os.Remove(filepath.Join(bin, testFile))
}

// restoreDB replaces dst with the saved copy, dropping dst's write-ahead log so SQLite
// doesn't replay newer changes onto the old data. A missing copy leaves dst alone.
func restoreDB(saved, dst string) error {
	in, err := os.Open(saved)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".restore.tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dst + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			os.Remove(tmp)
			return err
		}
	}
	return os.Rename(tmp, dst)
}
