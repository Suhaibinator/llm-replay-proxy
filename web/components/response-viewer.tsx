"use client";

import { useEffect, useMemo, useState } from "react";
import {
  Braces,
  ChevronRight,
  Copy,
  MessageSquare,
  Wrench,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  exactJSON,
  JSONNumber,
  responseView,
  type JSONValue,
  type ResponseBlock,
  type ResponseRevision,
} from "@/lib/response-viewer";

type Expansion = { open: boolean | null; generation: number };

function Section({
  title,
  description,
  children,
  defaultOpen = false,
  expansion,
}: {
  title: string;
  description?: string;
  children: React.ReactNode;
  defaultOpen?: boolean;
  expansion: Expansion;
}) {
  const [open, setOpen] = useState(defaultOpen);
  useEffect(() => {
    if (expansion.open !== null) setOpen(expansion.open);
  }, [expansion]);
  return (
    <details
      open={open}
      onToggle={(event) => setOpen(event.currentTarget.open)}
      className="rounded-lg border bg-background [&[open]>summary>svg]:rotate-90"
    >
      <summary className="flex cursor-pointer list-none items-center gap-2 rounded-lg p-3 text-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary [&::-webkit-details-marker]:hidden">
        <ChevronRight
          aria-hidden="true"
          className="size-4 shrink-0 transition-transform"
        />
        <span className="min-w-0 break-words">{title}</span>
        {description && (
          <span className="ml-auto shrink-0 text-xs font-normal text-muted-foreground">
            {description}
          </span>
        )}
      </summary>
      {open && <div className="min-w-0 border-t p-3">{children}</div>}
    </details>
  );
}

function JSONTree({
  value,
  expansion,
  label = "Fields",
  depth = 0,
}: {
  value: JSONValue;
  expansion: Expansion;
  label?: string;
  depth?: number;
}) {
  if (value instanceof JSONNumber)
    return (
      <code className="break-all text-xs text-primary">{value.source}</code>
    );
  if (value === null || typeof value !== "object") {
    return (
      <span className="whitespace-pre-wrap break-words text-sm [overflow-wrap:anywhere]">
        {typeof value === "string" ? value : String(value)}
      </span>
    );
  }
  // Avoid rendering unbounded/deep trees; the exact representation remains available.
  if (depth >= 8)
    return (
      <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">
        {exactJSON(value)}
      </pre>
    );
  const entries = Object.entries(value);
  return (
    <Section
      title={label}
      description={`${entries.length} ${Array.isArray(value) ? "items" : "fields"}`}
      expansion={expansion}
      defaultOpen={depth === 0}
    >
      {entries.length === 0 ? (
        <code className="text-xs text-muted-foreground">
          {Array.isArray(value) ? "[]" : "{}"}
        </code>
      ) : (
        <dl className="space-y-3">
          {entries.map(([key, child]) => (
            <div key={key} className="min-w-0">
              <dt className="mb-1 break-all font-mono text-xs font-medium text-muted-foreground">
                {Array.isArray(value) ? `[${key}]` : key}
              </dt>
              <dd className="min-w-0 border-l-2 border-muted pl-3">
                <JSONTree
                  value={child}
                  label={key}
                  depth={depth + 1}
                  expansion={expansion}
                />
              </dd>
            </div>
          ))}
        </dl>
      )}
    </Section>
  );
}

function AnswerText({ text }: { text: string }) {
  if (!text)
    return <p className="text-sm italic text-muted-foreground">Empty text</p>;
  // Fenced code is separated for reading; all content stays escaped React text.
  // Never execute HTML or automatically fetch image URLs supplied by a model.
  const chunks = text.split(/(```[^\n]*\n[\s\S]*?```)/g);
  return (
    <div className="space-y-3 text-sm leading-7">
      {chunks.map((chunk, i) => {
        const fence = chunk.match(/^```([^\n]*)\n([\s\S]*?)```$/);
        return fence ? (
          <div
            key={i}
            className="overflow-hidden rounded-lg border bg-muted/50"
          >
            <div className="border-b px-3 py-1 text-xs text-muted-foreground">
              {fence[1] || "Code"}
            </div>
            <pre className="max-h-96 overflow-auto p-3 font-mono text-xs leading-6">
              <code>{fence[2]}</code>
            </pre>
          </div>
        ) : (
          <p
            key={i}
            className="whitespace-pre-wrap break-words [overflow-wrap:anywhere]"
          >
            {chunk}
          </p>
        );
      })}
    </div>
  );
}

