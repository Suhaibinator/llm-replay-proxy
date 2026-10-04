# Inference Replay Proxy

A standalone Go recorder and replay proxy for Chat Completions, Responses, and Anthropic Messages. SQLite holds named collections and immutable response revisions. A statically exported Next.js control panel, built with shadcn/ui and Tailwind CSS v4, is embedded in the executable. Node is needed to build the frontend, never to run the proxy.

## Build and run

Requires Go 1.27.1+ and Node 20.9+ with npm for frontend builds.
The race-test target also needs a C compiler; the executable itself builds with CGO disabled.

```sh
make build
cp config.example.json config.local.json
export OPENAI_API_KEY='your-key'
export ANTHROPIC_API_KEY='your-key'
./bin/replay-proxy -config config.local.json
```

Open http://127.0.0.1:8080. Fresh databases start in **Replay** mode and a `Default` collection. Configure the fixed upstream URL for each protocol you use; keep the same URLs and non-secret settings for offline replay. Credentials are optional for replay. `-listen` and `-db` override configuration. `REPLAY_LISTEN`, `REPLAY_DATABASE`, and `REPLAY_{CHAT,RESPONSES,ANTHROPIC}_{URL,API_KEY}` environment overrides are also supported.

Optional [KMS integration](docs/kms.md) loads startup configuration and upstream secrets through the existing KMS Go SDK, with TLS/mTLS and version pins. Start with `config.kms.example.json`. Local files and environment variables work without KMS.

The server exposes:

| Protocol | Endpoint |
| --- | --- |
| Chat Completions | `/v1/chat/completions` |
| Responses | `/v1/responses` |
| Anthropic Messages | `/v1/messages` |

Upstream URLs are complete endpoints, not base URLs. Caller credentials are not forwarded. Configure credentials through environment variables or a private local JSON file. `api_key_env` names an environment variable; `api_key` supports a literal local-file secret. No credentials are written into recordings or exports. Requests and model outputs themselves may contain sensitive application data.

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

Matching uses a versioned SHA-256 input containing the API route, fixed non-secret upstream configuration, and canonical JSON request. Object property order is ignored; array order, string contents, and exact numeric precision are preserved, and an empty array is distinct from `null`. Streaming and non-streaming requests are distinct; `"stream": null` is non-streaming. There is no fuzzy matching.

Collection-specific JSON Pointer exclusions remove fields only from matching, never from the forwarded request. For example, `/metadata/run_id` ignores a volatile run identifier. Matching rules are immutable: create a new collection to change exclusions. Use the request comparison panel to inspect changing fields before adding exclusions.

Exact recordings of stateful requests replay normally. Live requests that reference provider state produced by recorded responses are blocked with an actionable error. Warming the exact follow-up or reconstructing a self-contained conversation is required; v1 does not reconstruct conversations automatically.

## Inspecting and editing

Traffic analytics show request volume over time, cache hits and misses, recording completions, errors, and separate upstream/replay timing summaries. Collection and date filters aggregate the full persisted history, beyond the latest 200 rows shown in the history browser. Auto-mode forwarded misses count as cache misses; Record mode bypasses the cache. Legacy requests without timing measurements are excluded from latency statistics. The dashboard refreshes every five seconds without replacing editor drafts.

The control panel provides collection management, request history and hit/miss outcomes, request comparison, response inspection, revision history, and restore. Its protocol-aware response browser reconstructs assistant text, tool calls and results, reasoning, refusal, usage, annotations, and event timing, with expandable fields and the exact raw body or SSE frames always available. Ordinary textual assistant answers support plain-text editing. Tool calls, reasoning, and multimodal content require the advanced response/SSE editor, with the reason displayed in the panel.

Edits are validated before becoming new revisions. Stream structure, completion, and text/final-output agreement are checked. Restoring activates a saved immutable revision without deleting history. Usage remains historical metadata from the original provider response; editing never estimates token counts.

Export downloads a consistent SQLite snapshot of one collection with its matching rules and revisions; request history is not included. Import adds collections to the current database. Upstream credentials and runtime configuration are separate. Keep the same non-secret upstream settings when moving recordings between installations.



The proxy records inference only. Application tools and their side effects still execute during replay. Reset demo application data, use deterministic tool results and stable identifiers, and warm every inference request, including tool follow-ups and branches, before disconnecting the upstream network.

## Development

```sh
make test                 # Go tests with race detection
cd web && npm run build   # type-check and static export
```

Browser smoke-test instructions are in [tests/browser/README.md](tests/browser/README.md).

Build the frontend before building Go from a checkout that does not include `web/out`. The generated export is included in this repository so `go build ./cmd/replay-proxy` can produce a standalone binary without Node. `make build` regenerates it from source and the lockfile.

The application assumes one trusted local operator. It binds to localhost by default; it has no multi-user authentication. The control API rejects cross-origin browser writes. Avoid exposing the listener to untrusted networks.

Frontend build configuration follows the [Next.js static export documentation](https://nextjs.org/docs/app/guides/static-exports) and [shadcn Tailwind v4 guidance](https://ui.shadcn.com/docs/tailwind-v4).
