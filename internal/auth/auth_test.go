package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var testKeyText = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

func testAuthority(t *testing.T, now time.Time) *Authority {
	t.Helper()
	k, err := ParseKey(testKeyText)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(k)
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	return a
}

// sign builds an arbitrary compact JWT, signing with HMAC-SHA256 over key
// when key is non-nil (regardless of the header's alg).
func sign(t *testing.T, header, claims map[string]any, key []byte) string {
	t.Helper()
	enc := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	input := enc(header) + "." + enc(claims)
	if key == nil {
		return input + "."
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func TestIssueAndVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	a := testAuthority(t, now)
	token, issued, err := a.Issue("ci-runner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "ci-runner" || !got.ExpiresAt.Equal(now.Add(time.Hour)) || got.ID == "" || got.ID != issued.ID {
		t.Fatalf("claims = %+v", got)
	}
	other, _, _ := a.Issue("ci-runner", time.Hour)
	if other == token {
		t.Fatal("tokens must carry a unique jti")
	}
	if _, def, _ := a.Issue(" ", time.Hour); def.Subject != DefaultSubject {
		t.Fatalf("default subject = %q", def.Subject)
	}
	if _, _, err := a.Issue("x", -time.Second); err == nil {
		t.Fatal("negative ttl accepted")
	}
}

func TestNoExpiryToken(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	a := testAuthority(t, now)
	token, claims, err := a.Issue("", 0)
	if err != nil || !claims.ExpiresAt.IsZero() {
		t.Fatalf("%v %+v", err, claims)
	}
	a.now = func() time.Time { return now.Add(10 * 365 * 24 * time.Hour) }
	if _, err := a.Verify(token); err != nil {
		t.Fatalf("non-expiring token rejected: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	a := testAuthority(t, now)
	key, _ := ParseKey(testKeyText)
	valid := map[string]any{"iss": Issuer, "sub": "x", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	with := func(k string, v any) map[string]any {
		c := map[string]any{}
		for kk, vv := range valid {
			c[kk] = vv
		}
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
		return c
	}
	hs256 := map[string]any{"alg": "HS256", "typ": "JWT"}
	good := sign(t, hs256, valid, key.b)
	if _, err := a.Verify(good); err != nil {
		t.Fatalf("hand-built valid token rejected: %v", err)
	}
	parts := strings.Split(good, ".")
	flip := []byte(parts[2])
	if flip[0] == 'A' {
		flip[0] = 'B'
	} else {
		flip[0] = 'A'
	}
	payload, _ := json.Marshal(with("sub", "admin"))
	cases := map[string]string{
		"expired":           sign(t, hs256, with("exp", now.Add(-time.Minute).Unix()), key.b),
		"not yet valid":     sign(t, hs256, with("nbf", now.Add(time.Hour).Unix()), key.b),
		"issued in future":  sign(t, hs256, with("iat", now.Add(time.Hour).Unix()), key.b),
		"missing iat":       sign(t, hs256, with("iat", nil), key.b),
		"wrong issuer":      sign(t, hs256, with("iss", "someone-else"), key.b),
		"missing issuer":    sign(t, hs256, with("iss", nil), key.b),
		"wrong key":         sign(t, hs256, valid, []byte("ffffffffffffffffffffffffffffffff")),
		"alg none":          sign(t, map[string]any{"alg": "none", "typ": "JWT"}, valid, nil),
		"alg none signed":   sign(t, map[string]any{"alg": "none", "typ": "JWT"}, valid, key.b),
		"alg HS512":         sign(t, map[string]any{"alg": "HS512", "typ": "JWT"}, valid, key.b),
		"alg RS256":         sign(t, map[string]any{"alg": "RS256", "typ": "JWT"}, valid, key.b),
		"tampered sig":      parts[0] + "." + parts[1] + "." + string(flip),
		"tampered payload":  parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + parts[2],
		"stripped sig":      parts[0] + "." + parts[1] + ".",
		"empty":             "",
		"garbage":           "not-a-jwt",
		"oversized":         strings.Repeat("a", maxTokenBytes+1),
		"padded base64 sig": good + "=",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := a.Verify(token); err != ErrInvalidToken {
				t.Fatalf("Verify = %v, want ErrInvalidToken", err)
			}
		})
	}
	// Small clock skew is tolerated.
	skewed := sign(t, hs256, with("iat", now.Add(10*time.Second).Unix()), key.b)
	if _, err := a.Verify(skewed); err != nil {
		t.Fatalf("leeway not applied: %v", err)
	}
}

func TestTokenFromOtherKeyRejected(t *testing.T) {
	a := testAuthority(t, time.Now())
	k2, _ := GenerateKey()
	b, _ := New(k2)
	token, _, _ := b.Issue("x", time.Hour)
	if _, err := a.Verify(token); err == nil {
		t.Fatal("token signed by another key accepted")
	}
}

func TestParseKey(t *testing.T) {
	raw := []byte("0123456789abcdef0123456789abcdef")
	for _, text := range []string{
		base64.StdEncoding.EncodeToString(raw) + "\n",
		base64.RawStdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
	} {
		if _, err := ParseKey(text); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
	}
	for _, text := range []string{"", "   ", "!!!", base64.StdEncoding.EncodeToString(raw[:31])} {
		if _, err := ParseKey(text); err == nil {
			t.Fatalf("%q accepted", text)
		}
	}
	if _, err := New(Key{b: raw[:16]}); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestKeyNeverPrints(t *testing.T) {
	k, _ := ParseKey(testKeyText)
	wrapper := struct{ K Key }{k}
	for _, s := range []string{fmt.Sprint(k), fmt.Sprintf("%+v %#v %x %s %q", k, k, k, k, k), fmt.Sprintf("%+v %#v", wrapper, wrapper)} {
		if strings.Contains(s, "0123") || strings.Contains(s, "3031") || strings.Contains(s, testKeyText[:8]) {
			t.Fatalf("key leaked: %s", s)
		}
	}
	if _, err := json.Marshal(wrapper); err == nil {
		t.Fatal("key serialized to JSON")
	}
}

func TestLoadOrCreateKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "replay.sqlite.jwt-key")
	k1, created, err := LoadOrCreateKeyFile(path)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v, want 0600", info.Mode().Perm())
	}
	k2, created, err := LoadOrCreateKeyFile(path)
	if err != nil || created || string(k1.b) != string(k2.b) {
		t.Fatalf("reload: created=%v err=%v same=%v", created, err, string(k1.b) == string(k2.b))
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
	if DefaultKeyPath("/data/replay.sqlite") != "/data/replay.sqlite.jwt-key" {
		t.Fatal("unexpected default key path")
	}
}

func TestConcurrentKeyCreationAgrees(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	keys := make([]Key, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Go(func() {
			k, _, err := LoadOrCreateKeyFile(path)
			if err != nil {
				t.Error(err)
			}
			keys[i] = k
		})
	}
	wg.Wait()
	for _, k := range keys[1:] {
		if string(k.b) != string(keys[0].b) {
			t.Fatal("concurrent creators produced different keys")
		}
	}
}

// --- middleware ---

func testServer(t *testing.T, a *Authority) (http.Handler, *int) {
	t.Helper()
	hits := new(int)
	mux := http.NewServeMux()
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		w.Write([]byte("ok"))
	})
	mux.Handle("/v1/chat/completions", ok)
	mux.Handle("/v1/messages", ok)
	mux.Handle("/api/", ok)
	mux.Handle("/healthz", ok)
	mux.Handle("/", ok)
	return a.Middleware(mux), hits
}

