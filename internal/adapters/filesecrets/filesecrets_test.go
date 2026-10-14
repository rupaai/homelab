package filesecrets

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

// writeSecrets writes name in dir with exactly mode (umask does not apply).
func writeSecrets(t *testing.T, dir, name, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func TestInjectParsing(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string]string
	}{
		{"plain pairs", "A=1\nB=two\n", map[string]string{"A": "1", "B": "two"}},
		{"comments and blank lines", "# note\n\n   # indented note\nA=1\n\n", map[string]string{"A": "1"}},
		{"double quotes removed", `A="x y"` + "\n", map[string]string{"A": "x y"}},
		{"single quotes removed", "B='z'\n", map[string]string{"B": "z"}},
		{"mismatched quotes kept", `C="mismatch'` + "\n", map[string]string{"C": `"mismatch'`}},
		{"export prefix dropped", "export A=1\nexport\tB=2\n", map[string]string{"A": "1", "B": "2"}},
		{"spaces around equals", "A = 3\n", map[string]string{"A": "3"}},
		{"equals inside value", "URL=http://x?a=b\n", map[string]string{"URL": "http://x?a=b"}},
		{"CRLF line endings", "A=1\r\nB=2\r\n", map[string]string{"A": "1", "B": "2"}},
		{"line without equals ignored", "junk\nA=1\n", map[string]string{"A": "1"}},
		{"empty key ignored", "=oops\nA=1\n", map[string]string{"A": "1"}},
		{"repeated key keeps last", "A=1\nA=2\n", map[string]string{"A": "2"}},
		{"empty value is no value", "A=\nB=\"\"\n", map[string]string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSecrets(t, dir, "movies.prod.env", tc.content, 0o600)
			names := slices.Sorted(maps.Keys(tc.want))
			if len(names) == 0 {
				names = []string{"A", "B", "C"}
			}
			got, err := New(dir).Inject("movies", "prod", names)
			if err != nil {
				t.Fatalf("Inject: %v", err)
			}
			if !maps.Equal(got, tc.want) {
				t.Fatalf("Inject = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestMissingAndInject(t *testing.T) {
	tests := []struct {
		name        string
		content     string // "" with noFile means the file does not exist
		noFile      bool
		names       []string
		wantMissing []string
		wantInject  map[string]string
	}{
		{
			name:        "no file means everything missing",
			noFile:      true,
			names:       []string{"TOKEN", "DB"},
			wantMissing: []string{"TOKEN", "DB"},
			wantInject:  map[string]string{},
		},
		{
			name:        "mix of present, empty and absent",
			content:     "TOKEN=abc\nDB=\nOTHER=x\n",
			names:       []string{"TOKEN", "DB", "ABSENT"},
			wantMissing: []string{"DB", "ABSENT"},
			wantInject:  map[string]string{"TOKEN": "abc"},
		},
		{
			name:        "all present",
			content:     "TOKEN=abc\nDB=\"postgres://x\"\n",
			names:       []string{"TOKEN", "DB"},
			wantMissing: nil,
			wantInject:  map[string]string{"TOKEN": "abc", "DB": "postgres://x"},
		},
		{
			name:        "no names asked",
			content:     "TOKEN=abc\n",
			names:       nil,
			wantMissing: nil,
			wantInject:  map[string]string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tc.noFile {
				writeSecrets(t, dir, "movies.prod.env", tc.content, 0o600)
			}
			s := New(dir)

			missing, err := s.Missing("movies", "prod", tc.names)
			if err != nil {
				t.Fatalf("Missing: %v", err)
			}
			if !slices.Equal(missing, tc.wantMissing) {
				t.Errorf("Missing = %v; want %v", missing, tc.wantMissing)
			}

			got, err := s.Inject("movies", "prod", tc.names)
			if err != nil {
				t.Fatalf("Inject: %v", err)
			}
			if !maps.Equal(got, tc.wantInject) {
				t.Errorf("Inject = %v; want %v", got, tc.wantInject)
			}
		})
	}
}

func TestPermissions(t *testing.T) {
	tests := []struct {
		name    string
		mode    os.FileMode
		wantErr bool
	}{
		{"owner only 0600", 0o600, false},
		{"owner read only 0400", 0o400, false},
		{"owner with exec 0700", 0o700, false},
		{"group readable 0640", 0o640, true},
		{"other readable 0604", 0o604, true},
		{"world readable 0644", 0o644, true},
		{"group writable 0660", 0o660, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The folder is named secrets so the message shows secrets/movies.prod.env.
			dir := filepath.Join(t.TempDir(), "secrets")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeSecrets(t, dir, "movies.prod.env", "TOKEN=abc\n", tc.mode)
			s := New(dir)

			missing, errMissing := s.Missing("movies", "prod", []string{"TOKEN"})
			got, errInject := s.Inject("movies", "prod", []string{"TOKEN"})
			if !tc.wantErr {
				if errMissing != nil || errInject != nil {
					t.Fatalf("Missing err=%v Inject err=%v; want no errors", errMissing, errInject)
				}
				if got["TOKEN"] != "abc" || len(missing) != 0 {
					t.Fatalf("Inject = %v, Missing = %v; want TOKEN=abc and nothing missing", got, missing)
				}
				return
			}
			if errMissing == nil || errInject == nil {
				t.Fatalf("Missing err=%v Inject err=%v; want both errors", errMissing, errInject)
			}
			want := "secrets/movies.prod.env can be read by other users; run: chmod 600 " +
				filepath.Join(dir, "movies.prod.env")
			if errMissing.Error() != want {
				t.Errorf("error = %q; want %q", errMissing.Error(), want)
			}
		})
	}
}

func TestPlatform(t *testing.T) {
	tests := []struct {
		name    string
		content string
		noFile  bool
		mode    os.FileMode
		key     string
		wantVal string
		wantOK  bool
	}{
		{"present", "GITHUB_TOKEN=ghp_1\nTS_AUTHKEY=\"tskey\"\n", false, 0o600, "GITHUB_TOKEN", "ghp_1", true},
		{"quoted value", "TS_AUTHKEY=\"tskey\"\n", false, 0o600, "TS_AUTHKEY", "tskey", true},
		{"absent key", "GITHUB_TOKEN=ghp_1\n", false, 0o600, "TS_AUTHKEY", "", false},
		{"empty value", "TS_AUTHKEY=\n", false, 0o600, "TS_AUTHKEY", "", false},
		{"no file", "", true, 0o600, "GITHUB_TOKEN", "", false},
		{"insecure file refused", "GITHUB_TOKEN=ghp_1\n", false, 0o644, "GITHUB_TOKEN", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tc.noFile {
				writeSecrets(t, dir, "platform.env", tc.content, tc.mode)
			}
			got, ok := New(dir).Platform(tc.key)
			if got != tc.wantVal || ok != tc.wantOK {
				t.Fatalf("Platform(%q) = %q, %v; want %q, %v", tc.key, got, ok, tc.wantVal, tc.wantOK)
			}
		})
	}
}

