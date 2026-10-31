// Data copies made before a promote, and restoring them.

package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rupaai/homelab/internal/core"
)

// dbDumpFile is where a copy keeps a postgres dump. It is a file, not a data folder.
const dbDumpFile = core.DatabaseFolder + ".dump"

// newCopyDir makes the folder for a new data copy of an environment, named after the time
// and label, and returns it with the folder all the environment's copies are in.
func (h *Hub) newCopyDir(app, env, label string) (dst, root string, err error) {
	root = filepath.Join(h.cfg.DataDir, "backups", app, env)
	dst = filepath.Join(root, h.cfg.Now().UTC().Format("20060102-150405")+"-"+label)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", err
	}
	// Mkdir, not MkdirAll: a copy made in the same second must never be written over.
	if err := os.Mkdir(dst, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", "", userErr("A copy was just made. Try again in a second.")
		}
		return "", "", err
	}
	return dst, root, nil
}

// pruneCopies keeps the newest KeepBackups copies in root. Copies made before changes from
// the SQL box are counted on their own, so many small changes never push out the copies made
// before promotes or by hand. The copy named spare is never removed, because the caller is
// about to use it.
func (h *Hub) pruneCopies(ctx context.Context, root, spare string) {
	entries, _ := os.ReadDir(root)
	var changes, others []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), "-"+copyBeforeChange) {
			changes = append(changes, e.Name())
		} else {
			others = append(others, e.Name())
		}
	}
	for _, dirs := range [][]string{changes, others} {
		sort.Strings(dirs)
		for len(dirs) > h.cfg.KeepBackups {
			if dirs[0] != spare {
				h.ad.Deployer.Remove(ctx, filepath.Join(root, dirs[0]))
			}
			dirs = dirs[1:]
		}
	}
}

// backupData copies the app's own data folders (names) of an environment while its app is
// stopped, so the copy is consistent (SQLite files included), and keeps the newest
// KeepBackups copies. With dump it also saves a dump of the environment's postgres database
// next to them. Service data (Jellyfin's config, say) isn't copied. The copies are made
// by a helper container, so files that belong to other users are kept as they are.
func (h *Hub) backupData(ctx context.Context, app, env, label string, names []string, dump bool) (string, error) {
	var have []string
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(h.dataDir(app, env), n)); err == nil {
			have = append(have, n)
		}
	}
	if len(have) == 0 && !dump {
		return "", nil // nothing to copy yet
	}
	dst, root, err := h.newCopyDir(app, env, label)
	if err != nil {
		return "", err
	}
	for _, n := range have {
		if err := h.ad.Deployer.Copy(ctx, filepath.Join(h.dataDir(app, env), n), filepath.Join(dst, n)); err != nil {
			h.ad.Deployer.Remove(ctx, dst)
			return "", err
		}
	}
	if dump {
		if err := h.ad.Deployer.DumpDatabase(ctx, app, env, filepath.Join(dst, dbDumpFile)); err != nil {
			h.ad.Deployer.Remove(ctx, dst)
			return "", fmt.Errorf("the database couldn't be dumped: %w", err)
		}
	}
	h.pruneCopies(ctx, root, "")
	return dst, nil
}

// restoreData replaces the data folders found in a copy made by backupData. The folders it
// replaces are moved next to the copies (named after label) rather than deleted. The app
// must be stopped. A postgres dump in the copy is not a data folder: restoreDatabase puts
// it back.
func (h *Hub) restoreData(ctx context.Context, app, env, from, label string) (kept string, err error) {
	entries, err := os.ReadDir(from)
	if err != nil {
		return "", err
	}
	kept = filepath.Join(h.cfg.DataDir, "backups", app, env, h.cfg.Now().UTC().Format("20060102-150405")+"-replaced-by-restore-"+label)
	if err := os.MkdirAll(kept, 0o700); err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name() == dbDumpFile {
			continue
		}
		dir := filepath.Join(h.dataDir(app, env), e.Name())
		if err := h.ad.Deployer.Move(ctx, dir, filepath.Join(kept, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return kept, err
		}
		if err := h.ad.Deployer.Copy(ctx, filepath.Join(from, e.Name()), dir); err != nil {
			return kept, err
		}
	}
	if os.Remove(kept) == nil {
		kept = "" // a copy of only a database dump moves nothing
	}
	return kept, nil
}
