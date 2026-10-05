# Dashboard insight API (contract)

These endpoints feed the console's analytics, conversation and miss views.
TypeScript shapes are in `web/lib/api.ts` (`Insights`, `ThreadSummary`,
`ThreadDetail`, `NearestCandidate`, `RecordingRow`, `ResponseSummary`); the Go
handlers return exactly those fields (Go types in `internal/model/insights.go`).
All follow the existing API rules in `contracts.md` (auth, error shape, HEAD on
GET, 404 before 405).

## Derived summaries

Both are computed the first time a read needs them and saved, never on the
proxy's write path (the same rule as request summaries). Each is stored with
the version of the code that made it; a build that summarizes differently
recomputes older ones.

- **Request summary** (`RequestSummary`) gains `opening`: the start of the
  first user-written text, bounded like `preview`. `summaryVersion` is bumped
  so stored summaries are recomputed.
- **Response summary** (`ResponseSummary`), one per revision, from its JSON
  body or SSE frames, for all three protocols:
  - `model`: the response's model field; empty if absent.
  - `outcome`: Responses `status` (`completed`, `incomplete`, `failed`, …);
    Chat `finish_reason` of the first choice; Messages `stop_reason`.
  - `usage`: subsets nest for every protocol: `cached_input` is part of
    `input` and `reasoning` is part of `output`. A count the provider did not
    report is null.
    - Responses: `input_tokens`, `input_tokens_details.cached_tokens`,
      `output_tokens`, `output_tokens_details.reasoning_tokens`,
      `total_tokens`.
    - Chat: `prompt_tokens`, `prompt_tokens_details.cached_tokens`,
      `completion_tokens`, `completion_tokens_details.reasoning_tokens`,
      `total_tokens`. Streams take the chunk that carries `usage` (OpenAI's
      `stream_options.include_usage`, OpenRouter's final chunk).
    - Messages: `input` = `input_tokens` + `cache_creation_input_tokens` +
      `cache_read_input_tokens` (the whole prompt, as for OpenAI);
      `cached_input` = `cache_read_input_tokens`; `output` = `output_tokens`;
      `reasoning` and `total` are null (not reported). Streams merge
      `message_start` usage with the last `message_delta` usage, field by
      field.
    - Responses streams read the terminal event's response
      (`response.completed`, `.incomplete` or `.failed`).
  - `cost`: `usage.cost` when the provider reports it (OpenRouter does), in USD.
  - `output_chars`, `reasoning_chars` (Unicode characters), `tool_calls`:
    from the reconstructed output. Reasoning text is Chat `reasoning`,
    `reasoning_content` or `reasoning_details`; Responses reasoning items'
    `summary` and `content` text (encrypted reasoning counts 0); Messages
    `thinking` blocks. Tool calls are Chat `tool_calls` (and legacy
    `function_call`), Responses output items whose type ends in `_call`, and
    Messages `tool_use`, `server_tool_use` and `mcp_tool_use` blocks.

## History rows link their revision

`history.revision_id` (nullable) records the revision served: the active
revision on a hit, the published revision on `recorded`; null for every other
outcome (including an interrupted replay). Token and cost attribution uses that
revision, never whatever is active later. Deleting a recording clears the link
(its revisions are gone), so those rows no longer carry tokens. Storage format
is 4: revisions also store their response summary, and request bodies store
their summary version and `thread` in indexed columns.

## `GET /api/insights?collection_id=N&from=RFC3339&to=RFC3339[&model=M]`

Returns `Insights`. Same range rules as `/api/analytics` (default last 24 h,
half-open, at most 366 days; hourly buckets through 48 h, daily beyond; buckets
start at the UTC hour or day containing `from`, so the first and last can be
partial). `model` filters every section to one model (exact match on the
attributed model). 404 for an unknown collection.

- Outcomes: `hits` = outcome `hit`; `misses` = `miss`; `recorded`;
  `interrupted`; `errors` = `error` + `incomplete`; `requests` = all rows.
  `hit_rate` = `lookup_hits` / `lookups`: calls whose recording lookup found a
  recording, over every call that looked one up (`lookup_outcome` `hit` or
  `miss`), whatever happened after the lookup. An interrupted replay still
  hit; an Auto-mode miss whose upstream call failed still missed; Record mode
  (`bypass`) is no lookup. Null when there were no lookups. This is the same
  rate `/api/analytics` reports. Each `series` bucket and each `models` and
  `routes` row has its own `hit_rate` with the same definition over its rows,
  so the outcome counts alone do not reproduce it.
- `upstream_tokens` / `upstream_cost`: summed over `recorded` rows' revisions.
  `replayed_tokens` / `saved_cost`: summed over `hit` rows' revisions (what
  replay avoided paying for). Token fields sum only reported counts (0 when
  none). Costs are null when no row in the set reported one.
