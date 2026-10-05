import {
  object,
  parseExactJSON,
  type JSONObject,
  type JSONValue,
} from "./response-viewer.ts";

/** One readable piece of a conversation turn, shared by request and response views. */
export type Part =
  | { kind: "text"; text: string; raw: JSONValue }
  | {
      kind: "image";
      /** Only inline data URLs are displayed; remote URLs are never fetched. */
      src?: string;
      url?: string;
      fileId?: string;
      mediaType?: string;
      bytes?: number;
      detail?: string;
      raw: JSONValue;
    }
  | {
      kind: "attachment";
      title: string;
      mediaType?: string;
      bytes?: number;
      reference?: string;
      raw: JSONValue;
    }
  | {
      kind: "tool_call";
      name: string;
      id?: string;
      arguments: JSONValue;
      raw: JSONValue;
    }
  | {
      kind: "tool_result";
      id?: string;
      name?: string;
      isError?: boolean;
      parts: Part[];
      raw: JSONValue;
    }
  | {
      kind: "reasoning";
      text: string;
      encrypted: boolean;
      redacted: boolean;
      raw: JSONValue;
    }
  | { kind: "refusal"; text: string; raw: JSONValue }
  | {
      kind: "data";
      title: string;
      value: JSONValue;
      raw: JSONValue;
      open?: boolean;
    };

export type Role =
  "instructions" | "system" | "developer" | "user" | "assistant" | "tool";
export type Turn = { role: Role; name?: string; parts: Part[]; raw: JSONValue };
export type ToolDefinition = {
  name: string;
  type: string;
  description?: string;
  schema?: JSONValue;
  raw: JSONValue;
};
export type RequestView = {
  protocol: string;
  model?: string;
  turns: Turn[];
  tools: ToolDefinition[];
  parameters: JSONObject;
  counts: { toolCalls: number; images: number; attachments: number };
  bytes: number;
  warnings: string[];
};

const array = (value: JSONValue | undefined): JSONValue[] =>
  Array.isArray(value) ? value : [];
const str = (value: JSONValue | undefined): string =>
  typeof value === "string" ? value : "";

/** Decoded size of base64 text, ignoring padding. */
export function base64Bytes(data: string): number {
  const clean = data.replace(/\s/g, "");
  const padding = clean.endsWith("==") ? 2 : clean.endsWith("=") ? 1 : 0;
  return Math.max(0, Math.floor((clean.length * 3) / 4) - padding);
}
function dataURL(url: string): { mediaType: string; bytes: number } | null {
  const match = /^data:([^;,]*)(;[^,]*)?,/.exec(url);
  if (!match) return null;
  const payload = url.slice(match[0].length);
  return {
    mediaType: match[1] || "text/plain",
    bytes: match[2]?.includes(";base64")
      ? base64Bytes(payload)
      : payload.length,
  };
}
function argumentsValue(value: JSONValue | undefined): JSONValue {
  if (typeof value !== "string") return value ?? null;
  try {
    return parseExactJSON(value);
  } catch {
    return value;
  }
}
function image(
  raw: JSONValue,
  url: string,
  extra: { detail?: string; fileId?: string } = {},
): Part {
  const inline = dataURL(url);
  // Only raster data URLs render; an SVG or a remote URL is described instead.
  const renderable =
    inline && /^image\/(png|jpe?g|gif|webp|avif)$/.test(inline.mediaType);
  return {
    kind: "image",
    raw,
    ...(renderable ? { src: url } : {}),
    ...(inline
      ? { mediaType: inline.mediaType, bytes: inline.bytes }
      : url
        ? { url }
        : {}),
    ...extra,
  };
}

