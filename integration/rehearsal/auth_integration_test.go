package rehearsal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/local/llm-replay-proxy/internal/auth"
	"github.com/local/llm-replay-proxy/internal/proxy"
	"github.com/local/llm-replay-proxy/internal/store"
)

// Go Common's default HTTP authentication reaches an auth-enforcing proxy
// with an issued token on every protocol, and the token is never forwarded.
func TestGoCommonAuthenticatesWithIssuedToken(t *testing.T) {
	key, err := auth.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	authority, err := auth.New(key)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := authority.Issue("integration", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range protocolCases() {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var forwarded []http.Header
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				forwarded = append(forwarded, r.Header.Clone())
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, textResponse(tc.api, "authenticated answer"))
			}))
			defer upstream.Close()
			db, err := store.Open(filepath.Join(t.TempDir(), "replay.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mux := http.NewServeMux()
			mux.Handle(tc.route, proxy.New(db, proxy.Config{Upstreams: map[string]proxy.Upstream{
				tc.route: {URL: upstream.URL, Identity: "integration-upstream", APIKey: "upstream-secret"},
			}}))
			server := httptest.NewServer(authority.Middleware(mux))
			defer server.Close()
			setMode(t, db, "record")

			anonymous, err := New(context.Background(), server.URL, "demo-model")
			if err != nil {
				t.Fatal(err)
			}
			defer anonymous.Close()
			if _, err := anonymous.Text(context.Background(), tc.api, "hello"); err == nil {
				t.Fatal("request without a token succeeded")
			}

			client, err := NewWithToken(context.Background(), server.URL, "demo-model", token)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if got, err := client.Text(context.Background(), tc.api, "hello"); err != nil || got != "authenticated answer" {
				t.Fatalf("authenticated request = %q, %v", got, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(forwarded) != 1 {
				t.Fatalf("upstream calls = %d, want 1", len(forwarded))
			}
			for name, values := range forwarded[0] {
				for _, v := range values {
					if strings.Contains(v, token) {
						t.Fatalf("caller token forwarded upstream in %s", name)
					}
				}
			}
		})
	}
}
