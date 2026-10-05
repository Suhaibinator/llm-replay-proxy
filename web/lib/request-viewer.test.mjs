import test from "node:test";
import assert from "node:assert/strict";
import { base64Bytes, formatBytes, requestView } from "./request-viewer.ts";
import { JSONNumber } from "./response-viewer.ts";

const png = "data:image/png;base64,iVBORw0KGgo=";

test("Responses requests read as instructions, turns, calls and image results", () => {
  const raw = JSON.stringify({
    model: "openai/gpt-6-luna",
    store: false,
    stream: true,
    instructions: "Be concise.",
    input: [
      {
        role: "system",
        content: [{ type: "input_text", text: "You are the Post Assistant" }],
      },
      {
        role: "user",
        content: [{ type: "input_text", text: "Help me understand this post" }],
      },
      { type: "reasoning", id: "rs_1", summary: [], encrypted_content: "gAAA" },
      {
        type: "function_call",
        call_id: "call_1",
        name: "get_post_content",
        arguments: "{}",
      },
      {
        type: "function_call_output",
        call_id: "call_1",
        output: [
          { type: "input_text", text: '{"text":"A post"}' },
          { type: "input_image", image_url: png, detail: "high" },
        ],
      },
      {
        role: "assistant",
        content: [{ type: "output_text", text: "It shows a man." }],
      },
    ],
    tools: [
      {
        type: "function",
        name: "get_post_content",
        description: "Fetch the post",
        parameters: { type: "object" },
      },
      { type: "web_search" },
    ],
  });
  const view = requestView("/v1/responses", raw);
  assert.equal(view.protocol, "Responses");
  assert.equal(view.model, "openai/gpt-6-luna");
  assert.deepEqual(
    view.turns.map((turn) => turn.role),
    ["instructions", "system", "user", "assistant", "tool", "assistant"],
  );
  const assistant = view.turns[3];
  assert.deepEqual(
    assistant.parts.map((part) => part.kind),
    ["reasoning", "tool_call"],
  );
  assert.equal(assistant.parts[0].encrypted, true);
  const result = view.turns[4].parts[0];
  assert.equal(result.kind, "tool_result");
  assert.equal(result.name, "get_post_content");
  assert.equal(result.parts[1].kind, "image");
  assert.equal(result.parts[1].src, png);
  assert.equal(result.parts[1].mediaType, "image/png");
  assert.equal(result.parts[1].detail, "high");
  assert.deepEqual(view.counts, { toolCalls: 1, images: 1, attachments: 0 });
  assert.deepEqual(
    view.tools.map((tool) => tool.name),
    ["get_post_content", "web_search"],
  );
  assert.equal(view.tools[0].description, "Fetch the post");
  assert.deepEqual(Object.keys(view.parameters), ["model", "store", "stream"]);
  assert.equal(view.bytes, raw.length);
});

test("Responses string input is one user turn", () => {
  const view = requestView("/v1/responses", '{"model":"m","input":"hello"}');
  assert.equal(view.turns.length, 1);
  assert.equal(view.turns[0].role, "user");
  assert.equal(view.turns[0].parts[0].text, "hello");
});

test("Chat requests keep roles, tool calls, results and exact argument numbers", () => {
  const view = requestView(
    "/v1/chat/completions",
    '{"model":"m","temperature":0.2,"messages":[' +
      '{"role":"system","content":"Rules"},' +
      '{"role":"user","content":[{"type":"text","text":"What is this?"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]},' +
      '{"role":"assistant","content":null,"tool_calls":[{"id":"call_9","type":"function","function":{"name":"lookup","arguments":"{\\"id\\":900719925474099312345}"}}]},' +
      '{"role":"tool","tool_call_id":"call_9","content":"found"}' +
      '],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}',
  );
  assert.deepEqual(
    view.turns.map((turn) => turn.role),
    ["system", "user", "assistant", "tool"],
  );
  const remote = view.turns[1].parts[1];
  assert.equal(remote.kind, "image");
  assert.equal(remote.src, undefined, "remote images are never loaded");
  assert.equal(remote.url, "https://example.com/a.png");
  const call = view.turns[2].parts[0];
  assert.equal(call.kind, "tool_call");
  assert.ok(call.arguments.id instanceof JSONNumber);
  assert.equal(call.arguments.id.source, "900719925474099312345");
  assert.equal(view.turns[3].parts[0].name, "lookup");
  assert.equal(view.turns[3].parts[0].parts[0].text, "found");
  assert.equal(view.parameters.temperature.source, "0.2");
});

