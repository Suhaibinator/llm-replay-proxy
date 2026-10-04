// Package web serves the statically exported Next.js control panel.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed all:out
var assets embed.FS

func Handler() http.Handler {
	root, err := fs.Sub(assets, "out")
	if err != nil {
		panic(err)
	}
	return handler(root)
}

// handler serves files from root. Directories are served only through their
// index.html (never listed), unknown paths get the exported 404 page, and
// build-hashed /_next/static assets are cacheable forever.
func handler(root fs.FS) http.Handler {
	notFound, _ := fs.ReadFile(root, "404.html")
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		// Clean the same way http.FileServer does; a rooted Clean cannot climb
		// above "/", and http.FS rejects any remaining invalid name.
		clean := path.Clean("/" + r.URL.Path)
		if !servable(root, clean) {
			writeNotFound(w, r, notFound)
			return
		}
		if strings.HasPrefix(clean, "/_next/static/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

// servable reports whether name is a file, or a directory with an index.html.
func servable(root fs.FS, name string) bool {
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		name = "."
	}
	info, err := fs.Stat(root, name)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return true
	}
	index, err := fs.Stat(root, path.Join(name, "index.html"))
	return err == nil && !index.IsDir()
}

func writeNotFound(w http.ResponseWriter, r *http.Request, page []byte) {
	if page == nil {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		_, _ = w.Write(page)
	}
}
