package auth

import (
	"encoding/json"
	"net/http"
	"path"
	"strings"
	"time"
)

const (
	// CookieName holds the console's token after a login link is opened.
	CookieName = "replay_proxy_token"
	// LoginPath accepts `?token=<jwt>` and sets the console cookie.
	LoginPath = "/auth"
	// maxCookieAge is used for tokens without `exp` (browsers cap persistent
	// cookies at roughly 400 days).
	maxCookieAge = 400 * 24 * time.Hour
)

// Protected reports whether a request path requires a token: every inference
// route under /v1/ and every control route under /api/. The cleaned path is
// checked as well so dot segments cannot reach a protected handler unchecked.
func Protected(p string) bool {
	for _, candidate := range []string{p, path.Clean("/" + p)} {
		for _, prefix := range []string{"/v1", "/api"} {
			if candidate == prefix || strings.HasPrefix(candidate, prefix+"/") {
				return true
			}
		}
	}
	return false
}

// Middleware requires a valid token for Protected paths and serves the console
// login link. /healthz and the static console pages stay public so the
// console can load and explain how to sign in.
//
// Accepted credentials:
//   - `Authorization: Bearer <jwt>` (OpenAI SDKs, curl)
//   - `x-api-key: <jwt>` (Anthropic SDK)
//   - the console cookie, for /api/ only. Inference routes do not accept it,
//     so a page on another port of the same host cannot spend upstream keys
//     through the browser's cookie.
func (a *Authority) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == LoginPath || (r.URL.Path == "/" && r.URL.Query().Has("token")) {
			a.login(w, r)
			return
		}
		if !Protected(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !a.authorized(r) {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Authority) authorized(r *http.Request) bool {
	var candidates []string
	if scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
		candidates = append(candidates, strings.TrimSpace(value))
	}
	if v := r.Header.Get("X-Api-Key"); v != "" {
		candidates = append(candidates, strings.TrimSpace(v))
	}
	if allowsCookie(r.URL.Path) {
		for _, c := range r.CookiesNamed(CookieName) {
			candidates = append(candidates, c.Value)
		}
	}
	for _, token := range candidates {
		if _, err := a.Verify(token); err == nil {
			return true
		}
	}
	return false
}

func allowsCookie(p string) bool {
	clean := path.Clean("/" + p)
	return (p == "/api" || strings.HasPrefix(p, "/api/")) && (clean == "/api" || strings.HasPrefix(clean, "/api/"))
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="llm-replay-proxy"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
		"code":    "unauthorized",
		"message": "a valid access token is required; issue one with `replay-proxy token issue`",
	}})
}

// login exchanges `?token=<jwt>` for an HttpOnly console cookie and
// redirects to the console, removing the token from the address bar and
// browser history. It never issues tokens; it only accepts ones the CLI
// already signed.
func (a *Authority) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := r.URL.Query().Get("token")
	claims, err := a.Verify(token)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="llm-replay-proxy"`)
		http.Error(w, "This console link is invalid or expired. Run `replay-proxy token issue` on the server and open the console link it prints.", http.StatusUnauthorized)
		return
	}
	maxAge := maxCookieAge
	if !claims.ExpiresAt.IsZero() {
		maxAge = min(claims.ExpiresAt.Sub(a.now()), maxCookieAge)
	}
	if maxAge < time.Second {
		maxAge = time.Second
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(maxAge / time.Second),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
