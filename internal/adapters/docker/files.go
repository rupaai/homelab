package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// helperImage is the small image that copies, moves and deletes data folders as root. It
// is pinned by version tag (busybox 1.37.0), which is multi-arch, so it also runs on an
// arm64 Mac. Docker downloads it the first time it is needed.
const helperImage = "docker.io/library/busybox:1.37.0"

// helper runs one command in a short-lived container as root, without a network, with the
// given host folders mounted. mounts maps a container path to a host folder; a "ro:" prefix
// on the container path mounts it read-only.
func (d *Deployer) helper(ctx context.Context, mounts [][2]string, args ...string) error {
	full := []string{"run", "--rm", "--network", "none", "--user", "0"}
	for _, m := range mounts {
		if !filepath.IsAbs(m[1]) || strings.ContainsAny(m[1], ":,\n") {
			return fmt.Errorf("can't use %q as a data folder", m[1])
		}
		mode := ""
		target := m[0]
		if rest, ok := strings.CutPrefix(target, "ro:"); ok {
			target, mode = rest, ":ro"
		}
		full = append(full, "-v", m[1]+":"+target+mode)
	}
	full = append(full, helperImage)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, d.bin, full...)
	cmd.Env = d.env()
	out := &tailBuffer{max: maxLogBytes}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the helper container failed (%s): %w\n%s", args[0], err, lastLines(out.Bytes(), errTailLines))
	}
	return nil
}

// CheckFiles runs a do-nothing helper, which downloads its image if needed.
func (d *Deployer) CheckFiles(ctx context.Context) error {
	return d.helper(ctx, nil, "true")
}

// Copy copies a folder with `cp -a`, which keeps owners, permissions and times and reads
// every file because the helper is root.
func (d *Deployer) Copy(ctx context.Context, from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%s already exists", to)
	}
	parent := filepath.Dir(to)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	return d.helper(ctx, [][2]string{{"ro:/from", from}, {"/out", parent}}, "cp", "-a", "/from", "/out/"+filepath.Base(to))
}

// Move renames from to to. Homelab's own account is tried first; if it isn't allowed
// (a folder an app took over), the helper does it.
func (d *Deployer) Move(ctx context.Context, from, to string) error {
	err := os.Rename(from, to)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return err
	}
	return d.helper(ctx, [][2]string{{"/a", filepath.Dir(from)}, {"/b", filepath.Dir(to)}}, "mv", "/a/"+filepath.Base(from), "/b/"+filepath.Base(to))
}

// Remove deletes path, with the helper when Homelab's own account can't.
func (d *Deployer) Remove(ctx context.Context, path string) error {
	if os.RemoveAll(path) == nil {
		return nil
	}
	return d.helper(ctx, [][2]string{{"/p", filepath.Dir(path)}}, "rm", "-rf", "/p/"+filepath.Base(path))
}

// RemoveOldImages deletes the images of repository image that no container uses, except
// those whose digest is in keep. Docker refuses to delete an image a container uses, and
// images of other repositories are never looked at: Docker is shared with other people.
func (d *Deployer) RemoveOldImages(ctx context.Context, image string, keep []string) error {
	if image == "" {
		return nil
	}
	imgs, err := d.api.images(ctx)
	if err != nil {
		return err
	}
	for _, img := range imgs {
		var refs []string
		kept := false
		for _, ref := range img.RepoDigests {
			repo, digest := splitRef(ref)
			if repo != image {
				continue
			}
			refs = append(refs, ref)
			kept = kept || slices.Contains(keep, digest)
		}
		if kept {
			continue
		}
		for _, ref := range refs {
			if err := d.api.removeImage(ctx, ref); err != nil && !errors.Is(err, errImageInUse) {
				d.log.Warn("removing an old image", "image", ref, "err", err)
			} else if err == nil {
				d.log.Info("removed an old image", "image", ref)
			}
		}
	}
	return nil
}
