package sharedir

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpen(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux needs it")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "db")
	os.Mkdir(sub, 0o700)
	f := filepath.Join(sub, "movies.sqlite")
	os.WriteFile(f, []byte("x"), 0o600)
	os.Symlink("/etc/passwd", filepath.Join(dir, "link"))
	if err := Open(dir); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{sub: 0o777, f: 0o666} {
		st, _ := os.Stat(p)
		if st.Mode().Perm() != want {
			t.Errorf("%s = %v, want %v", p, st.Mode().Perm(), want)
		}
	}
	if st, _ := os.Stat("/etc/passwd"); st.Mode().Perm() != 0o644 {
		t.Error("followed a symlink")
	}
}
