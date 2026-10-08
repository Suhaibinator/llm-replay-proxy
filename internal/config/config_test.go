package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	c, e := Load("")
	if e != nil {
		t.Fatal(e)
	}
	if c.Listen != "127.0.0.1:8080" || len(c.Providers) != 0 || c.DefaultProvider != "" {
		t.Fatalf("%+v", c)
	}
}
func TestEnvironmentAndLocalConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"default_provider":"anthropic","providers":{"anthropic":{"upstreams":{"/v1/messages":{"url":"http://localhost:9000/v1/messages","api_key_env":"TEST_PROVIDER_KEY"}}}}}`), 0600)
	t.Setenv("TEST_PROVIDER_KEY", "secret")
	c, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if c.Providers["anthropic"].Upstreams["/v1/messages"].APIKey != "secret" {
		t.Fatal("credential not resolved")
	}
}
func TestCredentialsResolvePerProvider(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"default_provider":"openai","providers":{
		"openai":{"upstreams":{"/v1/chat/completions":{"url":"https://api.openai.com/v1/chat/completions","api_key_env":"TEST_OPENAI_KEY"}}},
		"openrouter":{"upstreams":{"/v1/chat/completions":{"url":"https://openrouter.ai/api/v1/chat/completions","api_key":"inline-key"}}}}}`), 0600)
	t.Setenv("TEST_OPENAI_KEY", "openai-key")
	c, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if c.Providers["openai"].Upstreams["/v1/chat/completions"].APIKey != "openai-key" || c.Providers["openrouter"].Upstreams["/v1/chat/completions"].APIKey != "inline-key" {
		t.Fatal("per-provider credentials not resolved")
	}
	t.Setenv("REPLAY_DEFAULT_PROVIDER", "openrouter")
	if c, e = Load(p); e != nil || c.DefaultProvider != "openrouter" {
		t.Fatalf("default provider override: %v %q", e, c.DefaultProvider)
	}
}
func TestProviderValidation(t *testing.T) {
	up := map[string]Upstream{"/v1/responses": {URL: "https://example.com/v1/responses"}}
	for name, c := range map[string]Config{
		"secret URL":             {DefaultProvider: "p", Providers: map[string]Provider{"p": {Upstreams: map[string]Upstream{"/v1/responses": {URL: "https://user:secret@example.com/v1/responses"}}}}},
		"missing default":        {Providers: map[string]Provider{"p": {Upstreams: up}}},
		"unknown default":        {DefaultProvider: "q", Providers: map[string]Provider{"p": {Upstreams: up}}},
		"default without any":    {DefaultProvider: "p"},
		"uppercase name":         {DefaultProvider: "P", Providers: map[string]Provider{"P": {Upstreams: up}}},
		"reserved name":          {DefaultProvider: "unknown", Providers: map[string]Provider{"unknown": {Upstreams: up}}},
		"no upstreams":           {DefaultProvider: "p", Providers: map[string]Provider{"p": {}}},
		"unsupported route":      {DefaultProvider: "p", Providers: map[string]Provider{"p": {Upstreams: map[string]Upstream{"/v1/embeddings": {URL: "https://example.com/v1/embeddings"}}}}},
		"credential as a header": {DefaultProvider: "p", Providers: map[string]Provider{"p": {Upstreams: map[string]Upstream{"/v1/responses": {URL: "https://example.com/v1/responses", Headers: map[string]string{"Authorization": "x"}}}}}},
	} {
		c.Listen, c.Database = "127.0.0.1:8080", "test"
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := Config{Listen: "127.0.0.1:8080", Database: "test", DefaultProvider: "open-router_2", Providers: map[string]Provider{"open-router_2": {Upstreams: up}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCLIOverridesPrecedeValidation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"listen":"invalid","database":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadWithOverrides(context.Background(), p, Overrides{Listen: "127.0.0.1:9000", Database: "override.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Database != "override.sqlite" || c.Listen != "127.0.0.1:9000" {
		t.Fatal("CLI overrides not applied")
	}
}
