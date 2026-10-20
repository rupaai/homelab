package fake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// CheckFiles returns FilesErr.
func (d *Deployer) CheckFiles(context.Context) error { return d.FilesErr }

// Copy copies a folder with Homelab's own account (the fake has no containers).
func (d *Deployer) Copy(_ context.Context, from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return errors.New(to + " already exists")
	}
	if err := copyTree(from, to); err != nil {
		os.RemoveAll(to)
		return err
	}
	return nil
}

func (d *Deployer) Move(_ context.Context, from, to string) error { return os.Rename(from, to) }

func (d *Deployer) Remove(_ context.Context, path string) error { return os.RemoveAll(path) }

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, target)
		case info.Mode().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		}
		return nil // sockets and the like aren't data
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
