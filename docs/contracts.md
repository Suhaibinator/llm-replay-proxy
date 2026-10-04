# Control API

The embedded frontend uses these same-origin endpoints. All mutations require a
same-origin browser request; local command-line clients without an Origin header
are supported. Errors have the shape `{ "error": { "code": "...", "message": "..." } }`.

| Endpoint | Method | Request / result |
| --- | --- | --- |
| `/api/settings` | GET, PUT | Full settings object: `mode`, `active_collection_id`, `first_event_delay_ms`, `delay_multiplier` |
| `/api/collections` | GET, POST | POST `{name, exclusions}`; matching rules remain immutable |
| `/api/recordings?collection_id=N` | GET | Recording summaries |
| `/api/recordings/N` | GET | Active recording/revision, all revisions, editable text and unavailability reason, exact `request_text` and `matching_input_text` |
| `/api/recordings/N/edit` | POST | `{text, base_revision_id}` or `{revision, base_revision_id}` |
| `/api/recordings/N/restore` | POST | `{revision_id, base_revision_id}` |
| `/api/history?collection_id=N` | GET | History including exact `request_text`; optional `limit` up to 1000 |
| `/api/analytics?collection_id=N&from=RFC3339&to=RFC3339` | GET | Complete history aggregates, UTC series, hit rate, and source-specific timing percentiles |
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
not a readable replay-proxy SQLite database or that violates store invariants,
including blank (after trimming) collection names and `created_at` values that
are not RFC 3339 timestamps. Imported names are trimmed, and null exclusions,
revision headers, or events are stored as empty values. Request history in a
snapshot is ignored.

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
distinguish missing values from null. History-based comparison also reports a
route mismatch.

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

Shared Go data types are in `internal/model`. Storage exposes atomic
`PublishIfActive` and `RestoreIfActive` operations for optimistic editing, while
live successful recordings use `Publish`. Network reads, replay delays, and
snapshot validation happen outside publication transactions.

KMS bootstrap settings, identity tokens, binding keys, and upstream credentials
are not part of this control API. See [KMS configuration](kms.md).
