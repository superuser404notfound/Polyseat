// Package web serves the interface itself.
//
// Plain HTML, CSS and JavaScript, embedded in the binary, no build step and no
// framework. Not minimalism for its own sake: the daemon has to be installable
// on a machine that has no Node toolchain and no intention of getting one, and
// a page that talks to five endpoints does not need more than this.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed index.html app.js style.css
var files embed.FS

// Handler serves the static interface.
func Handler() http.Handler {
	sub, err := fs.Sub(files, ".")
	if err != nil {
		panic(err)
	}

	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Everything is generated fresh on each load and the daemon is often
		// updated in place during development, so a cached page is only ever a
		// confusing one.
		w.Header().Set("Cache-Control", "no-store")

		// No rewriting of "/" to "/index.html" here. The file server serves
		// the index for a directory by itself and redirects the explicit path
		// back to "/", so rewriting turns the root into a redirect loop.
		fileServer.ServeHTTP(w, r)
	})
}
