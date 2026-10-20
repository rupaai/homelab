package secretstore

import (
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
