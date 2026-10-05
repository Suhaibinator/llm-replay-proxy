"use client";

import { useMemo, useState } from "react";
import { Braces } from "lucide-react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import {
  Chip,
  Disclosure,
  ExpandControls,
  ExpansionProvider,
  FieldList,
  JsonTree,
  RawPanel,
  Transcript,
  type Expansion,
} from "@/components/transcript";
import {
  resultParts,
  formatBytes,
  type Part,
  type Turn,
} from "@/lib/request-viewer";
import {
  JSONNumber,
  object,
  responseView,
  type JSONValue,
  type ResponseBlock,
  type ResponseRevision,
} from "@/lib/response-viewer";

const EVENT_PAGE = 100;

function blockPart(block: ResponseBlock): Part {
  switch (block.kind) {
    case "text":
      return { kind: "text", text: block.text ?? "", raw: block.raw };
    case "refusal":
      return { kind: "refusal", text: block.text ?? "", raw: block.raw };
    case "reasoning": {
      const raw = object(block.raw);
      return {
        kind: "reasoning",
        text: block.text ?? "",
        encrypted: Boolean(raw.encrypted_content || raw.signature),
        redacted: block.title.startsWith("Redacted"),
        raw: block.raw,
      };
    }
    case "tool":
      return {
        kind: "tool_call",
        name: block.title,
        id: block.id || undefined,
        arguments: block.arguments ?? null,
        raw: block.raw,
      };
    case "result": {
      const output = block.arguments;
      return {
        kind: "tool_result",
        id: block.id || undefined,
        parts: resultParts(output),
        raw: block.raw,
      };
    }
    default:
      return {
        kind: "data",
        title: block.title,
        value: block.raw,
        raw: block.raw,
      };
  }
}

/** One assistant turn per choice; most responses have a single choice. */
function responseTurns(blocks: ResponseBlock[]): Turn[] {
  const turns: Turn[] = [];
  for (const block of blocks) {
    const choice = /· choice (\S+)$/.exec(block.title)?.[1];
    const name = choice ? `choice ${choice}` : undefined;
    const last = turns[turns.length - 1];
    if (last && (last.name === name || name === undefined))
      last.parts.push(blockPart(block));
    else
      turns.push({
        role: "assistant",
        name,
        parts: [blockPart(block)],
        raw: block.raw,
      });
  }
  return turns;
}

const humanize = (key: string) => {
  const words = key.replaceAll("_", " ").replace(/ details$/, "");
  return words.charAt(0).toUpperCase() + words.slice(1);
};
/** Numeric usage counters, flattened one level for display. */
function usageNumbers(usage: JSONValue | undefined): [string, string][] {
  const out: [string, string][] = [];
  const numeric = (value: JSONValue) =>
    value instanceof JSONNumber
      ? value.source
      : typeof value === "number"
        ? String(value)
        : null;
  for (const [key, value] of Object.entries(object(usage))) {
    const number = numeric(value);
    if (number !== null) out.push([humanize(key), number]);
    else
      for (const [child, nested] of Object.entries(object(value))) {
        const inner = numeric(nested);
        if (inner !== null)
          out.push([
            `${humanize(child)} (${humanize(key).toLowerCase()})`,
            inner,
          ]);
      }
  }
  return out;
}

