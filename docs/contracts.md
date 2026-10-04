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
| `/api/settings` | GET, PUT | Full settings object: `mode`, `active_collection_id`, `first_event_delay_ms`, `delay_multiplier` |
| `/api/collections` | GET, POST | POST `{name, exclusions}`; matching rules remain immutable |
| `/api/recordings?collection_id=N` | GET | Recording summaries |
| `/api/recordings/N` | GET | Active recording/revision, all revisions, editable text and unavailability reason, exact `request_text` and `matching_input_text` |
| `/api/recordings/N/edit` | POST | `{text, base_revision_id}` or `{revision, base_revision_id}` |
| `/api/recordings/N/restore` | POST | `{revision_id, base_revision_id}` |
| `/api/history?collection_id=N` | GET | History including exact `request_text`; optional `limit` up to 1000 |
| `/api/analytics?collection_id=N&from=RFC3339&to=RFC3339` | GET | Complete history aggregates, UTC series, hit rate, and source-specific timing percentiles |
| `/api/collections/N/export` | GET | SQLite snapshot attachment |
| `/api/import` | POST | Raw SQLite snapshot bytes |
| `/api/compare` | POST | `{recording_id, request}` or `{recording_id, history_id}` |

Analytics uses a half-open `[from,to)` interval, defaults to the last 24 hours,
and accepts ranges up to 366 days. `lifetime_total` is independent of that
interval. Series buckets are hourly through 48 hours and daily for longer
ranges. A timing percentile is null with zero samples when no measurement is
available; upstream and replay timings are never combined.

Edits and restores require the currently active `base_revision_id`. A stale
revision produces HTTP 409 `revision_conflict`; reload before retrying. Advanced
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

Shared Go data types are in `internal/model`. Storage exposes atomic
`PublishIfActive` and `RestoreIfActive` operations for optimistic editing, while
live successful recordings use `Publish`. Network reads, replay delays, and
snapshot validation happen outside publication transactions.

KMS bootstrap settings, identity tokens, binding keys, upstream credentials, and
the JWT signing key are not part of this control API. See [KMS configuration](kms.md).
