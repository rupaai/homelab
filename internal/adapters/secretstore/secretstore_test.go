package secretstore

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(filepath.Join(dir, "secrets.db"), filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRoundTripAndReopen(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if err := s.Set("movies", "prod", "TMDB_API_KEY", "k-prod", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("movies", "dev", "TMDB_API_KEY", "k-dev", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("", "", "GITHUB_TOKEN", "ghp_x", "owner"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	info, _ := os.Stat(filepath.Join(dir, "master.key"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "secrets.db"))
	if strings.Contains(string(raw), "k-prod") || strings.Contains(string(raw), "ghp_x") {
		t.Fatal("a value is stored in plain text")
	}

	s = open(t, dir)
	got, err := s.Inject("movies", "prod", []string{"TMDB_API_KEY", "OTHER"})
	if err != nil || got["TMDB_API_KEY"] != "k-prod" || len(got) != 1 {
		t.Fatalf("inject = %v, %v", got, err)
	}
	if v, ok := s.Platform("GITHUB_TOKEN"); !ok || v != "ghp_x" {
		t.Fatalf("platform = %q %v", v, ok)
	}
	if _, ok := s.Platform("TS_AUTHKEY"); ok {
		t.Fatal("unset platform value reported as set")
	}
	miss, _ := s.Missing("movies", "dev", []string{"TMDB_API_KEY", "OTHER"})
	if len(miss) != 1 || miss[0] != "OTHER" {
		t.Fatalf("missing = %v", miss)
	}
	list, _ := s.List("movies", "dev")
	if len(list) != 1 || list[0].Name != "TMDB_API_KEY" || list[0].UpdatedBy != "owner" || list[0].UpdatedAt.IsZero() {
		t.Fatalf("list = %+v", list)
	}
}

func TestPlatformCacheFollowsChanges(t *testing.T) {
	s := open(t, t.TempDir())
	_ = s.Set("", "", "GITHUB_TOKEN", "one", "a")
	if v, _ := s.Platform("GITHUB_TOKEN"); v != "one" {
		t.Fatal(v)
	}
	_ = s.Set("", "", "GITHUB_TOKEN", "two", "a")
	if v, _ := s.Platform("GITHUB_TOKEN"); v != "two" {
		t.Fatal(v)
	}
	_ = s.Delete("", "", "GITHUB_TOKEN")
	if _, ok := s.Platform("GITHUB_TOKEN"); ok {
		t.Fatal("deleted value still there")
	}
}

func TestValueCantMoveToAnotherSecret(t *testing.T) {
	s := open(t, t.TempDir())
	_ = s.Set("movies", "dev", "A", "dev-value", "x")
	_ = s.Set("movies", "prod", "A", "prod-value", "x")
	// Someone with write access to the file copies dev's sealed value over prod's.
	if _, err := s.db.Exec(`UPDATE secrets SET nonce = (SELECT nonce FROM secrets WHERE env='dev'), sealed = (SELECT sealed FROM secrets WHERE env='dev') WHERE env='prod'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inject("movies", "prod", []string{"A"}); err == nil {
		t.Fatal("a value moved from dev opened as prod")
	}
}

func TestMissingKeyWithValuesIsRefused(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	_ = s.Set("movies", "dev", "A", "v", "x")
	s.Close()
	os.Remove(filepath.Join(dir, "master.key"))
	_, err := Open(filepath.Join(dir, "secrets.db"), filepath.Join(dir, "master.key"))
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "master.key")); err == nil {
		t.Fatal("a new key was made over existing values")
	}
}

func TestWrongKeyIsRefused(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	_ = s.Set("movies", "dev", "A", "v", "x")
	s.Close()
	other := t.TempDir()
	open(t, other).Close()
	key, _ := os.ReadFile(filepath.Join(other, "master.key"))
	_ = os.WriteFile(filepath.Join(dir, "master.key"), key, 0o600)
	if _, err := Open(filepath.Join(dir, "secrets.db"), filepath.Join(dir, "master.key")); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestKeyReadableByOthersIsRefused(t *testing.T) {
	dir := t.TempDir()
	open(t, dir).Close()
	_ = os.Chmod(filepath.Join(dir, "master.key"), 0o644)
	if _, err := Open(filepath.Join(dir, "secrets.db"), filepath.Join(dir, "master.key")); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidation(t *testing.T) {
	s := open(t, t.TempDir())
	for _, c := range []struct{ app, env, name, value string }{
		{"movies", "dev", "1BAD", "v"},
		{"movies", "staging", "A", "v"},
		{"", "dev", "A", "v"},
		{"movies", "dev", "A", ""},
		{"movies", "dev", "A", "a\nb"},
		{"movies", "dev", "A", strings.Repeat("x", MaxValueLen+1)},
	} {
		if err := s.Set(c.app, c.env, c.name, c.value, "x"); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestDeleteApp(t *testing.T) {
	s := open(t, t.TempDir())
	_ = s.Set("movies", "dev", "A", "v", "x")
	_ = s.Set("movies", "prod", "A", "v", "x")
	_ = s.Set("music", "dev", "A", "v", "x")
	if err := s.DeleteApp("movies"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.List("movies", "prod"); len(l) != 0 {
		t.Fatal("movies values left")
	}
	if l, _ := s.List("music", "dev"); len(l) != 1 {
		t.Fatal("music values removed")
	}
}

func TestLoadPlatformSeparatesUnsetFromUnreadable(t *testing.T) {
	s := open(t, t.TempDir())
	if v, err := s.LoadPlatform("KEY"); v != "" || err != nil {
		t.Fatalf("unset = %q, %v", v, err)
	}
	_ = s.Set("", "", "KEY", "value", "a")
	s.mu.Lock()
	s.cache = map[string]string{}
	s.mu.Unlock()
	if _, err := s.db.Exec(`UPDATE secrets SET sealed = x'00112233' WHERE name = 'KEY'`); err != nil {
		t.Fatal(err)
	}
	if v, err := s.LoadPlatform("KEY"); err == nil || v != "" {
		t.Fatalf("unreadable = %q, %v", v, err)
	}
	if _, ok := s.Platform("KEY"); ok {
		t.Fatal("Platform reports an unreadable value as set")
	}
}

func TestVersionsKeepTheLastFive(t *testing.T) {
	s := open(t, t.TempDir())
	for i := 1; i <= 7; i++ {
		if err := s.Set("movies", "prod", "KEY", fmt.Sprintf("value-%d", i), fmt.Sprintf("who-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	vs, err := s.Versions("movies", "prod", "KEY")
	if err != nil || len(vs) != 5 {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
	if vs[0].Version != 7 || vs[4].Version != 3 || vs[0].SetBy != "who-7" || vs[0].SetAt.IsZero() {
		t.Fatalf("versions = %+v", vs)
	}
	if list, _ := s.List("movies", "prod"); len(list) != 1 || list[0].Version != 7 {
		t.Fatalf("list = %+v", list)
	}
	// Another secret has its own versions.
	if other, _ := s.Versions("movies", "dev", "KEY"); len(other) != 0 {
		t.Fatalf("other env versions = %+v", other)
	}
	// Restoring makes a new version holding the old value.
	n, err := s.Restore("movies", "prod", "KEY", 4, "restorer")
	if err != nil || n != 8 {
		t.Fatalf("restore = %d, %v", n, err)
	}
	if got, _ := s.Inject("movies", "prod", []string{"KEY"}); got["KEY"] != "value-4" {
		t.Fatalf("value = %v", got)
	}
	vs, _ = s.Versions("movies", "prod", "KEY")
	if len(vs) != 5 || vs[0].Version != 8 || vs[0].SetBy != "restorer" || vs[4].Version != 4 {
		t.Fatalf("versions after restore = %+v", vs)
	}
	// Version 3 was pushed out.
	if _, err := s.Restore("movies", "prod", "KEY", 3, "x"); err == nil {
		t.Fatal("restored a version that is no longer kept")
	}
	if _, err := s.Restore("movies", "prod", "NOPE", 1, "x"); err == nil {
		t.Fatal("restored a secret that was never set")
	}
}

func TestVersionsAreSealedToTheirPlace(t *testing.T) {
	s := open(t, t.TempDir())
	_ = s.Set("movies", "prod", "KEY", "one-one", "x")
	_ = s.Set("movies", "prod", "KEY", "two-two", "x")
	_ = s.Set("movies", "prod", "OTHER", "other-value", "x")
	// Copy version 1's sealed value over version 2: it must not open as version 2.
	if _, err := s.db.Exec(`UPDATE secret_versions SET nonce = (SELECT nonce FROM secret_versions WHERE name='KEY' AND version=1),
		sealed = (SELECT sealed FROM secret_versions WHERE name='KEY' AND version=1) WHERE name='KEY' AND version=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore("movies", "prod", "KEY", 2, "x"); err == nil {
		t.Fatal("one version opened as another")
	}
	// Copy another secret's sealed value onto KEY version 1.
	if _, err := s.db.Exec(`UPDATE secret_versions SET nonce = (SELECT nonce FROM secret_versions WHERE name='OTHER'),
		sealed = (SELECT sealed FROM secret_versions WHERE name='OTHER') WHERE name='KEY' AND version=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore("movies", "prod", "KEY", 1, "x"); err == nil {
		t.Fatal("another secret's value opened as KEY")
	}
	raw, _ := s.db.Query(`SELECT sealed FROM secret_versions`)
	defer raw.Close()
	for raw.Next() {
		var b []byte
		_ = raw.Scan(&b)
		if strings.Contains(string(b), "one-one") || strings.Contains(string(b), "two-two") {
			t.Fatal("a kept value is stored in plain text")
		}
	}
}

func TestDeleteForgetsVersions(t *testing.T) {
	s := open(t, t.TempDir())
	_ = s.Set("movies", "prod", "KEY", "one-one", "x")
	_ = s.Set("movies", "dev", "KEY", "dev-one", "x")
	_ = s.Set("music", "dev", "KEY", "music-one", "x")
	if err := s.Delete("movies", "prod", "KEY"); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s.Versions("movies", "prod", "KEY"); len(vs) != 0 {
		t.Fatalf("versions left after delete: %+v", vs)
	}
	if err := s.DeleteApp("movies"); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s.Versions("movies", "dev", "KEY"); len(vs) != 0 {
		t.Fatalf("versions left after DeleteApp: %+v", vs)
	}
	if vs, _ := s.Versions("music", "dev", "KEY"); len(vs) != 1 {
		t.Fatalf("music versions = %+v", vs)
	}
	// A deleted secret starts again at version 1.
	_ = s.Set("movies", "prod", "KEY", "again", "x")
	if l, _ := s.List("movies", "prod"); l[0].Version != 1 {
		t.Fatalf("version = %d", l[0].Version)
	}
}

// A secrets.db made before versions existed has no version column and no versions table.
func TestOpenOldStoreMakesValuesVersionOne(t *testing.T) {
	dir := t.TempDir()
	dbPath, keyPath := filepath.Join(dir, "secrets.db"), filepath.Join(dir, "master.key")
	// Make a key, and seal values the way the old code did.
	first := open(t, dir)
	seal := func(app, env, name, value string) ([]byte, []byte) {
		n, c, err := first.seal(aad(app, env, name), value)
		if err != nil {
			t.Fatal(err)
		}
		return n, c
	}
	n1, c1 := seal("movies", "prod", "TMDB_API_KEY", "old-prod-value")
	n2, c2 := seal("", "", "GITHUB_TOKEN", "ghp_old")
	first.Close()
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		os.Remove(f)
	}
	old, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE secrets (
		app TEXT NOT NULL, env TEXT NOT NULL, name TEXT NOT NULL,
		nonce BLOB NOT NULL, sealed BLOB NOT NULL,
		updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL,
		PRIMARY KEY (app, env, name))`); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		app, env, name string
		n, c           []byte
	}{{"movies", "prod", "TMDB_API_KEY", n1, c1}, {"", "", "GITHUB_TOKEN", n2, c2}} {
		if _, err := old.Exec(`INSERT INTO secrets VALUES (?,?,?,?,?,?,?)`, r.app, r.env, r.name, r.n, r.c, 1700000000000, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	s, err := Open(dbPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	got, err := s.Inject("movies", "prod", []string{"TMDB_API_KEY"})
	if err != nil || got["TMDB_API_KEY"] != "old-prod-value" {
		t.Fatalf("inject = %v, %v", got, err)
	}
	if v, ok := s.Platform("GITHUB_TOKEN"); !ok || v != "ghp_old" {
		t.Fatalf("platform = %q %v", v, ok)
	}
	list, _ := s.List("movies", "prod")
	if len(list) != 1 || list[0].Version != 1 || list[0].UpdatedBy != "owner" {
		t.Fatalf("list = %+v", list)
	}
	vs, _ := s.Versions("movies", "prod", "TMDB_API_KEY")
	if len(vs) != 1 || vs[0].Version != 1 || vs[0].SetBy != "owner" || vs[0].SetAt.UnixMilli() != 1700000000000 {
		t.Fatalf("versions = %+v", vs)
	}
	// New changes build on it, and opening again changes nothing.
	if err := s.Set("movies", "prod", "TMDB_API_KEY", "new-prod-value", "me"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(dbPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.Close() })
	vs, _ = s2.Versions("movies", "prod", "TMDB_API_KEY")
	if len(vs) != 2 || vs[0].Version != 2 || vs[1].Version != 1 {
		t.Fatalf("versions after reopen = %+v", vs)
	}
	if _, err := s2.Restore("movies", "prod", "TMDB_API_KEY", 1, "me"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s2.Inject("movies", "prod", []string{"TMDB_API_KEY"}); got["TMDB_API_KEY"] != "old-prod-value" {
		t.Fatalf("restored = %v", got)
	}
}

// The Rust secrets service imports this fixture in its own tests (services/homelab-services).
// Opening it here with the real store keeps the fixture honest: if the Go format changes, this
// fails, and the fixture and the Rust import must change with it.
func TestRustImportFixtureOpens(t *testing.T) {
	src := filepath.Join("..", "..", "..", "services", "homelab-services", "tests", "fixtures", "go-secrets")
	dir := t.TempDir()
	for _, f := range []struct{ from, to string }{{"secrets.sqlite", "secrets.db"}, {"master.key", "master.key"}} {
		b, err := os.ReadFile(filepath.Join(src, f.from))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f.to), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := open(t, dir)
	got, err := s.Inject("movies", "prod", []string{"TMDB_API_KEY", "HOMELAB_TOKEN"})
	if err != nil || got["TMDB_API_KEY"] != "tmdb-v7" || got["HOMELAB_TOKEN"] != "hlk_fixture" {
		t.Fatalf("movies/prod = %v, %v", got, err)
	}
	if v, ok := s.Platform("GITHUB_TOKEN"); !ok || v != "ghp_fixture_token" {
		t.Fatalf("platform = %q %v", v, ok)
	}
	vs, err := s.Versions("movies", "prod", "TMDB_API_KEY")
	if err != nil || len(vs) != 5 || vs[0].Version != 7 || vs[4].Version != 3 {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
}

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
	s, err := Open(goDB, filepath.Join(dir, "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("movies", "prod", "K", "v", "me"); err != nil {
		t.Fatal(err)
	}
	s.Close()
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