/** Content parts used across Chat Completions, Responses and Messages. */
export function contentParts(value: JSONValue | undefined): Part[] {
  if (typeof value === "string")
    return [{ kind: "text", text: value, raw: value }];
  return array(value).flatMap((raw): Part[] => {
    if (typeof raw === "string") return [{ kind: "text", text: raw, raw }];
    const part = object(raw),
      type = str(part.type);
    if (["text", "input_text", "output_text", "summary_text"].includes(type))
      return [{ kind: "text", text: str(part.text), raw }];
    if (type === "refusal")
      return [
        { kind: "refusal", text: str(part.refusal) || str(part.text), raw },
      ];
    if (type === "image_url") {
      const ref = object(part.image_url);
      return [
        image(raw, str(ref.url) || str(part.image_url), {
          detail: str(ref.detail) || undefined,
        }),
      ];
    }
    if (type === "input_image")
      return [
        image(raw, str(part.image_url), {
          detail: str(part.detail) || undefined,
          fileId: str(part.file_id) || undefined,
        }),
      ];
    if (type === "image") {
      const source = object(part.source);
      if (source.type === "base64")
        return [
          image(
            raw,
            `data:${str(source.media_type)};base64,${str(source.data)}`,
          ),
        ];
      return [
        image(raw, str(source.url), {
          fileId: str(source.file_id) || undefined,
        }),
      ];
    }
    if (["input_file", "file", "document"].includes(type)) {
      const file = type === "file" ? object(part.file) : part;
      const source = object(part.source);
      const inline =
        dataURL(str(file.file_data)) ||
        (source.type === "base64"
          ? {
              mediaType: str(source.media_type),
              bytes: base64Bytes(str(source.data)),
            }
          : null);
      return [
        {
          kind: "attachment",
          title:
            str(file.filename) ||
            str(part.title) ||
            (type === "document" ? "Document" : "File"),
          mediaType: inline?.mediaType || str(source.media_type) || undefined,
          bytes: inline?.bytes,
          reference:
            str(file.file_id) ||
            str(file.file_url) ||
            str(source.url) ||
            str(source.file_id) ||
            undefined,
          raw,
        },
      ];
    }
    if (type === "input_audio") {
      const audio = object(part.input_audio);
      const data = str(audio.data) || str(part.data);
      return [
        {
          kind: "attachment",
          title: "Audio",
          mediaType: str(audio.format) || str(part.format) || undefined,
          bytes: data ? base64Bytes(data) : undefined,
          raw,
        },
      ];
    }
    if (["thinking", "redacted_thinking", "reasoning"].includes(type))
      return [reasoning(part, raw)];
    if (["tool_use", "server_tool_use", "mcp_tool_use"].includes(type))
      return [
        {
          kind: "tool_call",
          name: str(part.name) || type,
          id: str(part.id) || undefined,
          arguments: argumentsValue(part.input),
          raw,
        },
      ];
    if (type === "tool_result" || type.endsWith("_tool_result"))
      return [
        {
          kind: "tool_result",
          id: str(part.tool_use_id) || undefined,
          isError: part.is_error === true,
          parts: resultParts(part.content),
          raw,
        },
      ];
    return [
      {
        kind: "data",
        title: type ? type.replaceAll("_", " ") : "Content",
        value: raw,
        raw,
      },
    ];
  });
}
function reasoning(part: JSONObject, raw: JSONValue): Part {
  const texts = (items: JSONValue | undefined) =>
    array(items)
      .map((x) => str(object(x).text))
      .filter(Boolean)
      .join("\n\n");
  return {
    kind: "reasoning",
    text: str(part.thinking) || texts(part.summary) || texts(part.content),
    encrypted: Boolean(part.encrypted_content || part.signature),
    redacted: part.type === "redacted_thinking",
    raw,
  };
}
/** Tool output text that is a JSON document reads better as fields. */
function jsonText(part: Part): Part {
  if (part.kind !== "text" || !/^\s*[[{]/.test(part.text)) return part;
  try {
    const value = parseExactJSON(part.text.trim());
    return { kind: "data", title: "Output", value, raw: part.raw, open: true };
  } catch {
    return part;
  }
}
/** Tool output is a string, content parts, or arbitrary JSON. */
export function resultParts(value: JSONValue | undefined): Part[] {
  if (value === undefined || value === null) return [];
  if (typeof value === "string" || Array.isArray(value))
    return contentParts(value).map(jsonText);
  return [{ kind: "data", title: "Output", value, raw: value, open: true }];
}

function chatTurns(request: JSONObject): Turn[] {
  return array(request.messages).map((raw): Turn => {
    const message = object(raw),
      role = str(message.role);
    if (role === "tool" || role === "function")
      return {
        role: "tool",
        parts: [
          {
            kind: "tool_result",
            id: str(message.tool_call_id) || undefined,
            name: str(message.name) || undefined,
            parts: resultParts(message.content),
            raw,
          },
        ],
        raw,
      };
    const parts = contentParts(message.content ?? undefined);
    if (message.refusal)
      parts.push({
        kind: "refusal",
        text: str(message.refusal),
        raw: message.refusal,
      });
    const thought = message.reasoning_content ?? message.reasoning;
    if (typeof thought === "string" && thought)
      parts.unshift({
        kind: "reasoning",
        text: thought,
        encrypted: false,
        redacted: false,
        raw: thought,
      });
    const calls = [...array(message.tool_calls)];
    if (message.function_call)
      calls.push({ type: "function", function: message.function_call });
    for (const rawCall of calls) {
      const call = object(rawCall),
        fn = object(call.function),
        custom = object(call.custom);
      parts.push({
        kind: "tool_call",
        name: str(fn.name) || str(custom.name) || "Tool call",
        id: str(call.id) || undefined,
        arguments: argumentsValue(fn.arguments ?? custom.input),
        raw: rawCall,
      });
    }
    return {
      role: (["system", "developer", "user", "assistant"].includes(role)
        ? role
        : "user") as Role,
      name: str(message.name) || undefined,
      parts,
      raw,
    };
  });
}

function responsesTurns(request: JSONObject): Turn[] {
  const turns: Turn[] = [];
  if (typeof request.instructions === "string" && request.instructions)
    turns.push({
      role: "instructions",
      parts: [
        { kind: "text", text: request.instructions, raw: request.instructions },
      ],
      raw: request.instructions,
    });
  if (typeof request.input === "string") {
    turns.push({
      role: "user",
      parts: [{ kind: "text", text: request.input, raw: request.input }],
      raw: request.input,
    });
    return turns;
  }
  const add = (role: Role, part: Part, raw: JSONValue) => {
    // Function calls, reasoning and outputs arrive as separate items; adjacent
    // assistant or tool items read as one step of the conversation.
    const last = turns[turns.length - 1];
    if (
      last &&
      last.role === role &&
      (role === "assistant" || role === "tool")
    ) {
      last.parts.push(part);
      last.raw = [...(Array.isArray(last.raw) ? last.raw : [last.raw]), raw];
    } else turns.push({ role, parts: [part], raw });
  };
  for (const raw of array(request.input)) {
    const item = object(raw),
      type = str(item.type);
    if (!type || type === "message") {
      const role = str(item.role);
      const parts = contentParts(item.content);
      const mapped = (
        ["system", "developer", "user", "assistant"].includes(role)
          ? role
          : "user"
      ) as Role;
      if (mapped === "assistant")
        for (const part of parts) add("assistant", part, raw);
      else turns.push({ role: mapped, parts, raw });
    } else if (type === "function_call" || type === "custom_tool_call")
      add(
        "assistant",
        {
          kind: "tool_call",
          name: str(item.name) || type,
          id: str(item.call_id) || str(item.id) || undefined,
          arguments: argumentsValue(item.arguments ?? item.input),
          raw,
        },
        raw,
      );
    else if (type.endsWith("_call_output"))
      add(
        "tool",
        {
          kind: "tool_result",
          id: str(item.call_id) || undefined,
          parts: resultParts(item.output),
          raw,
        },
        raw,
      );
    else if (type === "reasoning") add("assistant", reasoning(item, raw), raw);
    else
      add(
        "assistant",
        { kind: "data", title: type.replaceAll("_", " "), value: raw, raw },
        raw,
      );
  }
  return turns;
}

function messagesTurns(request: JSONObject): Turn[] {
  const turns: Turn[] = [];
  if (request.system !== undefined && request.system !== null)
    turns.push({
      role: "system",
      parts: contentParts(request.system),
      raw: request.system,
    });
  for (const raw of array(request.messages)) {
    const message = object(raw);
    const parts = contentParts(message.content);
    // Anthropic sends tool results in user messages; show them as the tool's turn.
    const role: Role =
      message.role === "assistant"
        ? "assistant"
        : parts.length && parts.every((part) => part.kind === "tool_result")
          ? "tool"
          : "user";
    turns.push({ role, parts, raw });
  }
  return turns;
}

function toolDefinitions(request: JSONObject): ToolDefinition[] {
  return [...array(request.tools), ...array(request.functions)].map((raw) => {
    const tool = object(raw),
      fn = tool.function
        ? object(tool.function)
        : tool.custom
          ? object(tool.custom)
          : tool,
      type = str(tool.type) || (request.functions ? "function" : "tool");
    return {
      name: str(fn.name) || str(tool.name) || type,
      type,
      description: str(fn.description) || undefined,
      schema: fn.parameters ?? fn.input_schema ?? fn.format ?? undefined,
      raw,
    };
  });
}

/** Attach the calling tool's name to each result that answers a known call. */
function nameResults(turns: Turn[]) {
  const names = new Map<string, string>();
  const visit = (parts: Part[]) => {
    for (const part of parts) {
      if (part.kind === "tool_call" && part.id) names.set(part.id, part.name);
      if (part.kind === "tool_result") {
        if (!part.name && part.id) part.name = names.get(part.id);
        visit(part.parts);
      }
    }
  };
  for (const turn of turns) visit(turn.parts);
}
function count(parts: Part[], counts: RequestView["counts"]) {
  for (const part of parts) {
    if (part.kind === "tool_call") counts.toolCalls++;
    if (part.kind === "image") counts.images++;
    if (part.kind === "attachment") counts.attachments++;
    if (part.kind === "tool_result") count(part.parts, counts);
  }
}

export function protocolName(route: string): string {
  return route.endsWith("/messages")
    ? "Anthropic Messages"
    : route.endsWith("/responses")
      ? "Responses"
      : "Chat Completions";
}

export function requestView(route: string, raw: string): RequestView {
  const protocol = protocolName(route);
  const view: RequestView = {
    protocol,
    turns: [],
    tools: [],
    parameters: {},
    counts: { toolCalls: 0, images: 0, attachments: 0 },
    bytes: new TextEncoder().encode(raw).length,
    warnings: [],
  };
  let request: JSONObject;
  try {
    request = object(parseExactJSON(raw));
  } catch {
    view.warnings.push(
      "This request is not valid JSON. The stored request is available in Raw.",
    );
    return view;
  }
  view.model = typeof request.model === "string" ? request.model : undefined;
  try {
    view.turns =
      protocol === "Chat Completions"
        ? chatTurns(request)
        : protocol === "Responses"
          ? responsesTurns(request)
          : messagesTurns(request);
    nameResults(view.turns);
    for (const turn of view.turns) count(turn.parts, view.counts);
    view.tools = toolDefinitions(request);
  } catch {
    view.warnings.push(
      "Part of this request could not be interpreted. The stored request is available in Raw.",
    );
  }
  const conversation = [
    "messages",
    "input",
    "instructions",
    "system",
    "tools",
    "functions",
  ];
  view.parameters = Object.fromEntries(
    Object.entries(request).filter(([key]) => !conversation.includes(key)),
  );
  return view;
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
