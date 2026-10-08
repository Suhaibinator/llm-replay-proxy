// Package config loads server-only upstream credentials and safe runtime defaults.
package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

type Upstream struct {
	URL          string            `json:"url"`
	APIKey       string            `json:"api_key,omitempty"`
	APIKeySecret *SecretRef        `json:"api_key_secret,omitempty"`
	APIKeyEnv    string            `json:"api_key_env,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
}

// Provider is a named inference provider: the upstream endpoint, credential,
// and fixed headers for each inference route it serves. Callers select one
// per request with the X-Replay-Provider header.
type Provider struct {
	Upstreams map[string]Upstream `json:"upstreams"`
}

type Config struct {
	KMS      *KMS   `json:"kms,omitempty"`
	Listen   string `json:"listen"`
	Database string `json:"database"`
	// DefaultProvider serves requests that do not name a provider.
	DefaultProvider string              `json:"default_provider,omitempty"`
	Providers       map[string]Provider `json:"providers"`
	Auth            Auth                `json:"auth"`
}

// Routes lists the inference routes a provider may configure.
var Routes = []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"}

// providerName is the provider-name grammar. Names travel in a request header
// and in history, so they stay short and unambiguous.
var providerName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Load resolves an optional local file and KMS snapshot once at startup.
// Precedence: defaults, KMS parameter, local file, environment. Secrets are
// fetched only when neither a local credential nor an environment override wins.
func Load(path string) (Config, error) { return LoadContext(context.Background(), path) }
func LoadContext(ctx context.Context, path string) (Config, error) {
	return load(ctx, path, newKMSClient)
}

// Overrides are command-line values applied after every other configuration source.
type Overrides struct{ Listen, Database string }

func LoadWithOverrides(ctx context.Context, path string, overrides Overrides) (Config, error) {
	return loadWithOverrides(ctx, path, newKMSClient, overrides)
}

func decodeConfig(raw []byte, c *Config) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("configuration must be a JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(c); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("configuration must contain exactly one JSON object")
	}
	return nil
}
func defaults() Config {
	return Config{Listen: "127.0.0.1:8080", Database: "replay.sqlite", Providers: map[string]Provider{}}
}
func load(ctx context.Context, path string, factory kmsFactory) (Config, error) {
	return loadWithOverrides(ctx, path, factory, Overrides{})
}
func loadWithOverrides(ctx context.Context, path string, factory kmsFactory, overrides Overrides) (Config, error) {
	local := []byte(`{}`)
	if path != "" {
		var err error
		local, err = os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}
	}
	c := defaults()
	if err := decodeConfig(local, &c); err != nil {
		return c, err
	}
	if err := applyKMSBootstrapEnvironment(&c); err != nil {
		return c, err
	}
	bootstrap := c.KMS
	var client kmsReader
	if bootstrap != nil {
		var err error
		client, err = factory(*bootstrap)
		if err != nil {
			return c, err
		}
		defer client.Close()
		if bootstrap.ConfigKey != "" {
			var options []kmsclient.GetOption
			if bootstrap.ConfigVersion > 0 {
				options = append(options, kmsclient.WithVersion(bootstrap.ConfigVersion))
			}
			parameter, err := client.GetParameter(ctx, bootstrap.ConfigKey, options...)
			if err != nil {
				return c, kmsReadError("configuration parameter", err)
			}
			remote := defaults()
			if err := decodeConfig([]byte(parameter), &remote); err != nil {
				return c, errors.New("KMS configuration parameter is not a valid proxy configuration")
			}
			if remote.KMS != nil {
				return c, errors.New("KMS configuration parameter cannot change KMS bootstrap settings")
			}
			for _, p := range remote.Providers {
				for _, u := range p.Upstreams {
					if u.APIKey != "" {
						return c, errors.New("KMS configuration parameters must use secret references instead of inline api_key values")
					}
				}
			}
			merged, err := mergeJSON([]byte(parameter), local)
			if err != nil {
				return c, err
			}
			c = defaults()
			if err := decodeConfig(merged, &c); err != nil {
				return c, err
			}
			c.KMS = bootstrap
		}
	}
	if c.Providers == nil {
		c.Providers = map[string]Provider{}
	}
	for name, p := range c.Providers {
		for route, u := range p.Upstreams {
			if err := resolveCredential(ctx, &u, client); err != nil {
				return c, fmt.Errorf("provider %s %s: %w", name, route, err)
			}
			p.Upstreams[route] = u
		}
	}
	if err := resolveSigningKey(ctx, &c, client); err != nil {
		return c, err
	}
	if v, ok := os.LookupEnv("REPLAY_DEFAULT_PROVIDER"); ok && v != "" {
		c.DefaultProvider = v
	}
	if v, ok := os.LookupEnv("REPLAY_LISTEN"); ok && v != "" {
		c.Listen = v
	}
	if v, ok := os.LookupEnv("REPLAY_DATABASE"); ok {
		c.Database = v
	}
	if overrides.Listen != "" {
		c.Listen = overrides.Listen
	}
	if overrides.Database != "" {
		c.Database = overrides.Database
	}
	return c, c.Validate()
}

// resolveCredential fills u.APIKey. An inline api_key is used as is; an
// api_key_env variable that is set wins over it; a KMS secret is read only
// when neither supplied a key and the upstream has a URL.
func resolveCredential(ctx context.Context, u *Upstream, client kmsReader) error {
	if u.APIKeyEnv != "" {
		if v, ok := os.LookupEnv(u.APIKeyEnv); ok {
			u.APIKey = v
			return nil
		}
	}
	if u.APIKeySecret == nil || u.APIKey != "" || u.URL == "" {
		return nil
	}
	if client == nil {
		return errors.New("api_key_secret requires KMS bootstrap configuration")
	}
	ref := u.APIKeySecret
	if ref.Key == "" {
		return errors.New("api_key_secret.key is required")
	}
	if ref.Version > 0 && ref.Label != "" {
		return errors.New("api_key_secret must choose either version or label")
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
		return kmsReadError("upstream secret", err)
	}
	u.APIKey = secret.StringValue()
	if u.APIKey == "" {
		return errors.New("KMS upstream secret is empty")
	}
	return nil
}

func (c Config) Validate() error {
	if _, _, e := net.SplitHostPort(c.Listen); e != nil {
		return fmt.Errorf("listen must be host:port: %w", e)
	}
	if c.Database == "" {
		return fmt.Errorf("database path is required")
	}
	if err := c.Auth.validate(c.Listen); err != nil {
		return err
	}
	if len(c.Providers) == 0 {
		if c.DefaultProvider != "" {
			return fmt.Errorf("default_provider %q is not a configured provider", c.DefaultProvider)
		}
		return nil
	}
	if c.DefaultProvider == "" {
		return errors.New("default_provider is required when providers are configured")
	}
	if _, ok := c.Providers[c.DefaultProvider]; !ok {
		return fmt.Errorf("default_provider %q is not a configured provider", c.DefaultProvider)
	}
	for name, p := range c.Providers {
		// "unknown" is how history and the dashboard label calls without a provider.
		if !providerName.MatchString(name) || name == "unknown" {
			return fmt.Errorf("invalid provider name %q: use 1-64 lowercase letters, digits, '-' or '_'", name)
		}
		if len(p.Upstreams) == 0 {
			return fmt.Errorf("provider %s configures no upstreams", name)
		}
		for route, u := range p.Upstreams {
			if err := validateUpstream(route, u); err != nil {
				return fmt.Errorf("provider %s: %w", name, err)
			}
		}
	}
	return nil
}

func validateUpstream(route string, u Upstream) error {
	if !slices.Contains(Routes, route) {
		return fmt.Errorf("unsupported upstream route %q", route)
	}
	parsed, e := url.Parse(u.URL)
	if e != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("upstream %s requires an absolute http(s) URL", route)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("upstream %s URL must not contain user credentials, query parameters, or fragments", route)
	}
	for k, v := range u.Headers {
		if !validHeaderName(k) || strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("invalid upstream header")
		}
		switch strings.ToLower(k) {
		case "authorization", "x-api-key", "api-key", "cookie", "proxy-authorization":
			return fmt.Errorf("use api_key or api_key_env for credentials, not headers")
		}
	}
	return nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}
