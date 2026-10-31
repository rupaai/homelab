// Package secretstore makes homelabd's master key (EnsureMasterKey). The secrets
// themselves live in homelab-services, which imports the old Go secrets.db once on first
// start (see services/homelab-services/src/secrets.rs). The old store code is gone; this
// package only makes sure the key exists, and never makes a new one over old values.
package secretstore

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// EnsureMasterKey makes sure the master key at keyPath exists and is usable, and makes one
// if there is none. homelab-services reads this key to open the secrets; homelabd makes it
// because homelab-services only reads. A missing key is made only while none of the stores
// (the old secrets.db, the one in homelab-services) holds any value, because a new key
// couldn't open old values: then it refuses, and says how to put the key back.
func EnsureMasterKey(keyPath string, stores ...string) error {
	mayCreate := true
	for _, p := range stores {
		if storeHasValues(p) {
			mayCreate = false
		}
	}
	key, err := loadKey(keyPath, mayCreate)
	for i := range key {
		key[i] = 0
	}
	return err
}

// storeHasValues says whether a secrets database holds any secret. A file that can't be
// read counts as holding some, so a key is never made over it.
func storeHasValues(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return true
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM secrets`).Scan(&n); err != nil {
		return !strings.Contains(err.Error(), "no such table")
	}
	return n > 0
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
