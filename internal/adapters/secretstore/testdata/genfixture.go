//go:build ignore

// genfixture writes the fixture the Rust secrets service tests import: a secrets.sqlite made
// by the real store, and the key it was made with.
//
//	go run ./internal/adapters/secretstore/testdata/genfixture.go services/homelab-services/tests/fixtures/go-secrets
//
// (It is not named .db because .gitignore skips those.) The key is a fixed test key (bytes 0 to 31). It protects nothing real.
package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rupaai/homelab/internal/adapters/secretstore"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: genfixture <folder>")
		os.Exit(2)
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	db, key := filepath.Join(dir, "secrets.sqlite"), filepath.Join(dir, "master.key")
	_ = os.Remove(db)
	_ = os.Remove(db + "-wal")
	_ = os.Remove(db + "-shm")
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(raw)+"\n"), 0o600); err != nil {
		panic(err)
	}
	s, err := secretstore.Open(db, key)
	if err != nil {
		panic(err)
	}
	set := func(app, env, name, value string) {
		if err := s.Set(app, env, name, value, "fixture"); err != nil {
			panic(err)
		}
	}
	set("", "", "GITHUB_TOKEN", "ghp_fixture_token")
	for i := 1; i <= 7; i++ { // seven values: versions 3 to 7 are kept
		set("movies", "prod", "TMDB_API_KEY", fmt.Sprintf("tmdb-v%d", i))
	}
	set("movies", "dev", "TMDB_API_KEY", "tmdb-dev")
	set("movies", "prod", "HOMELAB_TOKEN", "hlk_fixture")
	set("notes", "prod", "DB_URL", "postgres://u:p@h/db?x=1&y=ü")
	if err := s.Close(); err != nil {
		panic(err)
	}
	for _, f := range []string{db + "-wal", db + "-shm"} {
		if _, err := os.Stat(f); err == nil {
			panic(f + " is left over; the fixture must be one file")
		}
	}
}
