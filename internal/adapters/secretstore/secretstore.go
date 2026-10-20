// Package secretstore implements core.Secrets as an encrypted store (S1): one SQLite file
// whose values are sealed with AES-256-GCM under a master key kept in its own file (S9).
//
// Every value has its own random nonce and is bound to its app, environment and name as
// additional data, so a sealed value copied onto another secret fails to open instead of
// quietly becoming that secret.
package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rupaai/homelab/internal/core"
)

var _ core.Secrets = (*Store)(nil)

// MaxValueLen is the longest value accepted.
const MaxValueLen = 8192

var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Store is the encrypted secrets store. It is safe for concurrent use.
type Store struct {
	db   *sql.DB
	aead cipher.AEAD
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]string // platform values, read often (GITHUB_TOKEN on every request)
}

// Open opens (or creates) the store at dbPath with the master key at keyPath. A missing key
// file is created only while the store holds no values; otherwise Open refuses, because a
// new key couldn't read the old values.
func Open(dbPath, keyPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS secrets (
		app TEXT NOT NULL, env TEXT NOT NULL, name TEXT NOT NULL,
		nonce BLOB NOT NULL, sealed BLOB NOT NULL,
		updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL,
		PRIMARY KEY (app, env, name))`); err != nil {
		db.Close()
		return nil, fmt.Errorf("secrets store: %w", err)
	}
	_ = os.Chmod(dbPath, 0o600)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM secrets`).Scan(&n); err != nil {
		db.Close()
		return nil, err
	}
	key, err := loadKey(keyPath, n == 0)
	if err != nil {
		db.Close()
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		db.Close()
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, aead: aead, now: time.Now, cache: map[string]string{}}
	// Prove the key opens what is stored, so a wrong key fails now and not at a deploy.
	if n > 0 {
		var app, env, name string
		var nonce, sealed []byte
		if err := db.QueryRow(`SELECT app, env, name, nonce, sealed FROM secrets LIMIT 1`).Scan(&app, &env, &name, &nonce, &sealed); err != nil {
			db.Close()
			return nil, err
		}
		if _, err := s.open(app, env, name, nonce, sealed); err != nil {
			db.Close()
			return nil, fmt.Errorf("the master key at %s doesn't open the secrets in %s. Put back the key that was used with them", keyPath, dbPath)
		}
	}
	return s, nil
}

// loadKey reads the master key, or makes one when allowed.
func loadKey(path string, mayCreate bool) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if !mayCreate {
			return nil, fmt.Errorf("the master key %s is missing, but the secrets store already has values that only it can open. "+
				"Put the key file back (from your copy), or move the store away to start again with no secrets", path)
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		enc := base64.StdEncoding.EncodeToString(key) + "\n"
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create master key: %w", err)
		}
		if _, err := f.WriteString(enc); err != nil {
			f.Close()
			return nil, err
		}
		return key, f.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("the master key %s can be read by other users. Run: chmod 600 %s", path, path)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("the master key %s isn't a valid key (32 bytes, base64)", path)
	}
	return key, nil
}

// Close closes the store.
func (s *Store) Close() error { return s.db.Close() }

func aad(app, env, name string) []byte {
	return []byte("homelab-secret-v1\x00" + app + "\x00" + env + "\x00" + name)
}

func (s *Store) open(app, env, name string, nonce, sealed []byte) (string, error) {
	if len(nonce) != s.aead.NonceSize() {
		return "", errors.New("bad nonce")
	}
	v, err := s.aead.Open(nil, nonce, sealed, aad(app, env, name))
	if err != nil {
		return "", fmt.Errorf("secret %s couldn't be decrypted", name)
	}
	return string(v), nil
}

// check validates where a secret lives. app "" means platform, and then env must be "".
func check(app, env, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%q isn't a valid secret name: use letters, digits and _, not starting with a digit", name)
	}
	if app == "" && env != "" {
		return errors.New("platform secrets have no environment")
	}
	if app != "" && (env != core.Dev && env != core.Prod) {
		return fmt.Errorf("there's no environment %q", env)
	}
	return nil
}