func do(h http.Handler, method, target string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMiddlewareCredentialForms(t *testing.T) {
	now := time.Now()
	a := testAuthority(t, now)
	h, _ := testServer(t, a)
	token, _, _ := a.Issue("client", time.Hour)
	cases := []struct {
		name   string
		target string
		set    func(*http.Request)
		want   int
	}{
		{"bearer", "/v1/chat/completions", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }, 200},
		{"bearer lowercase scheme", "/v1/chat/completions", func(r *http.Request) { r.Header.Set("Authorization", "bearer "+token) }, 200},
		{"x-api-key", "/v1/messages", func(r *http.Request) { r.Header.Set("x-api-key", token) }, 200},
		{"x-api-key on api", "/api/settings", func(r *http.Request) { r.Header.Set("x-api-key", token) }, 200},
		{"cookie on api", "/api/settings", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: token}) }, 200},
		{"cookie on inference", "/v1/chat/completions", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: token}) }, 401},
		{"missing on inference", "/v1/chat/completions", nil, 401},
		{"missing on api", "/api/collections", nil, 401},
		{"api root", "/api", nil, 401},
		{"unknown v1 path", "/v1/models", nil, 401},
		{"placeholder key", "/v1/chat/completions", func(r *http.Request) { r.Header.Set("Authorization", "Bearer local-only") }, 401},
		{"basic auth", "/v1/chat/completions", func(r *http.Request) { r.SetBasicAuth("u", token) }, 401},
		{"wrong cookie name", "/api/settings", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "other", Value: token}) }, 401},
		{"invalid bearer valid x-api-key", "/v1/messages", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer nope")
			r.Header.Set("x-api-key", token)
		}, 200},
		{"dot segments", "/static/../api/settings", nil, 401},
		{"healthz open", "/healthz", nil, 200},
		{"console open", "/", nil, 200},
		{"static asset open", "/_next/static/chunk.js", nil, 200},
		{"apiary not api", "/apiary", nil, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.target, nil)
			r.URL.Path = tc.target // keep dot segments as a raw client would send them
			if tc.set != nil {
				tc.set(r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d (%s)", w.Code, tc.want, w.Body)
			}
			if tc.want == 401 {
				if w.Header().Get("WWW-Authenticate") == "" || !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Bearer") {
					t.Fatal("missing WWW-Authenticate: Bearer")
				}
				var body struct {
					Error struct{ Code, Message string } `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != "unauthorized" || body.Error.Message == "" {
					t.Fatalf("error shape: %s", w.Body)
				}
				if w.Header().Get("Content-Type") != "application/json" {
					t.Fatal("401 is not JSON")
				}
			}
		})
	}
}

func TestMiddlewareExpiredToken(t *testing.T) {
	now := time.Now()
	a := testAuthority(t, now.Add(-2*time.Hour))
	token, _, _ := a.Issue("client", time.Hour)
	a.now = func() time.Time { return now }
	h, hits := testServer(t, a)
	w := do(h, http.MethodPost, "/v1/responses", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) })
	if w.Code != 401 || *hits != 0 {
		t.Fatalf("expired token: status %d hits %d", w.Code, *hits)
	}
}

func TestConsoleLoginFlow(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	a := testAuthority(t, now)
	h, hits := testServer(t, a)
	token, _, _ := a.Issue("browser", 2*time.Hour)

	for _, target := range []string{"/auth?token=" + token, "/?token=" + token} {
		w := do(h, http.MethodGet, target, nil)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
			t.Fatalf("%s: status %d location %q", target, w.Code, w.Header().Get("Location"))
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("login response must not be cached or leak a referrer")
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("cookies: %v", cookies)
		}
		c := cookies[0]
		if c.Name != CookieName || c.Value != token || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure {
			t.Fatalf("cookie attributes: %+v", c)
		}
		if c.MaxAge != int((2 * time.Hour).Seconds()) {
			t.Fatalf("cookie Max-Age %d, want token lifetime", c.MaxAge)
		}
		// The cookie then authorizes the console API.
		w = do(h, http.MethodGet, "/api/settings", func(r *http.Request) { r.AddCookie(c) })
		if w.Code != 200 {
			t.Fatalf("cookie rejected: %d", w.Code)
		}
	}
	if *hits != 2 {
		t.Fatalf("login must not reach the console handler; hits=%d", *hits)
	}

	// TLS requests get a Secure cookie.
	r := httptest.NewRequest(http.MethodGet, "https://proxy.example/auth?token="+token, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if c := w.Result().Cookies(); len(c) != 1 || !c[0].Secure {
		t.Fatalf("TLS cookie not Secure: %+v", c)
	}

	// A non-expiring token gets a long-lived cookie.
	forever, _, _ := a.Issue("browser", 0)
	w = do(h, http.MethodGet, "/auth?token="+forever, nil)
	if c := w.Result().Cookies(); len(c) != 1 || c[0].MaxAge != int(maxCookieAge.Seconds()) {
		t.Fatalf("non-expiring cookie: %+v", c)
	}

	for _, target := range []string{"/auth", "/auth?token=", "/auth?token=garbage", "/?token=garbage"} {
		w := do(h, http.MethodGet, target, nil)
		if w.Code != 401 || len(w.Result().Cookies()) != 0 {
			t.Fatalf("%s: status %d cookies %v", target, w.Code, w.Result().Cookies())
		}
	}
	if w := do(h, http.MethodPost, "/auth?token="+token, nil); w.Code != http.StatusMethodNotAllowed || len(w.Result().Cookies()) != 0 {
		t.Fatalf("POST login: %d", w.Code)
	}
}

func TestProtected(t *testing.T) {
	for p, want := range map[string]bool{
		"/v1/chat/completions": true, "/v1/responses": true, "/v1/messages": true, "/v1": true,
		"/api/": true, "/api": true, "/api/collections/1/export": true, "/x/../api/settings": true,
		"/": false, "/healthz": false, "/index.html": false, "/_next/static/a.js": false, "/v10": false, "/apis": false,
	} {
		if Protected(p) != want {
			t.Errorf("Protected(%q) = %v", p, !want)
		}
	}
}
