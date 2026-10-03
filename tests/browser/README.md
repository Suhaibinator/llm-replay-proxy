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

The protocol-aware response browser also has a read-only mocked API suite that
covers all three response formats without changing the test database:

```sh
REPLAY_BROWSER_BASE_URL=http://127.0.0.1:18080 \
  npx --prefix tests/browser playwright test \
  --config=tests/browser/response-viewer.config.js
```

Build `web/out` before starting the proxy. Test screenshots and traces are written below `tests/browser/test-results/`.
