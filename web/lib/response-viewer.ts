/** Response inspection keeps JSON numbers as source text, never rounded JS floats. */
export class JSONNumber {
  readonly source: string;
  constructor(source: string) {
    this.source = source;
  }
}
export type JSONValue =
  null | boolean | string | number | JSONNumber | JSONValue[] | JSONObject;
export type JSONObject = { [key: string]: JSONValue };
export type ResponseEvent = { data: string; offset_ms: number };
export type ResponseRevision = {
  id?: number;
  status: number;
  headers: Record<string, string>;
  body: string;
  events: ResponseEvent[];
  source?: string;
};
export type ResponseBlock = {
  kind: "text" | "tool" | "result" | "reasoning" | "refusal" | "other";
  title: string;
  text?: string;
  id?: string;
  arguments?: JSONValue;
  raw: JSONValue;
};
export type ResponseView = {
  protocol: string;
  metadata: JSONObject;
  usage?: JSONValue;
  blocks: ResponseBlock[];
  warnings: string[];
  frames: ParsedEvent[];
};
export type ParsedEvent = ResponseEvent & {
  name: string;
  value?: JSONValue;
  error?: string;
};

export function parseExactJSON(source: string): JSONValue {
  let pos = 0;
  const space = () => {
    while (/[\t\r\n ]/.test(source[pos] || "x")) pos++;
  };
  const string = (): string => {
    const start = pos++;
    while (pos < source.length) {
      const char = source[pos++];
      if (char === "\\") pos++;
      else if (char === '"') return JSON.parse(source.slice(start, pos));
    }
    throw new Error("Unterminated JSON string");
  };
  const value = (): JSONValue => {
    space();
    const c = source[pos];
    if (c === '"') return string();
    if (c === "{" || c === "[") {
      const object = c === "{";
      pos++;
      space();
      const result: JSONObject | JSONValue[] = object
        ? Object.create(null)
        : [];
      const close = object ? "}" : "]";
      if (source[pos] === close) {
        pos++;
        return result;
      }
      for (;;) {
        space();
        if (object) {
          if (source[pos] !== '"') throw new Error("Expected an object field");
          const key = string();
          space();
          if (source[pos++] !== ":") throw new Error("Expected ':'");
          (result as JSONObject)[key] = value();
        } else (result as JSONValue[]).push(value());
        space();
        if (source[pos] === close) {
          pos++;
          return result;
        }
        if (source[pos++] !== ",") throw new Error("Expected ','");
      }
    }
    for (const [literal, result] of [
      ["true", true],
      ["false", false],
      ["null", null],
    ] as const) {
      if (source.startsWith(literal, pos)) {
        pos += literal.length;
        return result;
      }
    }
    const number = source
      .slice(pos)
      .match(/^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/);
    if (number) {
      pos += number[0].length;
      return new JSONNumber(number[0]);
    }
    throw new Error("Invalid JSON value");
  };
  const result = value();
  space();
  if (pos !== source.length) throw new Error("Unexpected trailing JSON data");
  return result;
}
export function exactJSON(value: JSONValue, depth = 0): string {
  if (value instanceof JSONNumber) return value.source;
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  const pad = "  ".repeat(depth),
    inner = pad + "  ";
  if (Array.isArray(value))
    return value.length
      ? `[\n${value.map((x) => inner + exactJSON(x, depth + 1)).join(",\n")}\n${pad}]`
      : "[]";
  const entries = Object.entries(value);
  return entries.length
    ? `{\n${entries.map(([k, v]) => `${inner}${JSON.stringify(k)}: ${exactJSON(v, depth + 1)}`).join(",\n")}\n${pad}}`
    : "{}";
}
export function object(value: JSONValue | undefined): JSONObject {
  return value !== null &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    !(value instanceof JSONNumber)
    ? value
    : Object.create(null);
}
const array = (value: JSONValue | undefined): JSONValue[] =>
  Array.isArray(value) ? value : [];
const str = (value: JSONValue | undefined): string =>
  typeof value === "string" ? value : "";
const index = (value: JSONValue | undefined): string =>
  value instanceof JSONNumber
    ? value.source
    : typeof value === "number"
      ? String(value)
      : "0";
