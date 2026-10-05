const { test, expect } = require("@playwright/test");

const large = "900719925474099312345";
const args = `{"id":${large},"nested":{"city":"Paris"}}`;
const answer = "Readable answer\n\n```js\nconst safe = true;\n```";
const chat = {
  id: "chat_viewer",
  model: "fixture",
  choices: [
    {
      index: 0,
      message: {
        role: "assistant",
        content: answer,
        tool_calls: [
          {
            id: "call_viewer",
            type: "function",
            function: { name: "lookup", arguments: args },
          },
        ],
      },
      finish_reason: "tool_calls",
    },
  ],
  usage: { prompt_tokens: 4, completion_tokens: 2, total_tokens: 6 },
};
const responses = {
  id: "resp_viewer",
  model: "fixture",
  status: "completed",
  output: [
    {
      type: "reasoning",
      id: "reason_viewer",
      summary: [{ type: "summary_text", text: "Recorded summary" }],
    },
    {
      type: "function_call",
      id: "fc_viewer",
      call_id: "call_viewer",
      name: "lookup",
      arguments: args,
      status: "completed",
    },
    {
      type: "message",
      id: "msg_viewer",
      role: "assistant",
      status: "completed",
      content: [{ type: "output_text", text: answer, annotations: [] }],
    },
  ],
  usage: { input_tokens: 4, output_tokens: 2, total_tokens: 6 },
};
// Insert this input through raw JSON below to avoid JavaScript rounding it.
const messages = {
  id: "msg_viewer",
  type: "message",
  role: "assistant",
  model: "fixture",
  content: [
    { type: "text", text: answer },
    { type: "tool_use", id: "call_viewer", name: "lookup", input: "__ARGS__" },
  ],
  stop_reason: "tool_use",
  usage: { input_tokens: 4, output_tokens: 2 },
};
const frame = (value, offset_ms) => ({
  data: `data: ${JSON.stringify(value)}\n\n`,
  offset_ms,
});
const png = "iVBORw0KGgo=";
const result = '{"city":"Paris"}';
// One conversation per protocol: instructions, a user turn with an image, a
// tool call and its JSON result.
function requestFixture(route, stream) {
  if (route.endsWith("completions"))
    return {
      model: "fixture",
      stream,
      messages: [
        { role: "system", content: "Rules" },
        {
          role: "user",
          content: [
            { type: "text", text: "Find Paris" },
            {
              type: "image_url",
              image_url: { url: `data:image/png;base64,${png}` },
            },
          ],
        },
        {
          role: "assistant",
          content: null,
          tool_calls: [
            {
              id: "call_viewer",
              type: "function",
              function: { name: "lookup", arguments: "{}" },
            },
          ],
        },
        { role: "tool", tool_call_id: "call_viewer", content: result },
      ],
      tools: [
        {
          type: "function",
          function: {
            name: "lookup",
            description: "Look up a city",
            parameters: { type: "object" },
          },
        },
      ],
    };
  if (route.endsWith("responses"))
    return {
      model: "fixture",
      stream,
      instructions: "Rules",
      input: [
        {
          role: "user",
          content: [
            { type: "input_text", text: "Find Paris" },
            { type: "input_image", image_url: `data:image/png;base64,${png}` },
          ],
        },
        {
          type: "function_call",
          call_id: "call_viewer",
          name: "lookup",
          arguments: "{}",
        },
        {
          type: "function_call_output",
          call_id: "call_viewer",
          output: result,
        },
      ],
      tools: [
        {
          type: "function",
          name: "lookup",
          description: "Look up a city",
          parameters: { type: "object" },
        },
      ],
    };
  return {
    model: "fixture",
    stream,
    max_tokens: 10,
    system: "Rules",
    messages: [
      {
        role: "user",
        content: [
          { type: "text", text: "Find Paris" },
          {
            type: "image",
            source: { type: "base64", media_type: "image/png", data: png },
          },
        ],
      },
      {
        role: "assistant",
        content: [
          { type: "tool_use", id: "call_viewer", name: "lookup", input: {} },
        ],
      },
      {
        role: "user",
        content: [
          { type: "tool_result", tool_use_id: "call_viewer", content: result },
        ],
      },
    ],
    tools: [
      {
        name: "lookup",
        description: "Look up a city",
        input_schema: { type: "object" },
      },
    ],
  };
}