- Model attribution: response summary `model`, else request summary `model`,
  else `"unknown"`. `models` and `routes` are sorted by `requests`, descending.
- `latency`: `upstream` from rows with source `upstream`, `replay` from source
  `replay`. `histogram` (durations) and `first_event_histogram` (first-event
  times) count samples **per bucket**, not cumulatively: a bucket counts
  samples with previous `le` < value ≤ `le`. Bounds (ms): 50, 100, 250, 500,
  1000, 2500, 5000, 10000, 30000, 60000, then `le: null` for everything above.
- `totals.threads`: distinct non-empty request `thread`s in range.
- `top_recordings`: up to 10 by hits in range (ties: newest recording first);
  `preview` and `model` come from the latest hit. `top_threads`: up to 10 by
  requests in range (ties: most recently active first), counted over the
  range only.

Measured with `BenchmarkInsights10k` (`go test ./internal/devseed -run '^$'
-bench Insights10k -v`) on 10,000 seeded rows (Apple M4 Max): ~27 ms once
summaries exist; ~270 ms for the first call, which computes and saves every
summary.

## `GET /api/threads?collection_id=N[&limit=50][&from=…&to=…]`

`ThreadSummary[]`, most recently active first. A thread is the set of history
rows whose request summary shares `thread`; rows without a request, or whose
request has no user-written text (no `thread`), are excluded. `limit` 1–500.
`from`/`to` are optional RFC 3339 bounds (either may be omitted; no range cap)
and restrict the rows counted. `opening` comes from the earliest row; `latest`,
`route`, `model` (attributed as above) and `last_outcome` from the latest.

## `GET /api/threads/{thread}?collection_id=N`

`ThreadDetail`; `turns` oldest first, each with the response summary of the
revision it was served (`null` for misses, errors and interrupted rows). The
summary part covers every row of the thread (no range). Each turn also carries
the history row's `route` and `source`. `{thread}` must be 16
lowercase hex digits (else 404 `not_found`); 404 if no row in the collection
has that thread.

## `GET /api/history/{id}/nearest`

`{"candidates": NearestCandidate[]}`, at most 5, best first, among recordings
in the row's collection (excluding an exact key match). Recordings whose active
request has the same `thread` come first (`same_thread`), then others ranked by
`similarity`: the share of the history request's chunk bytes that also appear
in the recording's active request body (`shared_content`). Candidates below
0.2 similarity are omitted unless same-thread. Each candidate carries `thread`
(its request's thread fingerprint, `""` if none) and `created_at` (when its
active revision was created). 404 for an unknown history id; a row without a
request returns no candidates.

Similarity uses the stored content-defined chunks (~8 KiB average), so large
requests are compared without reading them. A request of at most 16 KiB is a
single stored chunk or two, which would make similarity all-or-nothing; it is
also compared with fine (~64-byte) content-defined chunks against recordings
of the same route and model (up to 300 per call), keeping the higher share.
The UI then calls `POST /api/compare {recording_id, history_id}` for the field
diff; each difference's `excluded` says whether its path is under one of the
collection's match exclusions (see `contracts.md`).

## `GET /api/recordings?collection_id=N`

Each row is a `RecordingRow`: the existing fields plus `response`, the active
revision's `ResponseSummary` (null if it cannot be derived).

## Synthetic traffic for console development

`cmd/replay-seed` fills a database with realistic traffic: eight models
(reasoning ones included: gpt-5, o4-mini and DeepSeek R1 via OpenRouter with
`usage.cost`, Claude with thinking), all three protocols as JSON and SSE,
conversations that grow over up to ten turns with tool calls, and hits,
misses (including near-miss variants of recorded requests for the nearest
view), recordings, edits, interruptions and errors over the last 30 days, with
usage and latencies. Recordings and edits carry the time of the call that
recorded or edited them (`Store.PublishAt`), so their dates spread over the
range like the history does. Every recording passes the same validation as
live traffic. Each run adds a new collection ("Demo traffic", "Demo traffic 2", …)
and makes it active.

```sh
go run ./cmd/replay-seed -db dev.sqlite            # ~4000 history rows
go run ./cmd/replay-seed -db dev.sqlite -rows 10000 -days 30 -seed 7
```

Then serve it (`auth.disabled` is allowed on loopback listeners):

```sh
cat > dev-config.json <<'EOF'
{"listen": "127.0.0.1:8080", "database": "dev.sqlite", "auth": {"disabled": true}, "upstreams": {}}
EOF
go run ./cmd/replay-proxy -config dev-config.json
```

With authentication on, issue a console token instead:
`go run ./cmd/replay-proxy token issue -db dev.sqlite` and open the printed
`/auth?token=` link. The proxy serves the embedded console (`web/out`) and the
API on the same origin. The default history limit (10,000 rows per collection) prunes larger seeds at
startup; raise `history_limit` in settings first if you need more.
