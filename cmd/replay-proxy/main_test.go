package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/local/llm-replay-proxy/internal/auth"
	"github.com/local/llm-replay-proxy/internal/config"
	"github.com/local/llm-replay-proxy/internal/store"
)

func issueCLI(t *testing.T, args ...string) (token string, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := tokenCommand(context.Background(), append([]string{"issue"}, args...), &out, &errOut); err != nil {
		t.Fatalf("token issue: %v (%s)", err, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || strings.Count(lines[0], ".") != 2 {
		t.Fatalf("stdout must contain only the token, got %q", out.String())
	}
	return lines[0], errOut.String()
}

func TestTokenIssueCLI(t *testing.T) {
	t.Setenv(config.SigningKeyEnv, "")
	os.Unsetenv(config.SigningKeyEnv)
	dir := t.TempDir()
	db := filepath.Join(dir, "replay.sqlite")
	token, info := issueCLI(t, "-db", db, "-listen", "0.0.0.0:9123", "-subject", "ci", "-ttl", "1h")
	if _, err := os.Stat(auth.DefaultKeyPath(db)); err != nil {
		t.Fatalf("generated key file missing: %v", err)
	}
	wantLink := "http://127.0.0.1:9123/auth?token=" + url.QueryEscape(token)
	if !strings.Contains(info, wantLink) || !strings.Contains(info, "Subject: ci") {
		t.Fatalf("stderr missing console link or subject:\n%s", info)
	}
	// The server started on the same database verifies the CLI's token.
	c, err := config.LoadWithOverrides(context.Background(), "", config.Overrides{Database: db})
	if err != nil {
		t.Fatal(err)
	}
	a, err := authorityFor(c, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := a.Verify(token)
	if err != nil || claims.Subject != "ci" || time.Until(claims.ExpiresAt) > time.Hour || time.Until(claims.ExpiresAt) < 59*time.Minute {
		t.Fatalf("verify CLI token: %v %+v", err, claims)
	}
	forever, info := issueCLI(t, "-db", db, "-ttl", "0")
	if claims, err := a.Verify(forever); err != nil || !claims.ExpiresAt.IsZero() || !strings.Contains(info, "Expires: never") {
		t.Fatalf("-ttl 0: %v %+v %s", err, claims, info)
	}
	// Rotating the key file invalidates every outstanding token.
	if err := os.Remove(auth.DefaultKeyPath(db)); err != nil {
		t.Fatal(err)
	}
	rotated, err := authorityFor(c, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.Verify(token); err == nil {
		t.Fatal("token survived key rotation")
	}

	var out, errOut bytes.Buffer
	for _, args := range [][]string{nil, {"revoke"}, {"issue", "-ttl", "-1h", "-db", db}, {"issue", "extra", "-db", db}} {
		if err := tokenCommand(context.Background(), args, &out, &errOut); err == nil {
			t.Fatalf("token %v accepted", args)
		}
	}
	disabled := filepath.Join(dir, "disabled.json")
	os.WriteFile(disabled, []byte(`{"auth":{"disabled":true}}`), 0600)
	if err := tokenCommand(context.Background(), []string{"issue", "-config", disabled, "-db", db}, &out, &errOut); err == nil {
		t.Fatal("issued a token while auth is disabled")
	}
	if out.Len() != 0 {
		t.Fatalf("failed commands wrote to stdout: %q", out.String())
	}
}

func TestConsoleURL(t *testing.T) {
	for listen, want := range map[string]string{
		"127.0.0.1:8080": "http://127.0.0.1:8080/auth?token=a.b.c",
		"[::]:8080":      "http://[::1]:8080/auth?token=a.b.c",
		":9000":          "http://127.0.0.1:9000/auth?token=a.b.c",
		"localhost:1":    "http://localhost:1/auth?token=a.b.c",
	} {
		if got := consoleURL(listen, "a.b.c"); got != want {
			t.Errorf("%s: %s, want %s", listen, got, want)
		}
	}
}

// upstreamSpy records what the proxy forwards.
type upstreamSpy struct {
	mu      sync.Mutex
	headers []http.Header
}

func (u *upstreamSpy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.headers = append(u.headers, r.Header.Clone())
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"c1","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
}

func TestServerRequiresTokensAndNeverForwardsThem(t *testing.T) {
	os.Unsetenv(config.SigningKeyEnv)
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy)
	defer upstream.Close()
	dir := t.TempDir()
	cfg := config.Config{Listen: "127.0.0.1:0", Database: filepath.Join(dir, "replay.sqlite"), Upstreams: map[string]config.Upstream{
		"/v1/chat/completions": {URL: upstream.URL + "/v1/chat/completions", APIKey: "upstream-secret"},
	}}
	db, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := authorityFor(cfg, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler(db, cfg, authority))
	defer server.Close()
	token, _, err := authority.Issue("test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, path, body string, set func(*http.Request)) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if set != nil {
			set(req)
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }

	// Public: health and the static console.
	for _, p := range []string{"/healthz", "/"} {
		if resp := send("GET", p, "", nil); resp.StatusCode != 200 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
	// Protected without a token.
	chat := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	for _, tc := range []struct{ method, path, body string }{{"GET", "/api/settings", ""}, {"GET", "/api/history?collection_id=1", ""}, {"POST", "/v1/chat/completions", chat}, {"GET", "/api/collections/1/export", ""}} {
		resp := send(tc.method, tc.path, tc.body, nil)
		if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") {
			t.Fatalf("%s %s without token: %d", tc.method, tc.path, resp.StatusCode)
		}
	}
	if len(spy.headers) != 0 {
		t.Fatal("unauthenticated request reached the upstream")
	}

	// Switch to record mode with a bearer token.
	resp := send("GET", "/api/settings", "", bearer)
	var settings map[string]any
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&settings) != nil {
		t.Fatalf("settings: %d", resp.StatusCode)
	}
	settings["mode"] = "record"
	raw, _ := json.Marshal(settings)
	if resp := send("PUT", "/api/settings", string(raw), func(r *http.Request) {
		r.Header.Set("x-api-key", token)
		r.Header.Set("Content-Type", "application/json")
	}); resp.StatusCode != 200 {
		t.Fatalf("put settings: %d", resp.StatusCode)
	}

	// Record through the proxy with the token in every accepted place.
	resp = send("POST", "/v1/chat/completions", chat, func(r *http.Request) {
		bearer(r)
		r.Header.Set("x-api-key", token)
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
		r.Header.Set("Content-Type", "application/json")
	})
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("authorized inference: %d %s", resp.StatusCode, body)
	}
	if len(spy.headers) != 1 {
		t.Fatalf("upstream calls: %d", len(spy.headers))
	}
	got := spy.headers[0]
	if got.Get("Authorization") != "Bearer upstream-secret" || got.Get("X-Api-Key") != "" || got.Get("Cookie") != "" {
		t.Fatalf("caller credentials leaked upstream or upstream key missing: %v", got)
	}
	for _, values := range got {
		for _, v := range values {
			if strings.Contains(v, token) {
				t.Fatal("caller token forwarded upstream")
			}
		}
	}

	// Console login link sets the cookie, which then authorizes /api reads.
	resp = send("GET", "/auth?token="+url.QueryEscape(token), "", nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" || len(resp.Cookies()) != 1 {
		t.Fatalf("login: %d %v", resp.StatusCode, resp.Cookies())
	}
	cookie := resp.Cookies()[0]
	withCookie := func(r *http.Request) { r.AddCookie(cookie) }
	resp = send("GET", "/api/settings", "", withCookie)
	if resp.StatusCode != 200 {
		t.Fatalf("cookie session: %d", resp.StatusCode)
	}
	collection := int(settings["active_collection_id"].(float64))
	for _, p := range []string{"/api/history?collection_id=", "/api/recordings?collection_id="} {
		resp = send("GET", p+strconv.Itoa(collection), "", withCookie)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || !strings.Contains(string(body), "hello") {
			t.Fatalf("%s: %d %s", p, resp.StatusCode, body)
		}
		if strings.Contains(string(body), token) || strings.Contains(string(body), "upstream-secret") {
			t.Fatalf("%s stored a credential: %s", p, body)
		}
	}
	// The cookie alone does not authorize inference.
	if resp := send("POST", "/v1/chat/completions", chat, withCookie); resp.StatusCode != 401 {
		t.Fatalf("cookie authorized inference: %d", resp.StatusCode)
	}
}

func TestDisabledAuthLeavesRoutesOpen(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{Listen: "127.0.0.1:0", Database: filepath.Join(dir, "replay.sqlite"), Auth: config.Auth{Disabled: true}}
	db, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := authorityFor(cfg, t.Logf)
	if err != nil || a != nil {
		t.Fatalf("disabled auth produced an authority: %v", err)
	}
	if _, err := os.Stat(auth.DefaultKeyPath(cfg.Database)); err == nil {
		t.Fatal("disabled auth generated a key file")
	}
	server := httptest.NewServer(newHandler(db, cfg, nil))
	defer server.Close()
	resp, err := http.Get(server.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("disabled auth: %d", resp.StatusCode)
	}
}