function fixture(route, streaming) {
  let body = "",
    events = [];
  if (route.endsWith("completions")) {
    body = JSON.stringify(chat);
    events = [
      frame(
        {
          id: chat.id,
          choices: [{ index: 0, delta: chat.choices[0].message }],
        },
        10,
      ),
      frame(
        {
          id: chat.id,
          choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }],
          usage: chat.usage,
        },
        20,
      ),
      { data: "data: [DONE]\n\n", offset_ms: 25 },
    ];
  } else if (route.endsWith("responses")) {
    body = JSON.stringify(responses);
    events = [
      frame({ type: "response.output_text.delta", delta: answer }, 10),
      frame({ type: "response.completed", response: responses }, 25),
    ];
  } else {
    body = JSON.stringify(messages).replace('"__ARGS__"', args);
    events = [
      frame(
        { type: "message_start", message: { ...messages, content: [] } },
        0,
      ),
      frame(
        {
          type: "content_block_start",
          index: 0,
          content_block: { type: "text", text: "" },
        },
        5,
      ),
      frame(
        {
          type: "content_block_delta",
          index: 0,
          delta: { type: "text_delta", text: answer },
        },
        10,
      ),
      frame(
        {
          type: "content_block_start",
          index: 1,
          content_block: {
            type: "tool_use",
            id: "call_viewer",
            name: "lookup",
            input: {},
          },
        },
        15,
      ),
      frame(
        {
          type: "content_block_delta",
          index: 1,
          delta: { type: "input_json_delta", partial_json: args },
        },
        20,
      ),
      frame({ type: "message_stop" }, 25),
    ];
  }
  return {
    recording: {
      id: 1,
      collection_id: 1,
      key: "viewer-key",
      route,
      streaming,
      upstream_identity: "fixture",
      active_revision_id: 1,
      created_at: "2026-09-17T12:00:00Z",
    },
    revision: {
      id: 1,
      recording_id: 1,
      status: 200,
      headers: {
        "content-type": streaming ? "text/event-stream" : "application/json",
      },
      body: streaming ? "" : body,
      events: streaming ? events : [],
      source: "recorded",
      created_at: "2026-09-17T12:00:00Z",
    },
    request_text: JSON.stringify(requestFixture(route, streaming)),
    matching_input_text: "{}",
    text_unavailable_reason: "Tool calls require the advanced editor.",
  };
}

// The recordings list carries a request summary, never the request itself.
function listed(entry) {
  return {
    ...entry.recording,
    request: {
      model: "fixture-model",
      items: 4,
      preview: "Find Paris",
      tool_calls: 1,
      images: 1,
      bytes: entry.request_text.length,
      thread: "0011223344556677",
    },
    updated_at: entry.recording.created_at,
    source: "recorded",
    revisions: 1,
    hits: 0,
    last_hit_at: "",
  };
}