func cacheKey(app, env, name string) string { return app + "\x00" + env + "\x00" + name }

// Set stores or replaces a value.
func (s *Store) Set(app, env, name, value, by string) error {
	if err := check(app, env, name); err != nil {
		return err
	}
	if value == "" || len(value) > MaxValueLen {
		return fmt.Errorf("a value must be 1 to %d characters", MaxValueLen)
	}
	if strings.ContainsAny(value, "\n\r\x00") {
		return errors.New("a value can't contain line breaks")
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := s.aead.Seal(nil, nonce, []byte(value), aad(app, env, name))
	_, err := s.db.Exec(`INSERT INTO secrets (app, env, name, nonce, sealed, updated_at, updated_by) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT (app, env, name) DO UPDATE SET nonce = excluded.nonce, sealed = excluded.sealed,
		updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		app, env, name, nonce, sealed, s.now().UnixMilli(), by)
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, cacheKey(app, env, name))
	s.mu.Unlock()
	return nil
}

// Delete removes a value.
func (s *Store) Delete(app, env, name string) error {
	if err := check(app, env, name); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM secrets WHERE app = ? AND env = ? AND name = ?`, app, env, name)
	s.mu.Lock()
	delete(s.cache, cacheKey(app, env, name))
	s.mu.Unlock()
	return err
}

// DeleteApp removes every value of an app.
func (s *Store) DeleteApp(app string) error {
	if app == "" {
		return errors.New("DeleteApp needs an app")
	}
	_, err := s.db.Exec(`DELETE FROM secrets WHERE app = ?`, app)
	return err
}

// List describes the values stored for app/env, by name, without the values.
func (s *Store) List(app, env string) ([]core.SecretInfo, error) {
	rows, err := s.db.Query(`SELECT name, updated_at, updated_by FROM secrets WHERE app = ? AND env = ? ORDER BY name`, app, env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.SecretInfo
	for rows.Next() {
		var i core.SecretInfo
		var at int64
		if err := rows.Scan(&i.Name, &at, &i.UpdatedBy); err != nil {
			return nil, err
		}
		i.UpdatedAt = time.UnixMilli(at)
		out = append(out, i)
	}
	return out, rows.Err()
}

// get returns one value, or "" when it isn't set.
func (s *Store) get(app, env, name string) (string, error) {
	var nonce, sealed []byte
	err := s.db.QueryRow(`SELECT nonce, sealed FROM secrets WHERE app = ? AND env = ? AND name = ?`, app, env, name).Scan(&nonce, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return s.open(app, env, name, nonce, sealed)
}

// Missing lists which of names have no value for app/env.
func (s *Store) Missing(app, env string, names []string) ([]string, error) {
	have, err := s.List(app, env)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, i := range have {
		set[i.Name] = true
	}
	var out []string
	for _, n := range names {
		if !set[n] {
			out = append(out, n)
		}
	}
	return out, nil
}

// Inject returns the values of names that are set for app/env.
func (s *Store) Inject(app, env string, names []string) (map[string]string, error) {
	if app == "" {
		return nil, errors.New("Inject needs an app")
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		v, err := s.get(app, env, n)
		if err != nil {
			return nil, err
		}
		if v != "" {
			out[n] = v
		}
	}
	return out, nil
}

// Platform returns a Homelab credential; ok is false when it is not set or can't be read.
func (s *Store) Platform(name string) (string, bool) {
	v, err := s.LoadPlatform(name)
	return v, err == nil && v != ""
}

// LoadPlatform returns a Homelab credential. The value is "" when it is not set; err is set
// when the store can't be read or decrypted, so callers don't mistake that for "not set".
func (s *Store) LoadPlatform(name string) (string, error) {
	k := cacheKey("", "", name)
	s.mu.Lock()
	v, ok := s.cache[k]
	s.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := s.get("", "", name)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.cache[k] = v
	s.mu.Unlock()
	return v, nil
}
