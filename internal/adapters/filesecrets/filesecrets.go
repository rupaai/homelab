// Package filesecrets reads Homelab's milestone 1 secrets: plain KEY=VALUE files in one
// folder. Since milestone 2 the encrypted store (adapters/secretstore) holds secrets, and
// homelabd only uses this package to import old files into it once (Files).
//
// A file that any other user can read is refused, with the command that fixes it.
package filesecrets

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// platformFile holds Homelab's own credentials, such as GITHUB_TOKEN and TS_AUTHKEY.
const platformFile = "platform.env"

// Store reads secrets from the files in one directory.
type Store struct {
	dir string
}

// New returns a Store that reads files from dir.
func New(dir string) *Store {
	return &Store{dir: dir}
}

// Dir returns the folder the secrets files are read from.
func (s *Store) Dir() string {
	return s.dir
}

// Platform returns a Homelab credential from platform.env. It returns false when the
// value is absent or empty, or when the file cannot be read safely.
func (s *Store) Platform(name string) (string, bool) {
	vals, err := s.load(platformFile)
	if err != nil {
		return "", false
	}
	v := vals[name]
	return v, v != ""
}

// Missing lists which of names have no value for app/env. It never returns values.
func (s *Store) Missing(app, env string, names []string) ([]string, error) {
	file, err := appFile(app, env)
	if err != nil {
		return nil, err
	}
	vals, err := s.load(file)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, n := range names {
		if vals[n] == "" {
			missing = append(missing, n)
		}
	}
	return missing, nil
}

// Inject returns the values of the requested names that have one for app/env.
func (s *Store) Inject(app, env string, names []string) (map[string]string, error) {
	file, err := appFile(app, env)
	if err != nil {
		return nil, err
	}
	vals, err := s.load(file)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		if v := vals[n]; v != "" {
			out[n] = v
		}
	}
	return out, nil
}

// File is one secrets file and its values. App is "" for platform.env.
type File struct {
	Path   string
	App    string
	Env    string
	Values map[string]string
}

// Files reads every secrets file in the folder: platform.env and <app>.<env>.env. Other
// files (such as ones already imported) are ignored.
func (s *Store) Files() ([]File, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []File
	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(n, ".env") {
			continue
		}
		f := File{Path: filepath.Join(s.dir, n)}
		if n != platformFile {
			parts := strings.Split(strings.TrimSuffix(n, ".env"), ".")
			if len(parts) != 2 {
				continue
			}
			f.App, f.Env = parts[0], parts[1]
		}
		vals, err := s.load(n)
		if err != nil {
			return nil, err
		}
		f.Values = vals
		out = append(out, f)
	}
	return out, nil
}

// appFile is the file for one app environment, such as movies.prod.env.
func appFile(app, env string) (string, error) {
	for _, part := range []string{app, env} {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\`) {
			return "", fmt.Errorf("invalid app or environment name %q", part)
		}
	}
	return app + "." + env + ".env", nil
}

// load reads and parses one file in the store's directory. A missing file has no values.
func (s *Store) load(file string) (map[string]string, error) {
	path := filepath.Join(s.dir, file)
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", file, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", file, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, permissionError(path)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return parse(data), nil
}

// permissionError says how to fix a file other users can read.
func permissionError(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	shown := filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path))
	return fmt.Errorf("%s can be read by other users; run: chmod 600 %s", shown, abs)
}

// parse reads KEY=VALUE lines. Blank lines and lines starting with # are skipped, an
// optional leading "export " is dropped, and one pair of matching quotes is removed.
// Lines without "=" are ignored. A repeated key keeps its last value.
func parse(data []byte) map[string]string {
	vals := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		vals[key] = unquote(strings.TrimSpace(value))
	}
	return vals
}

// unquote removes one pair of matching single or double quotes around v.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}
