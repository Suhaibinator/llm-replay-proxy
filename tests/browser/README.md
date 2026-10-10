# Browser smoke test

The test drives the embedded production UI against a fixture upstream. From the repository root:

```sh
npm --prefix tests/browser install
npx --prefix tests/browser playwright install chromium
rm -f /tmp/llm-replay-browser.sqlite*
node tests/browser/fixture-upstream.js &
go run ./cmd/replay-proxy -config tests/browser/config.json &
npm --prefix tests/browser test
```

`tests/browser/config.json` sets `"auth": {"disabled": true}`, which the proxy
accepts only because it listens on `127.0.0.1`, so the smoke test needs no
access token.

The protocol-aware response browser also has a read-only mocked API suite that
covers all three response formats without changing the test database:

```sh
REPLAY_BROWSER_BASE_URL=http://127.0.0.1:18080 \
  npx --prefix tests/browser playwright test \
  --config=tests/browser/response-viewer.config.js
```

The overview, conversations and traffic/recordings views have mocked suites
of their own, run the same way:

```sh
for suite in overview conversations traffic; do
  REPLAY_BROWSER_BASE_URL=http://127.0.0.1:18080 \
    npx --prefix tests/browser playwright test \
    --config=tests/browser/$suite.config.js
done
```

To look at the console with realistic data instead of fixtures, seed a
database (see docs/dashboard-api.md) and serve it with a loopback config that
sets `"auth": {"disabled": true}`.

Build `web/out` before starting the proxy. Test screenshots and traces are written below `tests/browser/test-results/`.

The mocked lazy-revision suite verifies on-demand loading, retry, active-response
reuse, and isolation from delayed requests after switching recordings:

```sh
REPLAY_BROWSER_BASE_URL=http://127.0.0.1:18080 \
  npx --prefix tests/browser playwright test \
  --config=tests/browser/lazy-revisions.config.js
```
