# Inference Replay Proxy

A standalone Go recorder and replay proxy for Chat Completions, Responses, and Anthropic Messages. SQLite holds named collections and immutable response revisions. A statically exported Next.js control panel, built with shadcn/ui and Tailwind CSS v4, is embedded in the executable. Node is needed to build the frontend, never to run the proxy.

## Build and run

Requires Go 1.27.1+ and Node 20.9+ with npm for frontend builds.
The race-test target also needs a C compiler; the executable itself builds with CGO disabled.

```sh
make build
cp config.example.json config.local.json
export OPENAI_API_KEY='your-key'      # upstream provider keys, used only by the proxy
export ANTHROPIC_API_KEY='your-key'
./bin/replay-proxy -config config.local.json
```

Every `/v1/*` and `/api/*` request needs an access token (see [Authentication](#authentication)). Issue one and open the console login link it prints:

```sh
./bin/replay-proxy token issue -config config.local.json
```

Fresh databases start in **Replay** mode and a `Default` collection. Configure the fixed upstream URL for each protocol you use; keep the same URLs and non-secret settings for offline replay. Credentials are optional for replay. `-listen` and `-db` override configuration. `REPLAY_LISTEN`, `REPLAY_DATABASE`, and `REPLAY_{CHAT,RESPONSES,ANTHROPIC}_{URL,API_KEY}` environment overrides are also supported.

Optional [KMS integration](docs/kms.md) loads startup configuration and upstream secrets through the existing KMS Go SDK, with TLS/mTLS and version pins. Start with `config.kms.example.json`. Local files and environment variables work without KMS.

The server exposes:

| Protocol | Endpoint |
| --- | --- |
| Chat Completions | `/v1/chat/completions` |
| Responses | `/v1/responses` |
| Anthropic Messages | `/v1/messages` |

Upstream URLs are complete endpoints, not base URLs. Caller credentials are not forwarded. Configure credentials through environment variables or a private local JSON file. `api_key_env` names an environment variable; `api_key` supports a literal local-file secret. No credentials are written into recordings or exports. Requests and model outputs themselves may contain sensitive application data.

## Authentication

The proxy requires a signed, stateless JWT on every inference (`/v1/*`) and control API (`/api/*`) request, so only holders of a token can spend the configured upstream keys or read recordings and exports. `/healthz` and the static console pages stay public; the console shows sign-in instructions until it has a token.

**Issuing tokens.** Tokens are issued only by the binary on the server; there is no HTTP endpoint that creates them. Use the same `-config`/`-db` as the server so the same signing key is used:

```sh
replay-proxy token issue [-config path] [-db path] [-listen addr] [-ttl 2160h] [-subject name]
```

The token alone is printed to stdout (so `TOKEN=$(replay-proxy token issue)` works); the subject, expiry and console login link go to stderr. `-ttl` defaults to 90 days (`2160h`); `-ttl 0` issues a token without expiry that stays valid until the key is rotated. `-subject` (default `local`) labels the client. Issuing does not need the server to be running.

**Clients.** Use the token as the SDK API key and the proxy as the base URL. The proxy accepts `Authorization: Bearer <token>` (OpenAI SDKs) and `x-api-key: <token>` (Anthropic SDK):

```sh
export OPENAI_API_KEY="$(replay-proxy token issue -subject my-app)"   # client process, not the proxy
export OPENAI_BASE_URL=http://127.0.0.1:8080/v1
export ANTHROPIC_API_KEY="$OPENAI_API_KEY"
export ANTHROPIC_BASE_URL=http://127.0.0.1:8080
```

These client variables are separate from the proxy's own upstream `OPENAI_API_KEY`/`ANTHROPIC_API_KEY`: the proxy never forwards the caller's token, and upstream requests carry only the configured upstream key. Tokens never enter recordings, history, matching input or exports. Requests without a valid token get HTTP 401 `unauthorized` with `WWW-Authenticate: Bearer`.

**Console.** Open the printed link, `http://<listen>/auth?token=<jwt>` (`/?token=<jwt>` also works). The server validates the token, stores it in an `HttpOnly; SameSite=Strict; Path=/` cookie named `replay_proxy_token` (also `Secure` when the request arrived at the proxy over TLS; forwarded-protocol headers from a reverse proxy are not trusted) whose lifetime matches the token's expiry (400 days for a non-expiring token), and redirects to `/` so the token leaves the address bar. The cookie authorizes only `/api/*`; inference routes require a header token.

**Signing key.** Tokens are HS256 signed with an HMAC key of at least 32 bytes, chosen in this order:

1. `REPLAY_JWT_KEY` environment variable (base64).
2. `auth.signing_key_file` (a file containing a base64 key), or `auth.signing_key_secret` (a [KMS](docs/kms.md) secret reference, same fields as `api_key_secret`). Configure only one.
3. Otherwise a random key generated on first start (or first `token issue`) and saved with mode 0600 as `<database>.jwt-key`, e.g. `replay.sqlite.jwt-key`.

Generate a key with `openssl rand -base64 32`. The key is never logged or exposed through the API. Verification accepts only HS256 (`none` and other algorithms are rejected), checks the signature in constant time, requires `iss` = `llm-replay-proxy` and `iat`, and checks `exp`/`nbf`/`iat` with 30 seconds of leeway. Tokens without `exp` are accepted because only `-ttl 0` issues them.

**Revocation and rotation.** Tokens are stateless, so individual tokens cannot be revoked. Rotate the signing key (replace or delete the key file, or change `REPLAY_JWT_KEY`/the KMS secret) and restart the proxy: every outstanding token, including console cookies, becomes invalid. Then issue new tokens.

**Disabling.** `"auth": {"disabled": true}` turns authentication off, and is accepted only when `listen` is a loopback address (`127.0.0.1`, `::1`, `localhost`); startup fails otherwise. The browser smoke-test configuration uses it.

```json
{
  "auth": {"signing_key_file": "/etc/replay-proxy/jwt.key"}
}
```

## Recording and replay

Select a collection and mode in the control panel:

- **Record** forwards each request and publishes a new revision after successful completion.
- **Replay** serves exact matches only. A miss immediately returns HTTP 404 with `recording_not_found`.
- **Auto** replays hits and forwards misses, recording successful responses.

Live bytes are forwarded as they arrive. Streams retain ordered raw SSE frames and relative timestamps. Non-streaming bodies retain their original bytes. Only complete, successful protocol responses become active recordings; an error or interrupted refresh preserves the previous revision. The proxy does not retry upstream requests.

Timing controls set the first event delay and a multiplier for later recorded delays. Set both to zero for instant playback. Each replay event is flushed, and disconnecting cancels playback.

Traffic refreshes automatically every five seconds without replacing settings
or editor drafts. The dashboard can analyze any collection without activating
it, with 24-hour, 7-day, and 30-day UTC graphs plus an accessible data table. It
reports full-history totals, interval request volume, cache lookup hit/miss
rate, completion errors, and separate upstream/replay duration and first-SSE-
event percentiles. Record-mode requests are cache bypasses. Existing history
from an older database remains untimed rather than being treated as zero.

Matching uses a versioned SHA-256 input containing the API route, fixed non-secret upstream configuration, and canonical JSON request. Object property order is ignored; array order, string contents, and exact numeric precision are preserved. Streaming and non-streaming requests are distinct. There is no fuzzy matching.

Collection-specific JSON Pointer exclusions remove fields only from matching, never from the forwarded request. For example, `/metadata/run_id` ignores a volatile run identifier. Matching rules are immutable: create a new collection to change exclusions. Use the request comparison panel to inspect changing fields before adding exclusions.

Exact recordings of stateful requests replay normally. Live requests that reference provider state produced by recorded responses are blocked with an actionable error. Warming the exact follow-up or reconstructing a self-contained conversation is required; v1 does not reconstruct conversations automatically.

## Inspecting and editing

Traffic analytics show request volume over time, cache hits and misses, recording completions, errors, and separate upstream/replay timing summaries. Collection and date filters aggregate the full persisted history, beyond the latest 200 rows shown in the history browser. Auto-mode forwarded misses count as cache misses; Record mode bypasses the cache. Legacy requests without timing measurements are excluded from latency statistics. The dashboard refreshes every five seconds without replacing editor drafts.

The control panel provides collection management, request history and hit/miss outcomes, request comparison, response inspection, revision history, and restore. Its protocol-aware response browser reconstructs assistant text, tool calls and results, reasoning, refusal, usage, annotations, and event timing, with expandable fields and the exact raw body or SSE frames always available. Ordinary textual assistant answers support plain-text editing. Tool calls, reasoning, and multimodal content require the advanced response/SSE editor, with the reason displayed in the panel.

Edits are validated before becoming new revisions. Stream structure, completion, and text/final-output agreement are checked. Restoring activates a saved immutable revision without deleting history. Usage remains historical metadata from the original provider response; editing never estimates token counts.

Export downloads a consistent SQLite snapshot of one collection with its matching rules and revisions. Import adds collections to the current database. Upstream credentials and runtime configuration are separate. Keep the same non-secret upstream settings when moving recordings between installations.

## Go Common and demo rehearsal

See [Go Common integration and rehearsal](docs/go-common.md) for the actual provider configuration and a runnable multi-step record/offline-replay rehearsal. The integration module uses the adjacent `../go-common` checkout; it is separate from the standalone executable's dependencies.

The proxy records inference only. Application tools and their side effects still execute during replay. Reset demo application data, use deterministic tool results and stable identifiers, and warm every inference request, including tool follow-ups and branches, before disconnecting the upstream network.

## Development

```sh
make test                 # Go tests with race detection
make integration          # actual Go Common consumers and offline rehearsal
cd web && npm run build   # type-check and static export
```

Browser smoke-test instructions are in [tests/browser/README.md](tests/browser/README.md).

Build the frontend before building Go from a checkout that does not include `web/out`. The generated export is included in this repository so `go build ./cmd/replay-proxy` can produce a standalone binary without Node. `make build` regenerates it from source and the lockfile.

The application assumes trusted operators: every token holder has full access (there are no roles or per-user permissions). It binds to localhost by default and requires a token for inference and the control API. The control API rejects cross-origin browser writes, and there is no CORS support. The server speaks plain HTTP; put it behind a TLS-terminating reverse proxy before exposing it beyond the local machine so tokens are not sent in clear text.

Frontend build configuration follows the [Next.js static export documentation](https://nextjs.org/docs/app/guides/static-exports) and [shadcn Tailwind v4 guidance](https://ui.shadcn.com/docs/tailwind-v4).