function argumentsValue(value: JSONValue | undefined): JSONValue {
  if (typeof value !== "string") return value ?? null;
  try {
    return parseExactJSON(value);
  } catch {
    return value;
  }
}
export function parseEvents(events: ResponseEvent[]): ParsedEvent[] {
  return events.map((event) => {
    let name = "message";
    const data: string[] = [];
    for (const line of event.data.replace(/\r\n/g, "\n").split("\n")) {
      if (line.startsWith("event:")) name = line.slice(6).trim();
      if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
    }
    const payload = data.join("\n");
    if (!payload) return { ...event, name: "keepalive" };
    if (payload === "[DONE]") return { ...event, name: "[DONE]" };
    try {
      const value = parseExactJSON(payload);
      return { ...event, name: str(object(value).type) || name, value };
    } catch {
      return {
        ...event,
        name,
        error:
          "Event data is not valid JSON; inspect the original frame below.",
      };
    }
  });
}
function contentBlocks(
  value: JSONValue | undefined,
  title = "Assistant answer",
): ResponseBlock[] {
  if (typeof value === "string")
    return [{ kind: "text", title, text: value, raw: value }];
  return array(value).map((raw): ResponseBlock => {
    const part = object(raw),
      type = str(part.type),
      id = str(part.call_id) || str(part.id) || str(part.tool_use_id);
    if (["text", "output_text", "input_text"].includes(type))
      return { kind: "text", title, text: str(part.text), raw };
    if (type === "refusal")
      return {
        kind: "refusal",
        title: "Refusal",
        text: str(part.refusal) || str(part.text),
        raw,
      };
    if (["thinking", "reasoning", "redacted_thinking"].includes(type)) {
      const summaries = array(part.summary)
        .map((x) => str(object(x).text))
        .filter(Boolean)
        .join("\n\n");
      return {
        kind: "reasoning",
        title:
          type === "redacted_thinking" ? "Redacted reasoning" : "Reasoning",
        text: str(part.thinking) || summaries,
        raw,
        id,
      };
    }
    if (
      [
        "tool_use",
        "server_tool_use",
        "function_call",
        "custom_tool_call",
      ].includes(type)
    )
      return {
        kind: "tool",
        title: str(part.name) || type,
        id,
        arguments: argumentsValue(part.arguments ?? part.input),
        raw,
      };
    if (
      type === "tool_result" ||
      type.endsWith("_tool_result") ||
      ["function_call_output", "custom_tool_call_output"].includes(type)
    )
      return {
        kind: "result",
        title: "Tool result",
        id,
        arguments: part.output ?? part.content ?? null,
        raw,
      };
    return {
      kind: "other",
      title: type ? type.replaceAll("_", " ") : "Additional output",
      id,
      raw,
    };
  });
}
function chatBlocks(response: JSONObject): ResponseBlock[] {
  return array(response.choices).flatMap((raw, i) => {
    const choice = object(raw),
      message = object(choice.message),
      blocks = contentBlocks(
        message.content,
        array(response.choices).length > 1
          ? `Assistant answer · choice ${index(choice.index ?? i)}`
          : "Assistant answer",
      );
    if (message.refusal)
      blocks.push({
        kind: "refusal",
        title: "Refusal",
        text: str(message.refusal),
        raw: message.refusal,
      });
    if (message.reasoning_content || message.reasoning)
      blocks.push({
        kind: "reasoning",
        title: "Reasoning",
        text: str(message.reasoning_content ?? message.reasoning),
        raw: message.reasoning_content ?? message.reasoning,
      });
    const calls = [...array(message.tool_calls)];
    if (message.function_call)
      calls.push({ type: "function", function: message.function_call });
    for (const rawCall of calls) {
      const call = object(rawCall),
        fn = object(call.function),
        custom = object(call.custom);
      blocks.push({
        kind: "tool",
        title:
          str(fn.name) || str(custom.name) || str(call.type) || "Tool call",
        id: str(call.id),
        arguments: argumentsValue(fn.arguments ?? custom.input),
        raw: rawCall,
      });
    }
    const extra = Object.fromEntries(
      Object.entries(message).filter(
        ([key]) =>
          ![
            "role",
            "content",
            "tool_calls",
            "function_call",
            "refusal",
            "reasoning",
            "reasoning_content",
          ].includes(key),
      ),
    );
    if (Object.keys(extra).length)
      blocks.push({
        kind: "other",
        title: "Additional message fields",
        raw: extra,
      });
    if (choice.finish_reason)
      blocks.push({
        kind: "other",
        title: `Finish reason · ${str(choice.finish_reason)}`,
        raw: {
          finish_reason: choice.finish_reason,
          ...(choice.logprobs ? { logprobs: choice.logprobs } : {}),
        },
      });
    return blocks;
  });
}
function streamedChat(frames: ParsedEvent[]): JSONObject {
  const result: JSONObject = { choices: [] },
    choices = new Map<string, JSONObject>();
  const tools = new Map<string, Map<string, JSONObject>>();
  for (const frame of frames) {
    const o = object(frame.value);
    for (const key of ["id", "model", "created", "usage", "system_fingerprint"])
      if (o[key] !== undefined) result[key] = o[key];
    for (const raw of array(o.choices)) {
      const choice = object(raw),
        key = index(choice.index),
        delta = object(choice.delta);
      let output = choices.get(key);
      if (!output) {
        output = { index: choice.index ?? 0, message: { role: "assistant" } };
        choices.set(key, output);
      }
      const message = object(output.message);
      for (const field of [
        "content",
        "refusal",
        "reasoning_content",
        "reasoning",
      ]) {
        if (typeof delta[field] === "string")
          message[field] = str(message[field]) + delta[field];
        else if (delta[field] !== undefined && delta[field] !== null)
          message[field] = [...array(message[field]), ...array(delta[field])];
      }
      for (const [field, value] of Object.entries(delta))
        if (
          ![
            "content",
            "refusal",
            "reasoning_content",
            "reasoning",
            "tool_calls",
            "function_call",
          ].includes(field)
        )
          message[field] = value;
      if (choice.finish_reason !== undefined)
        output.finish_reason = choice.finish_reason;
      let calls = tools.get(key);
      if (!calls) {
        calls = new Map();
        tools.set(key, calls);
      }
      const fragments = [...array(delta.tool_calls)];
      if (delta.function_call)
        fragments.push({
          index: "legacy",
          type: "function",
          function: delta.function_call,
        });
      for (const rawCall of fragments) {
        const fragment = object(rawCall),
          toolIndex =
            fragment.index === "legacy" ? "legacy" : index(fragment.index);
        let call = calls.get(toolIndex);
        if (!call) {
          call = {};
          calls.set(toolIndex, call);
        }
        for (const [field, value] of Object.entries(fragment))
          if (!["function", "custom"].includes(field)) call[field] = value;
        for (const field of ["function", "custom"])
          if (fragment[field]) {
            const target = object(call[field]),
              part = object(fragment[field]);
            for (const [name, value] of Object.entries(part))
              target[name] = ["arguments", "input"].includes(name)
                ? str(target[name]) + str(value)
                : value;
            call[field] = target;
          }
      }
      if (calls.size) message.tool_calls = [...calls.values()];
    }
  }
  result.choices = [...choices.values()];
  return result;
}
function streamedMessages(frames: ParsedEvent[]): JSONObject {
  let result: JSONObject = {},
    blocks = new Map<string, JSONObject>();
  const inputs = new Map<string, string>();
  for (const frame of frames) {
    const o = object(frame.value),
      type = str(o.type),
      key = index(o.index);
    if (type === "message_start") {
      result = { ...object(o.message) };
      blocks = new Map(
        array(result.content).map((x, i) => [String(i), object(x)]),
      );
    } else if (type === "content_block_start")
      blocks.set(key, { ...object(o.content_block) });
    else if (type === "content_block_delta") {
      const block = blocks.get(key) || {},
        delta = object(o.delta);
      if (delta.type === "text_delta")
        block.text = str(block.text) + str(delta.text);
      else if (delta.type === "thinking_delta")
        block.thinking = str(block.thinking) + str(delta.thinking);
      else if (delta.type === "signature_delta")
        block.signature = str(block.signature) + str(delta.signature);
      else if (delta.type === "input_json_delta")
        inputs.set(key, (inputs.get(key) || "") + str(delta.partial_json));
      else if (delta.type === "citations_delta" && delta.citation)
        block.citations = [...array(block.citations), delta.citation];
      blocks.set(key, block);
    } else if (type === "message_delta") {
      result = { ...result, ...object(o.delta) };
      if (o.usage)
        result.usage = { ...object(result.usage), ...object(o.usage) };
    }
  }
  result.content = [...blocks.entries()]
    .sort(([a], [b]) => Number(a) - Number(b))
    .map(([key, block]) =>
      inputs.has(key)
        ? { ...block, input: argumentsValue(inputs.get(key)!) }
        : block,
    );
  return result;
}
export function responseView(
  route: string,
  streaming: boolean,
  revision: ResponseRevision,
): ResponseView {
  const protocol = route.endsWith("/messages")
    ? "Anthropic Messages"
    : route.endsWith("/responses")
      ? "Responses"
      : "Chat Completions";
  const frames = streaming ? parseEvents(revision.events || []) : [];
  const view: ResponseView = {
    protocol,
    metadata: {},
    blocks: [],
    warnings: [],
    frames,
  };
  try {
    let response: JSONObject;
    if (!streaming) response = object(parseExactJSON(revision.body));
    else if (protocol === "Chat Completions") response = streamedChat(frames);
    else if (protocol === "Anthropic Messages")
      response = streamedMessages(frames);
    else {
      const completed = frames.findLast(
        (x) => object(x.value).type === "response.completed",
      );
      response = object(object(completed?.value).response);
      if (!completed)
        view.warnings.push(
          "No completed Responses snapshot was found. Use the event timeline to inspect the stream.",
        );
    }
    view.usage = response.usage;
    view.metadata = Object.fromEntries(
      Object.entries(response).filter(
        ([key]) =>
          !["choices", "output", "content", "output_text", "usage"].includes(
            key,
          ),
      ),
    );
    if (protocol === "Chat Completions") view.blocks = chatBlocks(response);
    else if (protocol === "Anthropic Messages")
      view.blocks = contentBlocks(response.content);
    else
      view.blocks = array(response.output).flatMap((raw) => {
        const item = object(raw);
        if (item.type !== "message") return contentBlocks([raw]);
        const fields = Object.fromEntries(
          Object.entries(item).filter(([key]) => key !== "content"),
        );
        return contentBlocks(
          item.content,
          `Assistant answer${item.id ? ` · ${str(item.id)}` : ""}`,
        ).map((block) => ({
          ...block,
          id: str(item.id),
          raw: { ...fields, content_block: block.raw },
        }));
      });
    if (frames.some((x) => x.error))
      view.warnings.push(
        "Some events could not be interpreted. Their original content is available in the event timeline.",
      );
  } catch {
    view.warnings.push(
      "This response could not be interpreted. The complete original response is available in Raw.",
    );
  }
  return view;
}