function OutputBlock({
  block,
  expansion,
}: {
  block: ResponseBlock;
  expansion: Expansion;
}) {
  const labels = {
    text: "Text",
    tool: "Tool call",
    result: "Tool result",
    reasoning: "Reasoning",
    refusal: "Refusal",
    other: "Output",
  };
  return (
    <Section
      title={block.title}
      description={labels[block.kind]}
      defaultOpen={["text", "tool", "result", "refusal"].includes(block.kind)}
      expansion={expansion}
    >
      <div className="space-y-3">
        {block.id && (
          <div className="text-xs text-muted-foreground">
            {block.kind === "tool" || block.kind === "result"
              ? "Call / item ID"
              : "ID"}
            : <code className="break-all text-foreground">{block.id}</code>
          </div>
        )}
        {block.text !== undefined && <AnswerText text={block.text} />}
        {block.arguments !== undefined && (
          <JSONTree
            label={block.kind === "result" ? "Result" : "Arguments / input"}
            value={block.arguments}
            expansion={expansion}
          />
        )}
        {block.kind === "reasoning" && !block.text && (
          <p className="text-xs text-muted-foreground">
            No readable reasoning text was included by the provider. Any
            recorded fields are available below.
          </p>
        )}
        {block.kind === "other" ? (
          <JSONTree value={block.raw} expansion={expansion} />
        ) : (
          <Section title="All fields" expansion={expansion}>
            <JSONTree value={block.raw} expansion={expansion} />
          </Section>
        )}
      </div>
    </Section>
  );
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
  const [expansion, setExpansion] = useState<Expansion>({
    open: null,
    generation: 0,
  });
  const [eventLimit, setEventLimit] = useState(100);
  const [copyStatus, setCopyStatus] = useState("");
  const raw = streaming
    ? revision.events.map((event) => event.data).join("")
    : revision.body;
  const toolCount = view.blocks.filter((block) => block.kind === "tool").length;
  const expand = (open: boolean) =>
    setExpansion((previous) => ({ open, generation: previous.generation + 1 }));
  async function copyRaw() {
    try {
      await navigator.clipboard.writeText(raw);
      setCopyStatus("Copied stored response");
    } catch {
      setCopyStatus("Copy unavailable. Select the raw response to copy it.");
    }
  }
  return (
    <section aria-label="Response browser" className="min-w-0 space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <MessageSquare className="size-4 text-primary" aria-hidden="true" />
        <h3 className="text-sm font-semibold">{view.protocol} response</h3>
        <Badge className="normal-case tracking-normal">
          {streaming ? `${revision.events.length} SSE events` : "JSON"}
        </Badge>
        {toolCount > 0 && (
          <Badge className="gap-1 normal-case tracking-normal">
            <Wrench className="size-3" aria-hidden="true" />
            {toolCount} tool {toolCount === 1 ? "call" : "calls"}
          </Badge>
        )}
      </div>
      <Tabs defaultValue="readable">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <TabsList aria-label="Response format">
            <TabsTrigger value="readable">Readable</TabsTrigger>
            <TabsTrigger value="raw">
              <Braces className="mr-1.5 size-3.5" aria-hidden="true" />
              Raw
            </TabsTrigger>
            {streaming && (
              <TabsTrigger value="timeline">Event timeline</TabsTrigger>
            )}
          </TabsList>
          <div className="flex gap-1">
            <Button variant="ghost" size="sm" onClick={() => expand(true)}>
              Expand all
            </Button>
            <Button variant="ghost" size="sm" onClick={() => expand(false)}>
              Collapse all
            </Button>
          </div>
        </div>
        <TabsContent value="readable" className="space-y-3">
          {view.warnings.map((warning) => (
            <p
              key={warning}
              role="status"
              className="rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900"
            >
              {warning}
            </p>
          ))}
          {view.blocks.length ? (
            view.blocks.map((block, i) => (
              <OutputBlock
                key={`${i}-${block.kind}`}
                block={block}
                expansion={expansion}
              />
            ))
          ) : (
            <p className="rounded-lg border border-dashed p-5 text-sm text-muted-foreground">
              No readable output blocks were found. Inspect Raw
              {streaming ? " or the event timeline" : ""} for the stored
              response.
            </p>
          )}
          {view.usage !== undefined && (
            <Section title="Usage · historical" expansion={expansion}>
              <p className="mb-3 text-xs text-muted-foreground">
                Provider-reported usage from the original recording. Edits do
                not recalculate token counts.
              </p>
              <JSONTree
                value={view.usage}
                label="Recorded usage"
                expansion={expansion}
              />
            </Section>
          )}
          <Section title="Response metadata" expansion={expansion}>
            <JSONTree value={view.metadata} expansion={expansion} />
          </Section>
        </TabsContent>
        <TabsContent value="raw" className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-xs text-muted-foreground">
              Stored {streaming ? "ordered SSE frames" : "JSON body"},
              preserving number precision and string content.
            </p>
            <Button size="sm" variant="outline" onClick={() => void copyRaw()}>
              <Copy className="size-3.5" aria-hidden="true" />
              Copy raw response
            </Button>
          </div>
          {copyStatus && (
            <p role="status" className="text-xs text-muted-foreground">
              {copyStatus}
            </p>
          )}
          <pre
            aria-label="Raw response"
            tabIndex={0}
            className="max-h-[60vh] overflow-auto rounded-lg border bg-muted/40 p-4 font-mono text-xs leading-6"
          >
            {raw}
          </pre>
        </TabsContent>
        {streaming && (
          <TabsContent value="timeline" className="space-y-2">
            <p className="text-xs text-muted-foreground">
              Ordered frames with capture offsets relative to the upstream
              request. Edited revisions may have adjusted offsets. Replay timing
              controls may change playback delays.
            </p>
            {view.frames.slice(0, eventLimit).map((frame, i) => (
              <Section
                key={i}
                title={`${i + 1}. ${frame.name}`}
                description={`+${frame.offset_ms} ms`}
                expansion={expansion}
              >
                <div className="space-y-3">
                  {frame.error && (
                    <p className="text-xs text-amber-700">{frame.error}</p>
                  )}
                  {frame.value !== undefined && (
                    <JSONTree
                      value={frame.value}
                      expansion={expansion}
                      label="Event fields"
                    />
                  )}
                  <Section title="Raw SSE frame" expansion={expansion}>
                    <pre
                      tabIndex={0}
                      className="max-h-80 overflow-auto text-xs leading-6"
                    >
                      {frame.data}
                    </pre>
                  </Section>
                </div>
              </Section>
            ))}
            {view.frames.length > eventLimit && (
              <Button
                variant="outline"
                onClick={() => setEventLimit((limit) => limit + 100)}
              >
                Show next {Math.min(100, view.frames.length - eventLimit)}{" "}
                events ({eventLimit} of {view.frames.length} shown)
              </Button>
            )}
          </TabsContent>
        )}
      </Tabs>
    </section>
  );
}
