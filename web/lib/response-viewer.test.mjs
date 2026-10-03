import test from "node:test";
import assert from "node:assert/strict";
import {
  parseExactJSON,
  exactJSON,
  responseView,
  JSONNumber,
} from "./response-viewer.ts";
const rev = (body) => ({ status: 200, headers: {}, body, events: [] });
const event = (value) => ({
  offset_ms: 1,
  data: `data: ${JSON.stringify(value)}\n\n`,
});
test("JSON inspection preserves numeric lexemes and prototype-like keys", () => {
  const parsed = parseExactJSON(
    '{"large":900719925474099312345,"decimal":1.2300e-12,"__proto__":{"safe":true}}',
  );
  assert.ok(parsed.large instanceof JSONNumber);
  assert.equal(parsed.large.source, "900719925474099312345");
  assert.match(exactJSON(parsed), /1.2300e-12/);
  assert.equal(parsed.__proto__.safe, true);
  for (const bad of [
    '{"x":01}',
    "[1,]",
    '{"x":NaN}',
    "{} false",
    '{"x":"bad\nstring"}',
  ])
    assert.throws(() => parseExactJSON(bad));
});
test("Chat JSON groups answer, tools, refusal, usage and reasoning", () => {
  const view = responseView(
    "/v1/chat/completions",
    false,
    rev(
      '{"id":"c","usage":{"total_tokens":9007199254740993},"choices":[{"message":{"role":"assistant","content":"Answer","reasoning_content":"Recorded reasoning","refusal":"Refusal","tool_calls":[{"id":"call1","type":"function","function":{"name":"lookup","arguments":"{\\"id\\":9007199254740993}"}}]},"finish_reason":"tool_calls"}]}',
    ),
  );
  assert.equal(view.blocks.find((b) => b.kind === "text").text, "Answer");
  const tool = view.blocks.find((b) => b.kind === "tool");
  assert.equal(tool.title, "lookup");
  assert.equal(tool.id, "call1");
  assert.equal(tool.arguments.id.source, "9007199254740993");
  assert.ok(view.blocks.some((b) => b.kind === "refusal"));
  assert.ok(view.blocks.some((b) => b.kind === "reasoning"));
  assert.equal(view.usage.total_tokens.source, "9007199254740993");
});
test("Chat streams reconstruct interleaved parallel tools and text", () => {
  const revision = {
    ...rev(""),
    events: [
      event({
        id: "c",
        choices: [
          {
            index: 0,
            delta: {
              content: "Hi ",
              tool_calls: [
                {
                  index: 1,
                  id: "b",
                  type: "function",
                  function: { name: "second", arguments: '{"b":' },
                },
                {
                  index: 0,
                  id: "a",
                  type: "function",
                  function: { name: "first", arguments: '{"a":' },
                },
              ],
            },
          },
        ],
      }),
      event({
        id: "c",
        choices: [
          {
            index: 0,
            delta: {
              content: "there",
              tool_calls: [
                { index: 0, function: { arguments: "1}" } },
                { index: 1, function: { arguments: "2}" } },
              ],
            },
            finish_reason: "tool_calls",
          },
        ],
      }),
      { data: "data: [DONE]\n\n", offset_ms: 2 },
    ],
  };
  const view = responseView("/v1/chat/completions", true, revision);
  assert.equal(view.blocks.find((b) => b.kind === "text").text, "Hi there");
  const tools = view.blocks.filter((b) => b.kind === "tool");
  assert.equal(tools.length, 2);
  assert.equal(tools.find((b) => b.id === "a").arguments.a.source, "1");
  assert.equal(tools.find((b) => b.id === "b").arguments.b.source, "2");
});
test("Responses uses completed output once, including tools and result IDs", () => {
  const response = {
    id: "r",
    status: "completed",
    output: [
      {
        type: "reasoning",
        summary: [{ type: "summary_text", text: "Summary" }],
      },
      {
        type: "function_call",
        call_id: "call1",
        name: "weather",
        arguments: '{"city":"Paris"}',
      },
      { type: "function_call_output", call_id: "call1", output: "sunny" },
      {
        type: "message",
        id: "m",
        content: [
          { type: "output_text", text: "Final answer", annotations: [] },
        ],
      },
    ],
  };
  for (const streaming of [false, true]) {
    const revision = streaming
      ? {
          ...rev(""),
          events: [
            event({
              type: "response.output_text.delta",
              delta: "Final answer",
            }),
            event({ type: "response.completed", response }),
          ],
        }
      : rev(JSON.stringify(response));
    const view = responseView("/v1/responses", streaming, revision);
    assert.equal(view.blocks.filter((b) => b.kind === "text").length, 1);
    assert.equal(
      view.blocks.find((b) => b.kind === "text").text,
      "Final answer",
    );
    assert.equal(view.blocks.find((b) => b.kind === "result").id, "call1");
    assert.equal(
      view.blocks.find((b) => b.kind === "reasoning").text,
      "Summary",
    );
  }
});
test("Anthropic streams reconstruct content, JSON input and historical usage", () => {
  const revision = {
    ...rev(""),
    events: [
      event({
        type: "message_start",
        message: { id: "m", content: [], usage: { input_tokens: 4 } },
      }),
      event({
        type: "content_block_start",
        index: 0,
        content_block: { type: "text", text: "Initial " },
      }),
      event({
        type: "content_block_delta",
        index: 0,
        delta: { type: "text_delta", text: "answer" },
      }),
      event({
        type: "content_block_start",
        index: 1,
        content_block: { type: "tool_use", id: "t", name: "lookup", input: {} },
      }),
      event({
        type: "content_block_delta",
        index: 1,
        delta: {
          type: "input_json_delta",
          partial_json: '{"value":9007199254740993}',
        },
      }),
      event({
        type: "message_delta",
        delta: { stop_reason: "tool_use" },
        usage: { output_tokens: 2 },
      }),
      event({ type: "message_stop" }),
    ],
  };
  const view = responseView("/v1/messages", true, revision);
  assert.equal(
    view.blocks.find((b) => b.kind === "text").text,
    "Initial answer",
  );
  assert.equal(
    view.blocks.find((b) => b.kind === "tool").arguments.value.source,
    "9007199254740993",
  );
  assert.equal(view.usage.input_tokens.source, "4");
  assert.equal(view.usage.output_tokens.source, "2");
});
test("unsupported and multimodal blocks remain inspectable, malformed raw is retained", () => {
  const view = responseView(
    "/v1/messages",
    false,
    rev(
      JSON.stringify({
        content: [
          {
            type: "image",
            source: { type: "url", url: "https://example.invalid/image.png" },
          },
          { type: "tool_result", tool_use_id: "t", content: "result" },
        ],
      }),
    ),
  );
  assert.equal(view.blocks[0].kind, "other");
  assert.equal(
    view.blocks[0].raw.source.url,
    "https://example.invalid/image.png",
  );
  assert.equal(view.blocks[1].id, "t");
  assert.equal(
    responseView("/v1/responses", false, rev("broken")).warnings.length,
    1,
  );
});

