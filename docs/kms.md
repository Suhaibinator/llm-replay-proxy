# KMS configuration and secrets

The proxy optionally uses the Go SDK from the adjacent [KMS project](../../kms/README.md). The build pins `github.com/Suhaibinator/kms` v0.4.5; there is no local module replacement or sibling checkout requirement for building the executable. Its SDK requires Go 1.27.1.

Configuration is resolved once before the server starts. No KMS request is made during inference or replay. Restart the process to adopt configuration changes or rotate credentials. This keeps the non-secret upstream identity fixed while requests are being recorded or replayed.

## Bootstrap

Use `config.kms.example.json` as a starting point:

```json
{
  "kms": {
    "endpoint": "kms.example.com:8443",
    "namespace": "demo/llm-replay-proxy",
    "config_key": "proxy/config",
    "token_env": "REPLAY_KMS_TOKEN",
    "ca_file": "/path/to/server-ca.pem",
    "timeout_ms": 5000
  }
}
```

```sh
export REPLAY_KMS_TOKEN='your-KMS-identity-token'
./bin/replay-proxy -config config.kms.example.json
```

TLS is enabled by default and verifies the server using the system trust store or `ca_file`. For mutual TLS, set both `cert_file` and `key_file`; `token_env` can be omitted for certificate identities. `insecure: true` explicitly enables plaintext transport for local development and cannot be combined with certificate files. The default per-read timeout is five seconds; `timeout_ms` accepts 0 (default) through 60000.

Bootstrap environment overrides are `REPLAY_KMS_ENDPOINT`, `REPLAY_KMS_NAMESPACE`, `REPLAY_KMS_CONFIG_KEY`, `REPLAY_KMS_CA_FILE`, `REPLAY_KMS_CERT_FILE`, `REPLAY_KMS_KEY_FILE`, and `REPLAY_KMS_INSECURE` (`true` or `false`). `REPLAY_KMS_TOKEN` selects that environment variable as the identity-token source when set. KMS is enabled only by a local `kms` object or `REPLAY_KMS_ENDPOINT`; unrelated KMS environment variables do not turn it on.

Namespace may be omitted for an identity bound to a KMS namespace. The SDK discovers it through `WhoAmI`. Resource keys are relative to the namespace, or use KMS absolute `/env/app/key` paths.

## Parameter and secret layout

Store a JSON parameter at `proxy/config`, using the same fields as local configuration. Put credentials in separate KMS **secrets**, referenced by the parameter:

```json
{
  "listen": "127.0.0.1:8080",
  "database": "replay.sqlite",
  "upstreams": {
    "/v1/chat/completions": {
      "url": "https://api.openai.com/v1/chat/completions",
      "api_key_secret": {"key": "providers/openai", "version": 3}
    },
    "/v1/responses": {
      "url": "https://api.openai.com/v1/responses",
      "api_key_secret": {"key": "providers/openai", "version": 3}
    },
    "/v1/messages": {
      "url": "https://api.anthropic.com/v1/messages",
      "headers": {"anthropic-version": "2023-06-01"},
      "api_key_secret": {"key": "providers/anthropic"}
    }
  }
}
```

An `api_key_secret` supports `key`, optional immutable `version`, optional `label` (mutually exclusive with version), and optional `binding_key_env` for bound secrets. Binding keys and KMS authentication tokens are read from environment variables; plaintext values never enter matching inputs, SQLite, exports, or the control API.

Pin `kms.config_version` to select an immutable JSON parameter version. Pin secret versions inside that document when the configuration and credentials must refer to a repeatable generation. Omitting versions reads the current values at startup. This integration uses the existing SDK's read APIs, not the managed-release hot-reload layer.

For secrets-only integration, omit `config_key`, keep non-secret upstream settings in the local file, and use `api_key_secret` on those upstream entries. The proxy never provisions or mutates KMS resources.

## Precedence and failures

Non-secret configuration precedence is **built-in defaults → KMS JSON parameter → local JSON file → environment → CLI flags**. JSON objects are merged recursively so a local upstream identity or header override can retain the remote URL. Secret references (`api_key_secret`) are the exception: a local reference replaces the remote one as a whole, so `{"key": "other-key"}` reads the current version of `other-key` rather than inheriting a remote `version` or `label` pin. The KMS parameter cannot replace KMS bootstrap settings or contain inline `api_key` values.

Credential precedence is **`REPLAY_{CHAT,RESPONSES,ANTHROPIC}_API_KEY` → named `api_key_env` → local inline `api_key` → KMS secret reference**. An explicitly empty environment credential suppresses a KMS secret read, allowing credential-free replay when non-secret settings are available. A local inline key can be cleared with `"api_key": ""` to use a secret reference instead.

An explicitly configured KMS parameter/secret that cannot be resolved fails startup; unavailable, denied, missing, or malformed data does not silently fall back to defaults. Errors identify the operation and failure category without echoing remote error details or credential values. The last running process is unaffected by later KMS outages because its startup snapshot is held in memory.

For completely offline startup, use a local configuration with the same non-secret upstream URLs, identities, and headers and omit the `kms` object and secret references. The original exact matching keys remain valid. Do not export secrets into SQLite to make offline startup work.

Tests use the KMS project's real SDK with its in-process gRPC fake to verify parameter/secret reads, version pins, binding credentials, overrides, transport validation, and failures without requiring a live KMS installation.
