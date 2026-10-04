package config

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/local/llm-replay-proxy/internal/auth"
)

// SigningKeyEnv holds a base64 JWT signing key and overrides every other source.
const SigningKeyEnv = "REPLAY_JWT_KEY"

// Auth configures client authentication. Tokens are always required unless
// Disabled is set, which is accepted only for a loopback listen address.
type Auth struct {
	Disabled bool `json:"disabled,omitempty"`
	// SigningKeyFile names a file containing a base64 HMAC key (>= 32 bytes).
	SigningKeyFile string `json:"signing_key_file,omitempty"`
	// SigningKeySecret reads the base64 key from a KMS secret.
	SigningKeySecret *SecretRef `json:"signing_key_secret,omitempty"`

	// SigningKey is the resolved key, empty when none of the sources above
	// is configured (the caller then uses a generated key file next to the
	// database). It is never serialized or printed.
	SigningKey auth.Key `json:"-"`
	// SigningKeySource describes where SigningKey came from, for logs.
	SigningKeySource string `json:"-"`
}

func (a Auth) validate(listen string) error {
	if a.SigningKeyFile != "" && a.SigningKeySecret != nil {
		return errors.New("auth: choose either signing_key_file or signing_key_secret")
	}
	if a.Disabled && !loopbackListen(listen) {
		return errors.New("auth.disabled is allowed only when listen is a loopback address (127.0.0.1, ::1 or localhost)")
	}
	return nil
}

func loopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// resolveSigningKey applies the key precedence REPLAY_JWT_KEY, then
// auth.signing_key_file or auth.signing_key_secret. KMS is read only when
// neither the environment nor a file supplies the key.
func resolveSigningKey(ctx context.Context, c *Config, client kmsReader) error {
	a := &c.Auth
	if a.Disabled {
		return nil
	}
	if a.SigningKeyFile != "" && a.SigningKeySecret != nil {
		return errors.New("auth: choose either signing_key_file or signing_key_secret")
	}
	if v, ok := os.LookupEnv(SigningKeyEnv); ok {
		key, err := auth.ParseKey(v)
		if err != nil {
			return fmt.Errorf("%s: %w", SigningKeyEnv, err)
		}
		a.SigningKey, a.SigningKeySource = key, SigningKeyEnv
		return nil
	}
	if a.SigningKeyFile != "" {
		key, err := auth.ReadKeyFile(a.SigningKeyFile)
		if err != nil {
			return err
		}
		a.SigningKey, a.SigningKeySource = key, "file "+a.SigningKeyFile
		return nil
	}
	ref := a.SigningKeySecret
	if ref == nil {
		return nil
	}
	if client == nil {
		return errors.New("auth.signing_key_secret requires KMS bootstrap configuration")
	}
	if ref.Key == "" {
		return errors.New("auth.signing_key_secret.key is required")
	}
	if ref.Version > 0 && ref.Label != "" {
		return errors.New("auth.signing_key_secret must choose either version or label")
	}
	var options []kmsclient.GetOption
	if ref.Version > 0 {
		options = append(options, kmsclient.WithVersion(ref.Version))
	}
	if ref.Label != "" {
		options = append(options, kmsclient.WithLabel(ref.Label))
	}
	if ref.BindingKeyEnv != "" {
		value, ok := os.LookupEnv(ref.BindingKeyEnv)
		if !ok || value == "" {
			return errors.New("KMS binding key environment variable is missing or empty")
		}
		options = append(options, kmsclient.WithBindingKey(value))
	}
	secret, err := client.GetSecret(ctx, ref.Key, options...)
	if err != nil {
		return kmsReadError("JWT signing key secret", err)
	}
	key, err := auth.ParseKey(secret.StringValue())
	if err != nil {
		return fmt.Errorf("KMS JWT signing key secret: %w", err)
	}
	a.SigningKey, a.SigningKeySource = key, "KMS secret"
	return nil
}