func TestRereadsEveryCall(t *testing.T) {
	dir := t.TempDir()
	writeSecrets(t, dir, "movies.prod.env", "TOKEN=old\n", 0o600)
	s := New(dir)

	got, err := s.Inject("movies", "prod", []string{"TOKEN"})
	if err != nil || got["TOKEN"] != "old" {
		t.Fatalf("first Inject = %v, %v; want old", got, err)
	}

	writeSecrets(t, dir, "movies.prod.env", "TOKEN=new\n", 0o600)
	got, err = s.Inject("movies", "prod", []string{"TOKEN"})
	if err != nil || got["TOKEN"] != "new" {
		t.Fatalf("second Inject = %v, %v; want new", got, err)
	}

	if err := os.Remove(filepath.Join(dir, "movies.prod.env")); err != nil {
		t.Fatal(err)
	}
	missing, err := s.Missing("movies", "prod", []string{"TOKEN"})
	if err != nil || !slices.Equal(missing, []string{"TOKEN"}) {
		t.Fatalf("after removal Missing = %v, %v; want [TOKEN]", missing, err)
	}
}

func TestInvalidNames(t *testing.T) {
	s := New(t.TempDir())
	for _, tc := range []struct{ app, env string }{
		{"", "prod"},
		{"movies", ""},
		{"../etc", "prod"},
		{"movies", "a/b"},
		{"..", "prod"},
	} {
		if _, err := s.Missing(tc.app, tc.env, []string{"X"}); err == nil {
			t.Errorf("Missing(%q, %q) = nil error; want error", tc.app, tc.env)
		}
		if _, err := s.Inject(tc.app, tc.env, []string{"X"}); err == nil {
			t.Errorf("Inject(%q, %q) = nil error; want error", tc.app, tc.env)
		}
	}
}

func TestDir(t *testing.T) {
	dir := t.TempDir()
	if got := New(dir).Dir(); got != dir {
		t.Fatalf("Dir() = %q; want %q", got, dir)
	}
}

var _ core.Secrets = (*Store)(nil)
