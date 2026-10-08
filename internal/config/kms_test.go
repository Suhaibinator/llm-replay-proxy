package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func kmsFixture(t *testing.T) (*kmsclienttest.Server, kmsFactory) {
	t.Helper()
	server, err := kmsclienttest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	return server, func(k KMS) (kmsReader, error) {
		return kmsclient.NewClient(kmsclient.Config{Endpoint: server.Target(), Namespace: k.Namespace, DialOptions: server.DialOptions()})
	}
}
func localConfig(t *testing.T, raw string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestKMSConfigSecretsAndOverrides(t *testing.T) {
	server, factory := kmsFixture(t)
	server.SetParameter("demo/proxy", "config", `{"listen":"127.0.0.1:9010","database":"remote.sqlite","default_provider":"p","providers":{"p":{"upstreams":{"/v1/responses":{"url":"https://provider.example/v1/responses","headers":{"x-tier":"remote"},"api_key_env":"TEST_RESPONSES_KEY","api_key_secret":{"key":"provider-key","version":1,"binding_key_env":"TEST_KMS_BINDING"}}}}}}`)
	server.SetSecretVersion("demo/proxy", "provider-key", []byte("kms-secret"), "string", 1)
	server.SetSecretVersionCredentials("demo/proxy", "provider-key", 1, "binding-secret")
	t.Setenv("TEST_KMS_BINDING", "binding-secret")
	path := localConfig(t, `{"kms":{"endpoint":"fake","namespace":"demo/proxy","config_key":"config"},"default_provider":"p","providers":{"p":{"upstreams":{"/v1/responses":{"headers":{"x-tier":"local"}}}}}}`)
	c, err := load(context.Background(), path, factory)
	if err != nil {
		t.Fatal(err)
	}
	u := c.Providers["p"].Upstreams["/v1/responses"]
	if c.Listen != "127.0.0.1:9010" || c.Database != "remote.sqlite" || u.Headers["x-tier"] != "local" || u.URL != "https://provider.example/v1/responses" || u.APIKey != "kms-secret" {
		t.Fatalf("configuration fields did not resolve as expected")
	}
	// Environment wins without reading a revoked/unavailable secret.
	server.SetSecretError("demo/proxy", "provider-key", status.Error(codes.PermissionDenied, "not authorized"))
	t.Setenv("TEST_RESPONSES_KEY", "env-secret")
	t.Setenv("REPLAY_LISTEN", "127.0.0.1:9011")
	c, err = load(context.Background(), path, factory)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9011" || c.Providers["p"].Upstreams["/v1/responses"].APIKey != "env-secret" {
		t.Fatal("environment did not override KMS")
	}
}
func TestKMSConfigVersionPin(t *testing.T) {
	server, factory := kmsFixture(t)
	server.SetParameterVersion("demo/proxy", "config", `{"listen":"127.0.0.1:9011"}`, "json", 1)
	server.SetParameterVersion("demo/proxy", "config", `{"listen":"127.0.0.1:9012"}`, "json", 2)
	c, err := load(context.Background(), localConfig(t, `{"kms":{"endpoint":"fake","namespace":"demo/proxy","config_key":"config","config_version":1}}`), factory)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9011" {
		t.Fatal("parameter version was not pinned")
	}
}
func TestKMSFailureDoesNotFallbackOrLeak(t *testing.T) {
	server, factory := kmsFixture(t)
	server.SetParameterError("demo/proxy", "config", status.Error(codes.Unavailable, "secret-plaintext-do-not-log"))
	_, err := load(context.Background(), localConfig(t, `{"kms":{"endpoint":"fake","namespace":"demo/proxy","config_key":"config"},"listen":"127.0.0.1:8888"}`), factory)
	if err == nil || strings.Contains(err.Error(), "secret-plaintext-do-not-log") {
		t.Fatalf("unsafe error: %v", err)
	}
}
func TestLocalConfigNeverConnectsToKMS(t *testing.T) {
	_, err := load(context.Background(), "", func(KMS) (kmsReader, error) {
		t.Fatal("unexpected KMS connection")
		return nil, errors.New("unexpected")
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestKMSRejectsBootstrapAndInlineSecretsInParameter(t *testing.T) {
	for _, raw := range []string{`{"kms":{"endpoint":"replacement"}}`, `{"default_provider":"p","providers":{"p":{"upstreams":{"/v1/responses":{"url":"https://provider.example/v1/responses","api_key":"do-not-store-as-parameter"}}}}}`, `{"unknown":"secret"}`, `null`} {
		t.Run(raw, func(t *testing.T) {
			server, factory := kmsFixture(t)
			server.SetParameter("demo/proxy", "config", raw)
			_, err := load(context.Background(), localConfig(t, `{"kms":{"endpoint":"fake","namespace":"demo/proxy","config_key":"config"}}`), factory)
			if err == nil {
				t.Fatal("unsafe remote config accepted")
			}
			if strings.Contains(err.Error(), "do-not-store-as-parameter") {
				t.Fatal("secret appeared in error")
			}
		})
	}
}
func TestKMSTransportValidation(t *testing.T) {
	for _, k := range []KMS{{}, {Endpoint: "example:8443", CertFile: "one"}, {Endpoint: "example:8443", Insecure: true, CAFile: "ca"}, {Endpoint: "example:8443", TimeoutMS: -1}, {Endpoint: "example:8443", TimeoutMS: 60001}} {
		if c, err := newKMSClient(k); err == nil {
			c.Close()
			t.Fatalf("invalid bootstrap accepted: %+v", k)
		}
	}
}
func TestKMSExplicitEmptyCredentialSkipsSecretForReplay(t *testing.T) {
	_, factory := kmsFixture(t)
	t.Setenv("TEST_EMPTY_KEY", "")
	c, err := load(context.Background(), localConfig(t, `{"kms":{"endpoint":"fake","namespace":"demo/proxy"},"default_provider":"p","providers":{"p":{"upstreams":{"/v1/responses":{"url":"https://provider.example/v1/responses","api_key_env":"TEST_EMPTY_KEY","api_key_secret":{"key":"absent"}}}}}}`), factory)
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers["p"].Upstreams["/v1/responses"].APIKey != "" {
		t.Fatal("empty credential override not preserved")
	}
}

func TestMergeJSONReplacesSecretReferencesWholesale(t *testing.T) {
	remote := `{"listen":"127.0.0.1:9010","default_provider":"p","providers":{"p":{"upstreams":{"/v1/responses":{"url":"https://provider.example/v1/responses","headers":{"x-a":"remote","x-b":"remote"},"api_key_secret":{"key":"providers/openai","version":3,"binding_key_env":"REMOTE_BINDING"}}}}}}`
	for _, tc := range []struct {
		name, local string
		want        SecretRef
	}{
		{"key only", `{"default_provider":"p","providers":{"p":{"upstreams":{"/v1/responses":{"api_key_secret":{"key":"other-key"}}}}}}`, SecretRef{Key: "other-key"}},
		{"key and label", `{"default_provider":"p","providers":{"p":{"upstreams":{"/v1/responses":{"api_key_secret":{"key":"other-key","label":"prod"}}}}}}`, SecretRef{Key: "other-key", Label: "prod"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := mergeJSON([]byte(remote), []byte(tc.local))
			if err != nil {
				t.Fatal(err)
			}
			c := defaults()
			if err = decodeConfig(merged, &c); err != nil {
				t.Fatal(err)
			}
			u := c.Providers["p"].Upstreams["/v1/responses"]
			if u.APIKeySecret == nil || *u.APIKeySecret != tc.want {
				t.Fatalf("secret ref = %+v, want %+v", u.APIKeySecret, tc.want)
			}
			if u.URL != "https://provider.example/v1/responses" || c.Listen != "127.0.0.1:9010" || u.Headers["x-a"] != "remote" {
				t.Fatal("non-secret fields were not deep merged")
			}
		})
	}
	// Ordinary objects such as headers still merge key by key.
	merged, err := mergeJSON([]byte(remote), []byte(`{"default_provider":"p","providers":{"p":{"upstreams":{"/v1/responses":{"headers":{"x-b":"local"}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	c := defaults()
	if err = decodeConfig(merged, &c); err != nil {
		t.Fatal(err)
	}
	u := c.Providers["p"].Upstreams["/v1/responses"]
	if u.Headers["x-a"] != "remote" || u.Headers["x-b"] != "local" || u.APIKeySecret == nil || u.APIKeySecret.Version != 3 {
		t.Fatal("deep merge of headers or untouched secret reference regressed")
	}
}

func TestKMSLocalSecretOverrideDoesNotInheritRemoteVersion(t *testing.T) {
	server, factory := kmsFixture(t)
	server.SetParameter("demo/proxy", "config", `{"default_provider":"p","providers":{"p":{"upstreams":{"/v1/responses":{"url":"https://provider.example/v1/responses","api_key_secret":{"key":"provider-key","version":3}}}}}}`)
	server.SetSecretVersion("demo/proxy", "provider-key", []byte("remote-secret"), "string", 3)
	server.SetSecretVersion("demo/proxy", "other-key", []byte("local-secret"), "string", 1)
	path := localConfig(t, `{"kms":{"endpoint":"fake","namespace":"demo/proxy","config_key":"config"},"default_provider":"p","providers":{"p":{"upstreams":{"/v1/responses":{"api_key_secret":{"key":"other-key"}}}}}}`)
	c, err := load(context.Background(), path, factory)
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers["p"].Upstreams["/v1/responses"].APIKey != "local-secret" {
		t.Fatal("local secret override did not select the current version of its own key")
	}
}