for (const route of ["/v1/chat/completions", "/v1/responses", "/v1/messages"]) {
  for (const streaming of [false, true]) {
    test(`${route} ${streaming ? "SSE" : "JSON"} readable, nested tools, raw and collapse controls`, async ({
      page,
    }, testInfo) => {
      const entry = fixture(route, streaming),
        errors = [];
      if (route.endsWith("messages"))
        await page.setViewportSize({ width: 390, height: 844 });
      page.on("pageerror", (error) => errors.push(error.message));
      // These are presentation fixtures, not protocol-validation fixtures. API
      // interception makes the browser test independent of operator data.
      await page.route("**/api/**", async (request) => {
        const path = new URL(request.request().url()).pathname;
        const data =
          path === "/api/settings"
            ? {
                mode: "replay",
                active_collection_id: 1,
                first_event_delay_ms: 0,
                delay_multiplier: 0,
                history_limit: 10000,
              }
            : path === "/api/collections"
              ? [
                  {
                    id: 1,
                    name: "Viewer fixtures",
                    exclusions: [],
                    created_at: entry.recording.created_at,
                  },
                ]
              : path === "/api/recordings"
                ? [listed(entry)]
                : path === "/api/recordings/1"
                  ? entry
                  : path === "/api/analytics"
                    ? {
                        total: 1,
                        lifetime_total: 1,
                        hits: 1,
                        misses: 0,
                        errors: 0,
                        recorded: 0,
                        hit_rate: 1,
                        sources: {},
                        series: [],
                      }
                    : [];
        await request.fulfill({ json: data });
      });
      // The shell deep-links the recording inspector from the hash.
      await page.goto("/#/recordings?recording=1");
      const viewer = page.getByRole("region", { name: "Response browser" });
      await expect(viewer).toBeVisible();
      await expect(
        viewer.getByText("Readable answer", { exact: true }),
      ).toBeVisible();
      await expect(
        viewer.getByText("const safe = true;", { exact: true }),
      ).toBeVisible();
      await expect(
        viewer.getByText("call_viewer", { exact: true }),
      ).toBeVisible();
      await expect(viewer.getByText(large, { exact: true })).toBeVisible();
      await viewer
        .locator("summary")
        .filter({ hasText: /^nested/ })
        .click();
      await expect(viewer.getByText("Paris", { exact: true })).toBeVisible();
      expect(
        await viewer.evaluate(
          (element) => element.scrollWidth <= element.clientWidth + 1,
        ),
      ).toBe(true);
      await page.screenshot({
        path: testInfo.outputPath("readable.png"),
        fullPage: true,
      });
      await viewer.getByRole("button", { name: "Collapse all" }).click();
      await expect(
        viewer.getByText("Readable answer", { exact: true }),
      ).toHaveCount(0);
      await viewer.getByRole("button", { name: "Expand all" }).click();
      await expect(
        viewer.getByText("Readable answer", { exact: true }).first(),
      ).toBeVisible();
      await expect(
        viewer.getByText(
          "Provider-reported usage from the original recording. Edits do not recalculate token counts.",
        ),
      ).toBeVisible();
      await viewer.getByRole("tab", { name: "Raw", exact: true }).click();
      const raw = streaming
        ? entry.revision.events.map((event) => event.data).join("")
        : entry.revision.body;
      expect(await viewer.getByLabel("Raw response").textContent()).toBe(raw);
      if (streaming) {
        await viewer.getByRole("tab", { name: "Event timeline" }).click();
        await expect(viewer).toContainText("+25 ms");
        await expect(
          viewer.getByText("Raw SSE frame", { exact: true }).first(),
        ).toBeVisible();
      }
      await page.screenshot({
        path: testInfo.outputPath("viewer.png"),
        fullPage: true,
      });

      await page.getByRole("tab", { name: "Request", exact: true }).click();
      const request = page.getByRole("region", { name: "Request browser" });
      await expect(request).toBeVisible();
      const conversation = request.getByRole("list", {
        name: "Request conversation",
      });
      await expect(
        conversation.getByText("Rules", { exact: true }),
      ).toBeVisible();
      await expect(
        conversation.getByText("Find Paris", { exact: true }),
      ).toBeVisible();
      await expect(
        conversation.getByRole("img", { name: /PNG image/ }),
      ).toHaveCount(1);
      await expect(conversation.getByText("No arguments")).toBeVisible();
      await expect(conversation.getByText("Result of")).toBeVisible();
      await expect(
        conversation.getByText("call_viewer", { exact: true }),
      ).toHaveCount(2);
      await expect(
        conversation.getByText("Paris", { exact: true }),
      ).toBeVisible();
      expect(
        await request.evaluate(
          (element) => element.scrollWidth <= element.clientWidth + 1,
        ),
      ).toBe(true);
      await request.getByRole("tab", { name: /^Tools/ }).click();
      await expect(request.getByText("Look up a city")).toBeVisible();
      await request
        .getByRole("tab", { name: "Parameters", exact: true })
        .click();
      await expect(request.getByText("model", { exact: true })).toBeVisible();
      await request.getByRole("tab", { name: "Raw", exact: true }).click();
      await expect(request.getByLabel("Raw request")).toContainText(
        "Find Paris",
      );
      await page.screenshot({
        path: testInfo.outputPath("request.png"),
        fullPage: true,
      });
      expect(errors).toEqual([]);
    });
  }
}
