package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// static serves the companion's files, with index.html for anything that isn't a file.
type static struct {
	fsys   fs.FS
	hashes []string // CSP hashes of inline scripts in index.html
}

var inlineScript = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)

func newStatic(fsys fs.FS) *static {
	s := &static{fsys: fsys}
	if fsys == nil {
		return s
	}
	if b, err := fs.ReadFile(fsys, "index.html"); err == nil {
		for _, m := range inlineScript.FindAllSubmatch(b, -1) {
			if bytes.Contains(m[1], []byte("src=")) || len(bytes.TrimSpace(m[2])) == 0 {
				continue
			}
			sum := sha256.Sum256(m[2])
			s.hashes = append(s.hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
		}
	}
	return s
}

// csp is the Content-Security-Policy. SvelteKit's static adapter writes one inline
// bootstrap script into index.html; its hash is allowed so scripts stay 'self' only.
func (s *static) csp() string {
	script := "'self'"
	if len(s.hashes) > 0 {
		script += " " + strings.Join(s.hashes, " ")
	}
	return "default-src 'self'; script-src " + script +
		"; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'" +
		"; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
}

func (s *static) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeErr(w, http.StatusMethodNotAllowed, "That isn't allowed here.")
		return
	}
	if s.fsys == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	b, err := fs.ReadFile(s.fsys, name)
	if err != nil { // missing or a folder: the companion routes it itself
		name = "index.html"
		b, err = fs.ReadFile(s.fsys, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	switch {
	case name == "index.html":
		w.Header().Set("Cache-Control", "no-cache")
	case strings.HasPrefix(name, "_app/immutable/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
}
