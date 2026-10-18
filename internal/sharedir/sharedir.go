// Package sharedir lets containers write to the data folders Homelab gives them.
//
// On Linux an app's container usually runs as its own user (the movie app runs as uid
// 1000), not as the homelab account that made its data folder. Homelab can't change who
// owns a file, so it opens the files it owns to every user instead. That is safe because
// the Homelab folder itself (0700, owned by homelab) keeps everyone else out; containers
// see only the folder mounted into them. Files the app made are already the app's.
//
// On a Mac, Docker runs in a VM that maps every file to the user, so nothing is needed.
package sharedir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// Open makes dir and everything in it that this process owns writable by containers.
func Open(dir string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	uid := os.Getuid()
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil // a folder the app made private; it's the app's
			}
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != uid {
			return nil
		}
		want := info.Mode().Perm() | 0o666
		if d.IsDir() {
			want = info.Mode().Perm() | 0o777
		}
		if want == info.Mode().Perm() {
			return nil
		}
		return os.Chmod(p, want)
	})
}