test("Anthropic JSON retains nested results, citations and redacted reasoning", () => {
  const view = responseView(
    "/v1/messages",
    false,
    rev(
      JSON.stringify({
        id: "msg_body",
        role: "assistant",
        model: "fixture",
        stop_reason: "end_turn",
        content: [
          {
            type: "text",
            text: "Cited answer",
            citations: [{ type: "char_location", cited_text: "source" }],
          },
          { type: "redacted_thinking", data: "opaque" },
          {
            type: "tool_result",
            tool_use_id: "call_nested",
            content: [{ type: "text", text: "Nested result" }],
            is_error: true,
          },
        ],
        usage: { input_tokens: 12, output_tokens: 6 },
      }),
    ),
  );
  assert.equal(view.metadata.id, "msg_body");
  assert.equal(view.blocks[0].raw.citations[0].cited_text, "source");
  assert.equal(view.blocks[1].title, "Redacted reasoning");
  assert.equal(view.blocks[2].id, "call_nested");
  assert.equal(view.blocks[2].arguments[0].text, "Nested result");
  assert.equal(view.blocks[2].raw.is_error, true);
});

test("Responses message fields and annotations remain related to the answer", () => {
  const view = responseView(
    "/v1/responses",
    false,
    rev(
      JSON.stringify({
        output: [
          {
            type: "message",
            id: "msg_related",
            role: "assistant",
            status: "completed",
            content: [
              {
                type: "output_text",
                text: "Answer",
                annotations: [
                  { type: "url_citation", url: "https://example.invalid/" },
                ],
              },
            ],
          },
        ],
      }),
    ),
  );
  assert.equal(view.blocks[0].id, "msg_related");
  assert.equal(view.blocks[0].raw.status, "completed");
  assert.equal(view.blocks[0].raw.role, "assistant");
  assert.equal(
    view.blocks[0].raw.content_block.annotations[0].type,
    "url_citation",
  );
});

test("CRLF multiline SSE and comments preserve raw frames", () => {
  const frame =
    ': comment\r\nevent: response.completed\r\ndata: {"type":"response.completed",\r\ndata: "response":{"output":[{"type":"message","content":[{"type":"output_text","text":"Exact"}]}]}}\r\n\r\n';
  const view = responseView("/v1/responses", true, {
    ...rev(""),
    events: [
      { data: ": keepalive\n\n", offset_ms: 0 },
      { data: frame, offset_ms: 25 },
    ],
  });
  assert.equal(view.frames[0].name, "keepalive");
  assert.equal(view.frames[1].data, frame);
  assert.equal(view.frames[1].offset_ms, 25);
  assert.equal(view.blocks[0].text, "Exact");
});