test("Messages requests show system, base64 images and tool_result turns", () => {
  const view = requestView(
    "/v1/messages",
    JSON.stringify({
      model: "claude",
      max_tokens: 100,
      system: [{ type: "text", text: "Be kind" }],
      messages: [
        {
          role: "user",
          content: [
            {
              type: "image",
              source: {
                type: "base64",
                media_type: "image/jpeg",
                data: "/9j/4AAQ",
              },
            },
            {
              type: "document",
              title: "Spec",
              source: {
                type: "base64",
                media_type: "application/pdf",
                data: "JVBERi0=",
              },
            },
          ],
        },
        {
          role: "assistant",
          content: [
            { type: "thinking", thinking: "Check the spec", signature: "sig" },
            {
              type: "tool_use",
              id: "toolu_1",
              name: "search",
              input: { q: "x" },
            },
          ],
        },
        {
          role: "user",
          content: [
            {
              type: "tool_result",
              tool_use_id: "toolu_1",
              is_error: true,
              content: "timeout",
            },
          ],
        },
      ],
      tools: [
        {
          name: "search",
          description: "Search",
          input_schema: { type: "object" },
        },
      ],
    }),
  );
  assert.deepEqual(
    view.turns.map((turn) => turn.role),
    ["system", "user", "assistant", "tool"],
  );
  assert.equal(view.turns[1].parts[0].src, "data:image/jpeg;base64,/9j/4AAQ");
  assert.equal(view.turns[1].parts[1].kind, "attachment");
  assert.equal(view.turns[1].parts[1].title, "Spec");
  assert.equal(view.turns[2].parts[0].text, "Check the spec");
  const result = view.turns[3].parts[0];
  assert.equal(result.isError, true);
  assert.equal(result.name, "search");
  assert.equal(view.tools[0].schema.type, "object");
  assert.deepEqual(view.counts, { toolCalls: 1, images: 1, attachments: 1 });
});

test("SVG data URLs are described rather than rendered", () => {
  const view = requestView(
    "/v1/responses",
    JSON.stringify({
      input: [
        {
          role: "user",
          content: [
            {
              type: "input_image",
              image_url: "data:image/svg+xml;base64,PHN2Zz4=",
            },
          ],
        },
      ],
    }),
  );
  const part = view.turns[0].parts[0];
  assert.equal(part.src, undefined);
  assert.equal(part.mediaType, "image/svg+xml");
});

test("Invalid requests fall back to Raw with a warning", () => {
  const view = requestView("/v1/responses", "{not json");
  assert.equal(view.turns.length, 0);
  assert.match(view.warnings[0], /Raw/);
});

test("Sizes are reported from decoded base64", () => {
  assert.equal(base64Bytes("iVBORw0KGgo="), 8);
  assert.equal(formatBytes(512), "512 B");
  assert.equal(formatBytes(1536), "1.5 KB");
  assert.equal(formatBytes(2.3 * 1024 * 1024), "2.3 MB");
});

test("Tool output that is JSON text opens as fields; other text stays text", () => {
  const view = requestView(
    "/v1/chat/completions",
    JSON.stringify({
      messages: [
        { role: "tool", tool_call_id: "a", content: '{"city":"Paris"}' },
        { role: "tool", tool_call_id: "b", content: "{not json" },
      ],
    }),
  );
  const [json, text] = view.turns.map((turn) => turn.parts[0].parts[0]);
  assert.equal(json.kind, "data");
  assert.equal(json.open, true);
  assert.equal(json.value.city, "Paris");
  assert.equal(text.kind, "text");
});
