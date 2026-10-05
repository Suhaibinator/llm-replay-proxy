# Dashboard insight API (contract)

These endpoints feed the console's analytics, conversation and miss views.
TypeScript shapes are in `web/lib/api.ts` (`Insights`, `ThreadSummary`,
`ThreadDetail`, `NearestCandidate`, `RecordingRow`, `ResponseSummary`); the Go
handlers return exactly those fields. All follow the existing API rules in
`contracts.md` (auth, error shape, HEAD on GET, 404 before 405).

## Derived summaries

Both are computed the first time a read needs them and saved, never on the
proxy's write path (the same rule as request summaries).

- **Request summary** (`RequestSummary`) gains `opening`: the start of the
  first user-written text, bounded like `preview`. `summaryVersion` is bumped
  so stored summaries are recomputed.
- **Response summary** (`ResponseSummary`), one per revision, from its JSON
  body or SSE frames, for all three protocols:
  - `model`: the response's model field; empty if absent.
  - `outcome`: Responses `status` (`completed`, `incomplete`, `failed`, …);
    Chat `finish_reason` of the first choice; Messages `stop_reason`.
  - `usage`: Responses `input_tokens`, `input_tokens_details.cached_tokens`,
    `output_tokens`, `output_tokens_details.reasoning_tokens`, `total_tokens`.
    Chat `prompt_tokens`, `prompt_tokens_details.cached_tokens`,
    `completion_tokens`, `completion_tokens_details.reasoning_tokens`,
    `total_tokens`. Messages `input_tokens` (+ `cache_read_input_tokens` as
    `cached_input`), `output_tokens`; for streams, merge `message_start` and
    the last `message_delta` usage. A count the provider did not report is null.
  - `cost`: `usage.cost` when the provider reports it (OpenRouter does), in USD.
  - `output_chars`, `reasoning_chars`, `tool_calls`: from the reconstructed
    output (reuse `protocol` reconstruction where possible).

## History rows link their revision

`history.revision_id` (nullable) records the revision served: the active
revision on a hit, the published revision on `recorded`. Token and cost
attribution uses that revision, never whatever is active later. Storage format
becomes 4.

## `GET /api/insights?collection_id=N&from=RFC3339&to=RFC3339[&model=M]`

Returns `Insights`. Same range rules as `/api/analytics` (default last 24 h,
half-open, at most 366 days; hourly buckets through 48 h, daily beyond).
`model` filters every section to one model.

- Outcomes: `hits` = outcome `hit`; `misses` = `miss`; `recorded`;
  `interrupted`; `errors` = `error` + `incomplete`; `requests` = all rows.
  `hit_rate` = hits / (hits + misses + recorded rows whose lookup missed), null
  when the denominator is 0 (matches the existing analytics definition).
- `upstream_tokens` / `upstream_cost`: summed over `recorded` rows' revisions.
  `replayed_tokens` / `saved_cost`: summed over `hit` rows' revisions (what
  replay avoided paying for). Costs are null when no row in the set reported
  one.
- Model attribution: response summary `model`, else request summary `model`,
  else `"unknown"`.
- `latency`: `upstream` from rows with source `upstream`, `replay` from source
  `replay`. Histogram bucket bounds (ms): 50, 100, 250, 500, 1000, 2500, 5000,
  10000, 30000, 60000, then `null`.
- `top_recordings`: up to 10 by hits in range. `top_threads`: up to 10 by
  requests in range.

Target: under 300 ms for 10,000 history rows once summaries exist.

## `GET /api/threads?collection_id=N[&limit=50][&from=…&to=…]`

`ThreadSummary[]`, most recently active first. A thread is the set of history
rows whose request summary shares `thread`; rows without a request are
excluded. `limit` 1–500.

## `GET /api/threads/{thread}?collection_id=N`

`ThreadDetail`; `turns` oldest first, each with the response summary of the
revision it was served (`null` for misses and errors). 404 if no row in the
collection has that thread.

## `GET /api/history/{id}/nearest`

`{"candidates": NearestCandidate[]}`, at most 5, best first, among recordings
in the row's collection (excluding an exact key match). Recordings whose active
request has the same `thread` come first (`same_thread`), then others ranked by
`similarity`: the share of the history request's chunk bytes that also appear
in the recording's active request body (`shared_content`). Candidates below
0.2 similarity are omitted unless same-thread. The UI then calls
`POST /api/compare {recording_id, history_id}` for the field diff.

## `GET /api/recordings?collection_id=N`

Each row is a `RecordingRow`: the existing fields plus `response`, the active
revision's `ResponseSummary` (null if it cannot be derived).
