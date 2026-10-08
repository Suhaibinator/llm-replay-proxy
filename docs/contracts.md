# Control API

The embedded frontend uses these same-origin endpoints. All mutations require a
same-origin browser request; local command-line clients without an Origin header
are supported. Errors have the shape `{ "error": { "code": "...", "message": "..." } }`.

## Authentication

Every `/api/*` and `/v1/*` request requires a JWT issued by
`replay-proxy token issue` (see the README's Authentication section). It is
accepted as `Authorization: Bearer <jwt>` or `x-api-key: <jwt>` on all of these
routes, and as the `replay_proxy_token` cookie on `/api/*` only. A missing,
invalid, or expired token returns HTTP 401 with `WWW-Authenticate: Bearer` and
`{ "error": { "code": "unauthorized", "message": "..." } }`; the frontend shows
sign-in instructions when it receives this. `/healthz` and the static console
pages are public. `auth.disabled` (loopback listeners only) removes the
requirement.

`GET /auth?token=<jwt>` (also `GET /?token=<jwt>`) is the console login link.
It validates the token, sets the `HttpOnly; SameSite=Strict; Path=/` cookie with
a `Max-Age` matching the token's `exp`, and answers `303 See Other` to `/`. An
invalid token returns a plain-text 401 and no cookie. There is no API that
issues, refreshes, lists, or revokes tokens.

## Endpoints

| Endpoint | Method | Request / result |
| --- | --- | --- |
| `/api/settings` | GET, PUT | Full settings object: `mode`, `active_collection_id`, `first_event_delay_ms`, `delay_multiplier`, `history_limit` |
| `/api/collections` | GET, POST | POST `{name, exclusions}`; matching rules remain immutable |
| `/api/collections/N` | DELETE | 204; deletes the collection, its recordings and its history. 409 `collection_active` for the active collection |
| `/api/recordings?collection_id=N` | GET | Recordings, newest first, each with a request summary (`request`), the active revision's response summary (`response`), `updated_at`, active revision `source`, `revisions`, `hits` and `last_hit_at` |
| `/api/recordings/N` | GET, DELETE | GET: active recording/revision, all revisions, editable text and unavailability reason, exact `request_text` and `matching_input_text`. DELETE: 204; removes the recording and its revisions |
| `/api/recordings/N/edit` | POST | `{text, base_revision_id}` or `{revision, base_revision_id}` |
| `/api/recordings/N/restore` | POST | `{revision_id, base_revision_id}` |
| `/api/history?collection_id=N` | GET, DELETE | GET: newest calls with a request summary (`request`, null without a body); optional `limit` up to 1000 and `after_id` for rows newer than an id. DELETE: 204; clears the collection's history |
| `/api/history/N` | GET | One call with its exact `request_text` |
| `/api/history/N/nearest` | GET | `{candidates}`: up to 5 recordings the call most likely meant (same thread first, then shared request bytes); see [dashboard API](dashboard-api.md) |
| `/api/analytics?collection_id=N&from=RFC3339&to=RFC3339` | GET | Complete history aggregates, UTC series, hit rate (the lookup-based rate `/api/insights` also reports), and source-specific timing percentiles |
| `/api/insights?collection_id=N&from=RFC3339&to=RFC3339[&model=M]` | GET | Dashboard aggregates: outcomes, tokens and costs by time, model and route, latency histograms, top recordings and threads; see [dashboard API](dashboard-api.md) |
| `/api/threads?collection_id=N[&limit=50][&from=…&to=…]` | GET | Conversation threads, most recently active first |
| `/api/threads/{thread}?collection_id=N` | GET | One thread with its turns, oldest first; `{thread}` is 16 lowercase hex digits |
| `/api/collections/N/export` | GET | SQLite snapshot attachment: the collection, recordings, and revisions; request history is never included |
| `/api/import` | POST | Raw SQLite snapshot bytes; 422 `invalid_import` for an invalid snapshot |
| `/api/compare` | POST | `{recording_id, request}` or `{recording_id, history_id}` |

Every GET endpoint also accepts HEAD. Path ids must be plain positive decimal
digits (no sign or whitespace). A path that does not name an endpoint or
resource, including a malformed id, is 404 `not_found` for every method; an
unsupported method on a valid path is 405 `method_not_allowed` with an `Allow`
header. A body over the endpoint's size limit is 413 `request_too_large`.
Unexpected server failures are 500 `internal_error` with a generic message; the
details are logged by the server, not returned.

Snapshot import rejects, with 422 and nothing imported, any snapshot that is
not a readable replay-proxy SQLite database in the current storage format or
that violates store invariants, including a request whose stored chunks do not
reassemble to its recorded hash, blank (after trimming) collection names and `created_at` values that
are not RFC 3339 timestamps. Imported names are trimmed, and null exclusions or
revision headers are stored as empty values. Request history in a
snapshot is ignored.

Request bytes appear only as exact text in `request_text` (recording detail and
history item); lists and revisions never carry them, and an edit that supplies
`request` or `matching_input` is rejected. A request summary has `model`,
`items` (messages or Responses input items), `preview` (the start of the last
user-written text), `opening` (the start of the first user-written text),
`tool_calls`, `images`, `bytes`, and `thread`, a fingerprint of the
conversation's opening shared by its later turns. A response summary
(`ResponseSummary` in [dashboard API](dashboard-api.md)) describes a stored
revision. Summaries are computed when a read first needs them and saved, not
when a request is recorded.

`history_limit` (default 10000, 0 keeps everything) is the number of history
rows kept per collection; older rows are deleted at startup and every 10
minutes. Deletes reclaim request storage no remaining row references.

Analytics uses a half-open `[from,to)` interval, defaults to the last 24 hours,
and accepts ranges up to 366 days. `lifetime_total` is independent of that
interval. Series buckets are hourly through 48 hours and daily for longer
ranges. A timing percentile is null with zero samples when no measurement is
available; upstream and replay timings are never combined.

Edits and restores require the currently active `base_revision_id`. A stale
revision, including one activated concurrently while the request is processed,
produces HTTP 409 `revision_conflict`; reload before retrying. Advanced
edits change response bodies/events; response status, headers, original request,
and matching provenance remain immutable.

For exact numeric inspection, use the string fields rather than parsing and
serializing JSON through a floating-point representation. Comparison differences
include exact `request_display`/`recorded_display` strings and existence flags to
distinguish missing values from null, and `excluded`, true when the path is (or
is inside) one of the collection's match exclusions, so the difference did not
affect matching. Paths and exclusions are compared as written; an excluded
array element shifts later indices in the matching input, which `excluded`
does not model. History-based comparison also reports a route mismatch.

`Event.data` contains a complete raw SSE frame, including event/data lines and its
blank delimiter. `Event.offset_ms` is the relative capture time. Edits must retain
valid event order, identities, completion, and agreement with final output.
Frames follow the SSE field rules: a line without a colon is a field with an
empty value (a bare `data` line), and a UTF-8 byte order mark is stripped from
the first frame only.

The plain-text editor only rewrites existing text; it never inserts events or
adjusts metadata. A recording therefore reports an unavailability reason, and
needs an advanced edit, when it contains tool calls, reasoning, refusals, audio,
multiple choices/parts/blocks, non-empty annotations, citations, or logprobs
(including any `*.annotation.added` Responses event), or a Responses stream
with no `response.output_text.delta` event to rewrite.

## Matching keys

Empty JSON arrays are matched as `[]`, distinct from `null`; an array emptied by
exclusions also matches as `[]`. The key is the SHA-256 of the matching input
`{"version":2,"api":…,"body":…}`, where `api` is `chat_completions`,
`responses` or `messages` and `body` is the canonical request after collection
exclusions. The upstream provider, URL and headers are not part of it, so a
recording replays whichever provider a request selects. The input is derived
whenever it is displayed and is not stored.

Shared Go data types are in `internal/model`. Storage exposes atomic
`PublishIfActive` and `RestoreIfActive` operations for optimistic editing, while
live successful recordings use `Publish`. Network reads, replay delays, and
snapshot validation happen outside publication transactions.

KMS bootstrap settings, identity tokens, binding keys, upstream credentials, and
the JWT signing key are not part of this control API. See [KMS configuration](kms.md).
