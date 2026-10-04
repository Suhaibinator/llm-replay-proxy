package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func request(method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func notFoundPage(t *testing.T) string {
	t.Helper()
	page, err := fs.ReadFile(assets, "out/404.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(page)
}

func TestDirectoriesAreNotListed(t *testing.T) {
	page := notFoundPage(t)
	for _, target := range []string{"/_next/", "/_next/static/", "/_next", "/_next/static/chunks/"} {
		w := request(http.MethodGet, target)
		if w.Code != http.StatusNotFound || w.Body.String() != page {
			t.Errorf("%s = %d %.80q", target, w.Code, w.Body.String())
		}
	}
}

func TestUnknownPathServesNotFoundPage(t *testing.T) {
	page := notFoundPage(t)
	w := request(http.MethodGet, "/no/such/page")
	if w.Code != http.StatusNotFound || w.Body.String() != page || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("unknown path = %d %q %.80q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers missing on 404")
	}
	head := request(http.MethodHead, "/no/such/page")
	if head.Code != http.StatusNotFound || head.Body.Len() != 0 {
		t.Fatalf("HEAD unknown path = %d with %d body bytes", head.Code, head.Body.Len())
	}
}

func TestIndexAndStaticAssets(t *testing.T) {
	index := request(http.MethodGet, "/")
	if index.Code != http.StatusOK || !strings.Contains(index.Body.String(), "<html") {
		t.Fatalf("index = %d", index.Code)
	}
	if index.Header().Get("Cache-Control") != "" {
		t.Fatal("index.html must not be cached as immutable")
	}
	var asset string
	_ = fs.WalkDir(assets, "out/_next/static", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && asset == "" {
			asset = strings.TrimPrefix(p, "out")
		}
		return nil
	})
	if asset == "" {
		t.Fatal("no static asset in export")
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := request(method, asset)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("%s %s = %d cache %q", method, asset, w.Code, w.Header().Get("Cache-Control"))
		}
	}
}

func TestOnlyGetAndHeadAreAllowed(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		w := request(method, "/")
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s / = %d allow %q", method, w.Code, w.Header().Get("Allow"))
		}
	}
}

func TestPathTraversalStaysInsideExport(t *testing.T) {
	for _, target := range []string{"/../embed.go", "/..%2fembed.go", "/_next/../../embed.go", "/%2e%2e/embed.go"} {
		w := request(http.MethodGet, target)
		if strings.Contains(w.Body.String(), "go:embed") {
			t.Fatalf("%s escaped the export root", target)
		}
	}
}
