# Implementation guide

The executable wires three inference routes, `/api/` control routes, `/healthz`, and the embedded static Next.js export. The frontend calls same-origin JSON endpoints; no Next.js server is used at runtime.

- `internal/config` loads server-only upstream settings and credentials from local files, environment variables, and optional KMS startup reads.
- `internal/match` builds versioned exact SHA-256 keys and applies JSON Pointer exclusions.
- `internal/proxy` shares forwarding and playback for all protocols. It handles request cancellation, live flushing, timestamp capture, allowlisted response headers, and publication only after protocol validation. A caller that disconnects (or a server shutdown) is recorded as `interrupted` and receives no error body. Upstream bodies are forwarded and recorded without content coding: a configured `Accept-Encoding` is dropped so the transport negotiates and decodes gzip, and gzip/deflate responses are decoded otherwise.
- `internal/protocol` owns completion checks and text reconstruction/editing.
- `internal/store` owns SQLite, short publication transactions, collection isolation, revision history, and snapshot transfer.
- `internal/admin` implements inspection, comparison, editing, settings, and import/export.
- `web` contains Next.js source, shadcn components, Tailwind v4 styles, the generated `out` assets, and the Go embed handler.
- `integration` is an independent Go module using the actual Go Common provider implementation.

A recording identifies a collection and matching key. Its active revision points to immutable response content. A completed refresh inserts a revision and changes the active pointer in one transaction. Failed refreshes never update that pointer. Revision provenance retains the request and matching input used for the revision. Network forwarding and event delays happen outside database transactions.

The replay clock is injectable. The first frame uses the configured first-event delay; subsequent frames use the difference between their captured offsets, multiplied by the configured factor. Zero delays produce immediate playback, with cancellation checked between events.

Edits and restores compare the caller's base revision atomically before activation, rejecting stale updates. Snapshot imports validate the complete source before acquiring the destination write transaction.

The management API returns errors as `{ "error": { "code": "...", "message": "..." } }`. Inference misses use the same shape. An upstream error body/status is forwarded as received. Once a live stream has started, later failures cannot replace its already-sent HTTP status; its bytes still reach the caller, and its history entry explains why it was not recorded.

No application tool execution is intercepted. Replay only replaces the inference response. Keep demo state resettable and follow the [rehearsal checklist](go-common.md).
