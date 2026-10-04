# Validation

The control panel validation includes desktop and mobile workflows for traffic
analytics, recording/history search and distinct filtered empty states,
background refresh preserving settings drafts, and the protocol-aware response
browser. Response browser fixtures cover all three protocols, streamed and
non-streamed text, tool calls/results, reasoning/refusals, usage, annotations,
multiline CRLF SSE frames, exact large numbers, raw output, and event timelines.

The implementation is checked at three levels:

| Suite | What it exercises |
| --- | --- |
| Go unit and HTTP integration tests | Exact matching, exclusion paths, collection isolation, concurrent publication, database reopen, restore, invalid snapshot rollback, all three protocol responses and streams, tool calls, error/incomplete/cancel behavior, and editing validation |
| Injected replay-clock tests | First-event delay, fractional subsequent delays, event order, flushing, and prompt cancellation |
| Go Common integration module | Actual Go Common consumers of streamed and non-streamed edited responses; two-inference tool workflows recorded and replayed after shutting down the fixture upstream |
| KMS SDK integration | Startup parameter/secret reads, immutable version pins, binding credentials, environment/CLI precedence, sanitized failures, and absence of credentials in exported SQLite |
| Playwright browser smoke | Embedded static frontend, collection creation, mode/timing settings, recording/history/inspection, text edit, restore, comparison, and SQLite export/import |

Run the backend checks with:

```sh
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./...
```

The race detector needs a C compiler. The shipping binary uses pure-Go SQLite and can be built with `CGO_ENABLED=0`.

Run the actual Go Common integration from its separate module (requires the adjacent Go Common checkout and Go 1.27.1):

```sh
cd integration
go test -count=1 ./...
```

These are deterministic local fixtures serving provider wire formats, consumed through Go Common's real configurable HTTP providers. Offline tests close the upstream listener before replay and assert no additional upstream requests. No remote provider credentials are required to run the suites.

Frontend and browser checks:

```sh
cd web
npm ci
npm run typecheck
npm run build
```

See [browser setup](../tests/browser/README.md) for the browser smoke workflow and [the rehearsal checklist](go-common.md) to warm and verify your own demo application, including every inference branch and tool follow-up.