export function ResponseViewer({
  route,
  streaming,
  revision,
}: {
  route: string;
  streaming: boolean;
  revision: ResponseRevision;
}) {
  const view = useMemo(
    () => responseView(route, streaming, revision),
    [route, streaming, revision],
  );
  const turns = useMemo(() => responseTurns(view.blocks), [view.blocks]);
  const [expansion, setExpansion] = useState<Expansion>({
    open: null,
    generation: 0,
  });
  const [eventLimit, setEventLimit] = useState(EVENT_PAGE);
  const raw = streaming
    ? revision.events.map((event) => event.data).join("")
    : revision.body;
  const toolCount = view.blocks.filter((block) => block.kind === "tool").length;
  const model =
    typeof view.metadata.model === "string" ? view.metadata.model : "";
  const finish = view.blocks
    .map((block) => /^Finish reason · (.+)$/.exec(block.title)?.[1])
    .find(Boolean);
  const outcome =
    finish ||
    (typeof view.metadata.stop_reason === "string" &&
      view.metadata.stop_reason) ||
    (typeof view.metadata.status === "string" && view.metadata.status) ||
    "";
  const usage = usageNumbers(view.usage);
  const lastOffset = Math.max(
    1,
    ...view.frames.map((frame) => frame.offset_ms),
  );
  return (
    <section aria-label="Response browser" className="min-w-0 space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="mr-1 text-sm font-semibold">Response</h3>
        <Chip>{view.protocol} API</Chip>
        {model && <Chip className="font-medium">{model}</Chip>}
        {outcome && <Chip>{outcome.replaceAll("_", " ")}</Chip>}
        <Chip>
          {streaming
            ? `${revision.events.length.toLocaleString()} SSE events`
            : "JSON body"}
        </Chip>
        {toolCount > 0 && (
          <Chip>
            {toolCount} tool {toolCount === 1 ? "call" : "calls"}
          </Chip>
        )}
        <Chip>{formatBytes(new TextEncoder().encode(raw).length)}</Chip>
      </div>
      <ExpansionProvider value={expansion}>
        <Tabs keepMounted defaultValue="readable">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <TabsList aria-label="Response format" className="h-auto flex-wrap">
              <TabsTrigger value="readable">Readable</TabsTrigger>
              <TabsTrigger value="raw">
                <Braces className="size-3.5" aria-hidden="true" />
                Raw
              </TabsTrigger>
              {streaming && (
                <TabsTrigger value="timeline">Event timeline</TabsTrigger>
              )}
            </TabsList>
            <ExpandControls
              onChange={(open) =>
                setExpansion((previous) => ({
                  open,
                  generation: previous.generation + 1,
                }))
              }
            />
          </div>
          <TabsContent value="readable" className="space-y-6">
            {view.warnings.map((warning) => (
              <p
                key={warning}
                role="status"
                className="rounded-md border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900"
              >
                {warning}
              </p>
            ))}
            {turns.length ? (
              <Transcript turns={turns} label="Response output" />
            ) : (
              <p className="rounded-md border border-dashed p-5 text-sm text-muted-foreground">
                No readable output was found. Open Raw
                {streaming ? " or the event timeline" : ""} to see the stored
                response.
              </p>
            )}
            {view.usage !== undefined && (
              <section aria-label="Token usage" className="space-y-2">
                <h4 className="text-sm font-semibold">Token usage</h4>
                {usage.length > 0 && (
                  <dl className="flex flex-wrap gap-x-8 gap-y-3 rounded-md border bg-card px-4 py-3">
                    {usage.map(([label, value]) => (
                      <div
                        key={label}
                        className="flex min-w-0 flex-col-reverse"
                      >
                        <dt className="text-xs text-muted-foreground">
                          {label}
                        </dt>
                        <dd className="text-lg font-semibold tabular-nums [overflow-wrap:anywhere]">
                          {value}
                        </dd>
                      </div>
                    ))}
                  </dl>
                )}
                <p className="text-xs text-muted-foreground">
                  Provider-reported usage from the original recording. Edits do
                  not recalculate token counts.
                </p>
              </section>
            )}
            <Disclosure
              summary={
                <span className="text-sm font-semibold">Response details</span>
              }
              meta={`${Object.keys(view.metadata).length} fields`}
            >
              <div className="pt-1">
                <FieldList fields={view.metadata} />
              </div>
            </Disclosure>
          </TabsContent>
          <TabsContent value="raw">
            <RawPanel
              text={raw}
              label="Raw response"
              description={`Stored ${streaming ? "ordered SSE frames" : "JSON body"}, preserving number precision and string content.`}
            />
          </TabsContent>
          {streaming && (
            <TabsContent value="timeline" className="space-y-3">
              <p className="text-xs text-muted-foreground">
                Each event with its capture time relative to the upstream
                request. The bar shows where it falls in the stream. Edited
                revisions may have adjusted times, and replay timing controls
                may change playback delays.
              </p>
              <ol
                aria-label="Stream events"
                className="divide-y rounded-md border bg-card"
              >
                {view.frames.slice(0, eventLimit).map((frame, i) => (
                  <li
                    key={i}
                    className="grid min-w-0 grid-cols-[5rem_minmax(0,1fr)] gap-x-3 px-3 py-1.5"
                  >
                    <div className="pt-0.5">
                      <span className="block text-right text-xs tabular-nums text-muted-foreground">
                        +{frame.offset_ms} ms
                      </span>
                      <span
                        aria-hidden="true"
                        className="mt-1 block h-0.5 rounded-full bg-muted"
                      >
                        <span
                          className="block h-full rounded-full bg-primary/60"
                          style={{
                            width: `${Math.max(2, (frame.offset_ms / lastOffset) * 100)}%`,
                          }}
                        />
                      </span>
                    </div>
                    <Disclosure
                      summary={
                        <span className="min-w-0 break-all font-mono text-xs">
                          {frame.name}
                        </span>
                      }
                      meta={formatBytes(frame.data.length)}
                    >
                      <div className="space-y-2 pb-1 pl-5">
                        {frame.error && (
                          <p className="text-xs text-amber-800">
                            {frame.error}
                          </p>
                        )}
                        {frame.value !== undefined && (
                          <JsonTree value={frame.value} label="Event fields" />
                        )}
                        <Disclosure
                          summary={
                            <span className="text-xs">Raw SSE frame</span>
                          }
                        >
                          <pre
                            tabIndex={0}
                            className="max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted/50 p-2 font-mono text-xs leading-5"
                          >
                            {frame.data}
                          </pre>
                        </Disclosure>
                      </div>
                    </Disclosure>
                  </li>
                ))}
              </ol>
              {view.frames.length > eventLimit && (
                <Button
                  variant="outline"
                  onClick={() => setEventLimit((limit) => limit + EVENT_PAGE)}
                >
                  Show next{" "}
                  {Math.min(EVENT_PAGE, view.frames.length - eventLimit)} events
                  ({eventLimit} of {view.frames.length} shown)
                </Button>
              )}
            </TabsContent>
          )}
        </Tabs>
      </ExpansionProvider>
    </section>
  );
}
