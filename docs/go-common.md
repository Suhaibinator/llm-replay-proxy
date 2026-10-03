# Go Common integration and rehearsal

Go Common needs no replay-specific adapter. Its existing `http` provider accepts
an exact endpoint for each supported wire API, and the replay proxy exposes all
three endpoints. Go Common also disables SDK retries for these runtimes, so a
proxy miss or upstream failure is returned to the application once.

The runnable consumer under [`integration/`](../integration/) imports the adjacent
Go Common checkout and uses `genai.ModelRuntimeFactory`. The separate module is
intentional: the proxy executable does not acquire Go Common's application
dependencies. Both modules require Go 1.27.1.

## Provider configuration

Configure one ordinary Go Common HTTP provider. Endpoint values are complete
URLs; Go Common does not append SDK paths. The v1 proxy has no client
authentication and binds to localhost by default, so configure Go Common's HTTP
provider for anonymous authentication. The proxy's upstream credentials remain
in the proxy process environment or local config and are never part of Go Common
config or the recording database.

```yaml
inference_providers:
  - id: replay-proxy
    name: Local inference replay proxy
    adapter: http
    default_api: 3 # Responses
    http:
      auth: none
    endpoints:
      chat_completions: http://127.0.0.1:8080/v1/chat/completions
      responses: http://127.0.0.1:8080/v1/responses
      anthropic_messages: http://127.0.0.1:8080/v1/messages
```

Do not add a credential binding for this provider. If the proxy is exposed
beyond the local machine, place it behind an authenticating reverse proxy; the
application itself does not validate incoming bearer or API keys.

Chat Completions, Responses, and Anthropic Messages work through the existing
Go Common adapters. Provider-native Google and Azure APIs are outside this
proxy's three-route contract. Streaming is selected by the normal Genkit
streaming option; no provider change is needed.

## Consumer check

The integration tests verify that a real Go Common consumer sends each protocol
to the correct proxy route, reads plain-text edits in both streaming and
non-streaming recordings (including empty and Unicode answers), and completes
streaming and non-streaming tool workflows while keeping tool execution in the
application. They also warm distinct tool-result branches and verify provider
errors are neither retried nor made replayable. The assembled tests close the
fake upstream before replay and assert its request count does not increase:

```sh
cd integration
go test ./...
```

Run one request against a live proxy with:

```sh
cd integration
go run ./cmd/rehearse \
  -proxy http://127.0.0.1:8080 \
  -api responses \
  -model demo-model \
  -workflow text \
  -prompt 'Reply with the word ready.'
```

Valid `-api` values are `chat`, `responses`, and `anthropic`. Use
`-workflow tools` for the two-inference weather demonstration. The demo returns
the deterministic local tool result `sunny, 21 C`; replaying inference does not
suppress that application code or any real tool side effects.

## Record and offline replay rehearsal

Use resettable demo data and a dedicated collection. Requests must be byte-level
semantically identical after canonicalization, including the selected protocol,
model, messages, tools, tool result, and streaming flag.

1. Start the proxy on localhost with its database path, fixed upstream URLs, and
   upstream credentials configured. The server defaults to replay mode.
2. In the control panel, create and activate a collection for the rehearsal.
   Add exclusions before recording; matching rules freeze after the first
   recording. Switch the mode to **Record**.
3. Run the text command above once for every API used by the demo. If production
   uses streaming, warm those streaming calls separately because streaming and
   non-streaming requests have different keys.
4. Run the tool workflow for every API used by the demo:

   ```sh
   REPLAY_PROXY_API_KEY=local-only go run ./cmd/rehearse \
     -proxy http://127.0.0.1:8080 -api responses -model demo-model \
     -workflow tools -prompt 'What is the weather in Paris? Use lookup_weather.'
   ```

   This warms both the tool-call inference and the follow-up inference containing
   its result. Repeat all branches, retries initiated by the application, and
   optional inference steps that the real demonstration may take.
5. Confirm in request history that every expected request is a successful record.
   Interrupted, failed, and incomplete streams are deliberately unavailable for
   replay.
6. Optionally edit an ordinary text-only recording in the plain-text editor.
   Run the same command in replay mode and confirm Go Common returns the edited
   answer. Tool, reasoning, mixed-content, and multimodal recordings require the
   advanced event editor.
7. Switch to **Replay**, stop or firewall the upstream test server, and rerun the
   exact commands. Every request should be a hit and the multi-step output should
   match. A miss fails immediately; use the compare view to locate changed JSON
   fields, warm that exact request in Record or Auto mode, then repeat the offline
   run.

Responses requests that refer to provider-maintained state can replay when their
exact recording exists. A live miss that references state seen only in recordings
fails with an actionable error because the upstream may no longer hold it. The v1
rehearsal should pass the full transcript/tool history, as the sample does, rather
than depending on conversation reconstruction.

Before a demo, export the warmed collection from the control panel and import it
into a fresh local database once. This checks that the consistent SQLite snapshot
contains the matching rules and all revisions while excluding credentials.
