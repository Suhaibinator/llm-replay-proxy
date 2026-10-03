// Package web serves the statically exported Next.js control panel.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:out
var assets embed.FS

func Handler() http.Handler {
	root, err := fs.Sub(assets, "out")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		files.ServeHTTP(w, r)
	})
}
