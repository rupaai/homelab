package secretstore

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureMasterKey(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "master.key")
	goDB, rustDB := filepath.Join(dir, "secrets.db"), filepath.Join(dir, "rust.db")

	// Nothing stored yet: a key is made, private, and kept on the next call.
	if err := EnsureMasterKey(key, goDB, rustDB); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(key)
	if st, _ := os.Stat(key); st.Mode().Perm() != 0o600 || len(first) == 0 {
		t.Fatalf("key mode %v, %d bytes", st.Mode().Perm(), len(first))
	}
	if err := EnsureMasterKey(key, goDB, rustDB); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(key); string(again) != string(first) {
		t.Fatal("the key changed")
	}
	// A key others can read, or a bad one, is refused.
	_ = os.Chmod(key, 0o644)
	if err := EnsureMasterKey(key, goDB); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("readable key: %v", err)
	}
	_ = os.Remove(key)
	_ = os.WriteFile(key, []byte("c2hvcnQ=\n"), 0o600)
	if err := EnsureMasterKey(key, goDB); err == nil || !strings.Contains(err.Error(), "isn't a valid key") {
		t.Fatalf("short key: %v", err)
	}

	// A store with values and no key: no new key (it couldn't open them).
	os.Remove(key)
	db, err := sql.Open("sqlite", goDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE secrets (name TEXT); INSERT INTO secrets VALUES ('K')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := EnsureMasterKey(key, goDB); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing key with values: %v", err)
	}
	if _, err := os.Stat(key); err == nil {
		t.Fatal("a key was made over a store that has values")
	}
	// An unreadable store file counts as having values.
	_ = os.WriteFile(rustDB, []byte("not a database at all, just text"), 0o600)
	if err := EnsureMasterKey(filepath.Join(dir, "k2"), rustDB); err == nil {
		t.Fatal("a key was made over a file that couldn't be read")
	}
}
