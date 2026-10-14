// Package companion holds the built companion web app, embedded into homehubd.
package companion

import (
	"embed"
	"io/fs"
	"testing/fstest"
)

// files is the contents of web/build, copied here by `npm run build` (make web).
// Only build/.keep is in Git, so a plain `go build` still compiles.
//
//go:embed all:build
var files embed.FS

//go:embed placeholder.html
var placeholder []byte

// Site returns the companion's files, or a single page explaining how to build them.
func Site() fs.FS {
	site, err := fs.Sub(files, "build")
	if err == nil {
		if _, err := fs.Stat(site, "index.html"); err == nil {
			return site
		}
	}
	return fstest.MapFS{"index.html": {Data: placeholder}}
}
