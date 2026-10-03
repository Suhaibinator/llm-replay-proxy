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
	if c.Listen != "127.0.0.1:8080" || len(c.Upstreams) != 0 {
		t.Fatalf("%+v", c)
	}
}
func TestEnvironmentAndLocalConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"upstreams":{"/v1/messages":{"url":"http://localhost:9000/v1/messages","api_key_env":"TEST_PROVIDER_KEY"}}}`), 0600)
	t.Setenv("TEST_PROVIDER_KEY", "secret")
	c, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if c.Upstreams["/v1/messages"].APIKey != "secret" {
		t.Fatal("credential not resolved")
	}
}
func TestRejectSecretURL(t *testing.T) {
	c := Config{Listen: "127.0.0.1:8080", Database: "test", Upstreams: map[string]Upstream{"/v1/responses": {URL: "https://user:secret@example.com/v1/responses"}}}
	if c.Validate() == nil {
		t.Fatal("accepted embedded credentials")
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
func TestExplicitEmptyURLOverrideIsNotIgnored(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"upstreams":{"/v1/responses":{"url":"https://example.com/v1/responses"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REPLAY_RESPONSES_URL", "")
	if _, err := Load(p); err == nil {
		t.Fatal("empty override silently retained previous upstream URL")
	}
}
