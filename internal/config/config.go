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
	"strings"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

type Upstream struct {
	URL          string            `json:"url"`
	APIKey       string            `json:"api_key,omitempty"`
	APIKeySecret *SecretRef        `json:"api_key_secret,omitempty"`
	APIKeyEnv    string            `json:"api_key_env,omitempty"`
	Identity     string            `json:"identity,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
}
type Config struct {
	KMS       *KMS                `json:"kms,omitempty"`
	Listen    string              `json:"listen"`
	Database  string              `json:"database"`
	Upstreams map[string]Upstream `json:"upstreams"`
	Auth      Auth                `json:"auth"`
}

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
	return Config{Listen: "127.0.0.1:8080", Database: "replay.sqlite", Upstreams: map[string]Upstream{}}
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
			for _, u := range remote.Upstreams {
				if u.APIKey != "" {
					return c, errors.New("KMS configuration parameters must use secret references instead of inline api_key values")
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
	if c.Upstreams == nil {
		c.Upstreams = map[string]Upstream{}
	}
	for _, p := range []struct{ route, prefix string }{{"/v1/chat/completions", "CHAT"}, {"/v1/responses", "RESPONSES"}, {"/v1/messages", "ANTHROPIC"}} {
		u, exists := c.Upstreams[p.route]
		if v, ok := os.LookupEnv("REPLAY_" + p.prefix + "_URL"); ok {
			u.URL = v
		}
		overridden := false
		if u.APIKeyEnv != "" {
			if v, ok := os.LookupEnv(u.APIKeyEnv); ok {
				u.APIKey = v
				overridden = true
			}
		}
		if v, ok := os.LookupEnv("REPLAY_" + p.prefix + "_API_KEY"); ok {
			u.APIKey = v
			overridden = true
		}
		if u.APIKeySecret != nil && u.APIKey == "" && !overridden && u.URL != "" {
			if client == nil {
				return c, errors.New("api_key_secret requires KMS bootstrap configuration")
			}
			ref := u.APIKeySecret
			if ref.Key == "" {
				return c, errors.New("api_key_secret.key is required")
			}
			if ref.Version > 0 && ref.Label != "" {
				return c, errors.New("api_key_secret must choose either version or label")
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
					return c, errors.New("KMS binding key environment variable is missing or empty")
				}
				options = append(options, kmsclient.WithBindingKey(value))
			}
			secret, err := client.GetSecret(ctx, ref.Key, options...)
			if err != nil {
				return c, kmsReadError("upstream secret", err)
			}
			u.APIKey = secret.StringValue()
			if u.APIKey == "" {
				return c, errors.New("KMS upstream secret is empty")
			}
		}
		if exists || u.URL != "" {
			c.Upstreams[p.route] = u
		}
	}
	if err := resolveSigningKey(ctx, &c, client); err != nil {
		return c, err
	}
	if v, ok := os.LookupEnv("REPLAY_LISTEN"); ok {
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
	for route, u := range c.Upstreams {
		switch route {
		case "/v1/chat/completions", "/v1/responses", "/v1/messages":
		default:
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
