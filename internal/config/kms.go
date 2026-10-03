package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"google.golang.org/grpc/status"
)

// KMS contains only bootstrap settings. Authentication material is read from
// environment variables or certificate files, never from the recording store.
type KMS struct {
	Endpoint      string `json:"endpoint"`
	Namespace     string `json:"namespace,omitempty"`
	ConfigKey     string `json:"config_key,omitempty"`
	ConfigVersion uint64 `json:"config_version,omitempty"`
	TokenEnv      string `json:"token_env,omitempty"`
	CAFile        string `json:"ca_file,omitempty"`
	CertFile      string `json:"cert_file,omitempty"`
	KeyFile       string `json:"key_file,omitempty"`
	Insecure      bool   `json:"insecure,omitempty"`
	TimeoutMS     int64  `json:"timeout_ms,omitempty"`
}

// SecretRef can pin a credential version. Binding keys stay in the process
// environment and are passed directly to the SDK's redacting credential type.
type SecretRef struct {
	Key           string `json:"key"`
	Version       uint64 `json:"version,omitempty"`
	Label         string `json:"label,omitempty"`
	BindingKeyEnv string `json:"binding_key_env,omitempty"`
}

type kmsReader interface {
	GetParameter(context.Context, string, ...kmsclient.GetOption) (string, error)
	GetSecret(context.Context, string, ...kmsclient.GetOption) (kmsclient.Secret, error)
	Close() error
}
type kmsFactory func(KMS) (kmsReader, error)

func newKMSClient(k KMS) (kmsReader, error) {
	if k.Endpoint == "" {
		return nil, errors.New("KMS endpoint is required")
	}
	if k.TimeoutMS < 0 || k.TimeoutMS > 60000 {
		return nil, errors.New("KMS timeout_ms must be between 0 and 60000")
	}
	if (k.CertFile == "") != (k.KeyFile == "") {
		return nil, errors.New("KMS cert_file and key_file must be supplied together")
	}
	if k.Insecure && (k.CAFile != "" || k.CertFile != "" || k.KeyFile != "") {
		return nil, errors.New("KMS insecure mode cannot be combined with TLS files")
	}
	cfg := kmsclient.Config{Endpoint: k.Endpoint, Namespace: k.Namespace, Insecure: k.Insecure, Timeout: time.Duration(k.TimeoutMS) * time.Millisecond, ClientName: "llm-replay-proxy"}
	if k.TokenEnv != "" {
		var found bool
		cfg.Token, found = os.LookupEnv(k.TokenEnv)
		if !found || cfg.Token == "" {
			return nil, errors.New("KMS token environment variable is missing or empty")
		}
	}
	if !k.Insecure {
		var err error
		cfg.TLS, err = kmsclient.MTLSConfig(k.CertFile, k.KeyFile, k.CAFile)
		if err != nil {
			return nil, errors.New("could not load KMS TLS certificates")
		}
	}
	client, err := kmsclient.NewClient(cfg)
	if err != nil {
		return nil, errors.New("could not initialize KMS client; check endpoint, namespace and transport settings")
	}
	return client, nil
}

func kmsReadError(kind string, err error) error {
	// Do not relay arbitrary server error text into startup logs: a misconfigured
	// remote service can include credentials or parameter contents in that text.
	switch {
	case errors.Is(err, kmsclient.ErrNotFound):
		return fmt.Errorf("KMS %s not found", kind)
	case errors.Is(err, kmsclient.ErrPermissionDenied):
		return fmt.Errorf("KMS %s access denied", kind)
	case errors.Is(err, kmsclient.ErrUnauthenticated):
		return fmt.Errorf("KMS authentication failed while reading %s", kind)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("KMS %s read canceled", kind)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("KMS %s read timed out", kind)
	default:
		return fmt.Errorf("KMS %s read failed (%s)", kind, status.Code(err))
	}
}

func applyKMSBootstrapEnvironment(c *Config) error {
	// Setting an endpoint is the explicit opt-in. Other KMS variables can coexist
	// with local-only configuration without accidentally enabling a dependency.
	if endpoint := os.Getenv("REPLAY_KMS_ENDPOINT"); endpoint != "" {
		if c.KMS == nil {
			c.KMS = &KMS{}
		}
		c.KMS.Endpoint = endpoint
	}
	if c.KMS == nil {
		return nil
	}
	for _, p := range []struct {
		name string
		dest *string
	}{
		{"REPLAY_KMS_NAMESPACE", &c.KMS.Namespace}, {"REPLAY_KMS_CONFIG_KEY", &c.KMS.ConfigKey},
		{"REPLAY_KMS_CA_FILE", &c.KMS.CAFile}, {"REPLAY_KMS_CERT_FILE", &c.KMS.CertFile}, {"REPLAY_KMS_KEY_FILE", &c.KMS.KeyFile},
	} {
		if value, ok := os.LookupEnv(p.name); ok {
			*p.dest = value
		}
	}
	if _, ok := os.LookupEnv("REPLAY_KMS_TOKEN"); ok {
		c.KMS.TokenEnv = "REPLAY_KMS_TOKEN"
	}
	if v, ok := os.LookupEnv("REPLAY_KMS_INSECURE"); ok {
		switch v {
		case "true":
			c.KMS.Insecure = true
		case "false":
			c.KMS.Insecure = false
		default:
			return errors.New("REPLAY_KMS_INSECURE must be true or false")
		}
	}
	return nil
}

func mergeJSON(base, overlay []byte) ([]byte, error) {
	var a, b map[string]json.RawMessage
	if err := json.Unmarshal(base, &a); err != nil || a == nil {
		return nil, errors.New("configuration must be a JSON object")
	}
	if err := json.Unmarshal(overlay, &b); err != nil || b == nil {
		return nil, errors.New("configuration must be a JSON object")
	}
	for key, value := range b {
		var old, new map[string]json.RawMessage
		if json.Unmarshal(a[key], &old) == nil && old != nil && json.Unmarshal(value, &new) == nil && new != nil {
			merged, err := mergeJSON(a[key], value)
			if err != nil {
				return nil, err
			}
			a[key] = merged
		} else {
			a[key] = value
		}
	}
	return json.Marshal(a)
}
