package config

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/llm-replay-proxy/internal/proxy"
	"github.com/local/llm-replay-proxy/internal/store"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestKMSCredentialReachesUpstreamButNotSnapshot(t *testing.T) {
	server, factory := kmsFixture(t)
	const secret = "kms-provider-credential-should-never-be-persisted"
	server.SetSecret("demo/proxy", "provider-key", []byte(secret))
	cfg, err := load(context.Background(), localConfig(t, `{"kms":{"endpoint":"fake","namespace":"demo/proxy"},"default_provider":"p","providers":{"p":{"upstreams":{"/v1/chat/completions":{"url":"https://upstream.example/v1/chat/completions","api_key_secret":{"key":"provider-key"}}}}}}`), factory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "recordings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	settings, err := db.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.Mode = "record"
	if err = db.SetSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	up := cfg.Providers["p"].Upstreams["/v1/chat/completions"]
	client := &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("resolved credential was not sent to fixed upstream")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"chat1","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`))}, nil
	})}
	handler := proxy.New(db, proxy.Config{Client: client, DefaultProvider: "p", Providers: map[string]map[string]proxy.Upstream{"p": {"/v1/chat/completions": {URL: up.URL, APIKey: up.APIKey}}}})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`)))
	if recorder.Code != 200 {
		t.Fatalf("inference status %d", recorder.Code)
	}
	recordings, err := db.Recordings(context.Background(), settings.ActiveCollectionID)
	if err != nil || len(recordings) != 1 {
		t.Fatalf("recording failed: %v", err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.sqlite")
	if err = db.Export(context.Background(), settings.ActiveCollectionID, snapshot); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("KMS credential persisted in snapshot")
	}
	if strings.Contains(string(recordings[0].MatchingInput), secret) {
		t.Fatal("KMS credential included in matching identity")
	}
}
