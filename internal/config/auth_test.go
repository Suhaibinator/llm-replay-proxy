package config

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/llm-replay-proxy/internal/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	keyA = base64.StdEncoding.EncodeToString([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	keyB = base64.StdEncoding.EncodeToString([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
)

// sameKey compares keys by the tokens they verify, since Key hides its bytes.
func sameKey(t *testing.T, got auth.Key, wantText string) bool {
	t.Helper()
	want, err := auth.ParseKey(wantText)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(got)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := auth.New(want)
	token, _, _ := b.Issue("x", 60e9)
	_, err = a.Verify(token)
	return err == nil
}

func TestSigningKeyDefaultsToUnresolved(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Auth.SigningKey.IsZero() || c.Auth.Disabled {
		t.Fatal("no key source configured, expected generated-key fallback")
	}
}

func TestSigningKeyFromEnvironmentAndFile(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "jwt.key")
	if err := os.WriteFile(keyFile, []byte(keyB+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := localConfig(t, fmt.Sprintf(`{"auth":{"signing_key_file":%q}}`, keyFile))
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !sameKey(t, c.Auth.SigningKey, keyB) || c.Auth.SigningKeySource != "file "+keyFile {
		t.Fatalf("file key not used (source %q)", c.Auth.SigningKeySource)
	}
	t.Setenv(SigningKeyEnv, keyA)
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !sameKey(t, c.Auth.SigningKey, keyA) || c.Auth.SigningKeySource != SigningKeyEnv {
		t.Fatal("environment key did not take precedence")
	}
	if s := fmt.Sprintf("%+v", c); strings.Contains(s, keyA) || strings.Contains(s, "aaaa") {
		t.Fatal("configuration formatting leaked the signing key")
	}
}

func TestSigningKeyErrors(t *testing.T) {
	short := base64.StdEncoding.EncodeToString([]byte("short"))
	t.Run("short env", func(t *testing.T) {
		t.Setenv(SigningKeyEnv, short)
		if _, err := Load(""); err == nil {
			t.Fatal("short key accepted")
		}
	})
	t.Run("empty env", func(t *testing.T) {
		t.Setenv(SigningKeyEnv, "")
		if _, err := Load(""); err == nil {
			t.Fatal("empty key accepted")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if _, err := Load(localConfig(t, `{"auth":{"signing_key_file":"/nonexistent/jwt.key"}}`)); err == nil {
			t.Fatal("missing key file accepted")
		}
	})
	t.Run("file and secret", func(t *testing.T) {
		if _, err := Load(localConfig(t, `{"auth":{"signing_key_file":"x","signing_key_secret":{"key":"k"}}}`)); err == nil {
			t.Fatal("ambiguous key sources accepted")
		}
	})
	t.Run("secret without kms", func(t *testing.T) {
		if _, err := Load(localConfig(t, `{"auth":{"signing_key_secret":{"key":"k"}}}`)); err == nil {
			t.Fatal("secret reference without KMS accepted")
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		if _, err := Load(localConfig(t, `{"auth":{"signing_key":"inline"}}`)); err == nil {
			t.Fatal("inline signing key field accepted")
		}
	})
}

func TestSigningKeyFromKMS(t *testing.T) {
	server, factory := kmsFixture(t)
	server.SetSecretVersion("demo/proxy", "jwt-key", []byte(keyA), "string", 2)
	path := localConfig(t, `{"kms":{"endpoint":"fake","namespace":"demo/proxy"},"auth":{"signing_key_secret":{"key":"jwt-key","version":2}}}`)
	c, err := load(context.Background(), path, factory)
	if err != nil {
		t.Fatal(err)
	}
	if !sameKey(t, c.Auth.SigningKey, keyA) || c.Auth.SigningKeySource != "KMS secret" {
		t.Fatal("KMS signing key not resolved")
	}
	// The environment wins without reading the (now failing) secret.
	server.SetSecretError("demo/proxy", "jwt-key", status.Error(codes.PermissionDenied, "denied: "+keyA))
	t.Setenv(SigningKeyEnv, keyB)
	if c, err = load(context.Background(), path, factory); err != nil || !sameKey(t, c.Auth.SigningKey, keyB) {
		t.Fatalf("environment did not override KMS: %v", err)
	}
	os.Unsetenv(SigningKeyEnv)
	_, err = load(context.Background(), path, factory)
	if err == nil || strings.Contains(err.Error(), keyA) {
		t.Fatalf("KMS failure not reported safely: %v", err)
	}
	// A remote configuration parameter may reference the secret too.
	server.SetParameter("demo/proxy", "config", `{"auth":{"signing_key_secret":{"key":"jwt-key2"}}}`)
	server.SetSecretVersion("demo/proxy", "jwt-key2", []byte(keyB), "string", 1)
	c, err = load(context.Background(), localConfig(t, `{"kms":{"endpoint":"fake","namespace":"demo/proxy","config_key":"config"}}`), factory)
	if err != nil || !sameKey(t, c.Auth.SigningKey, keyB) {
		t.Fatalf("KMS parameter secret reference: %v", err)
	}
}

func TestAuthDisabledOnlyOnLoopback(t *testing.T) {
	for listen, ok := range map[string]bool{
		"127.0.0.1:8080": true, "[::1]:8080": true, "localhost:8080": true, "127.0.0.2:9": true,
		"0.0.0.0:8080": false, ":8080": false, "[::]:8080": false, "192.168.1.5:8080": false, "example.com:80": false,
	} {
		path := localConfig(t, fmt.Sprintf(`{"listen":%q,"auth":{"disabled":true}}`, listen))
		c, err := Load(path)
		if ok && (err != nil || !c.Auth.Disabled) {
			t.Errorf("%s: %v", listen, err)
		}
		if !ok && err == nil {
			t.Errorf("%s: disabled auth accepted on non-loopback listener", listen)
		}
	}
	// A CLI listen override is validated too.
	path := localConfig(t, `{"auth":{"disabled":true}}`)
	if _, err := LoadWithOverrides(context.Background(), path, Overrides{Listen: "0.0.0.0:8080"}); err == nil {
		t.Fatal("disabled auth accepted after a non-loopback -listen override")
	}
	t.Setenv("REPLAY_LISTEN", "0.0.0.0:8080")
	if _, err := Load(path); err == nil {
		t.Fatal("disabled auth accepted after a non-loopback REPLAY_LISTEN override")
	}
}
