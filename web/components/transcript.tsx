"use client";

import { createContext, useContext, useEffect, useMemo, useState } from "react";
import {
  Ban,
  Brain,
  Check,
  ChevronRight,
  Copy,
  CornerDownRight,
  FileText,
  ImageIcon,
  Wrench,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { splitFences } from "@/lib/fences";
import {
  formatBytes,
  type Part,
  type Role,
  type Turn,
} from "@/lib/request-viewer";
import { exactJSON, JSONNumber, type JSONValue } from "@/lib/response-viewer";
import { cn } from "@/lib/utils";

/** Expand all / collapse all broadcasts a new generation to every disclosure. */
export type Expansion = { open: boolean | null; generation: number };
const ExpansionContext = createContext<Expansion>({
  open: null,
  generation: 0,
});
export const ExpansionProvider = ExpansionContext.Provider;
// Expand all stops cascading here so huge payloads stay responsive.
const EXPAND_ALL_DEPTH = 3;
const ENTRY_PAGE = 100;
const TURN_PAGE = 150;
const LONG_STRING = 600;
const LONG_TEXT = 2400;

function useDisclosure(defaultOpen: boolean, follow = true) {
  const expansion = useContext(ExpansionContext);
  const [open, setOpen] = useState(defaultOpen);
  useEffect(() => {
    if (follow && expansion.open !== null) setOpen(expansion.open);
  }, [expansion, follow]);
  return [open, setOpen] as const;
}

export function ExpandControls({
  onChange,
}: {
  onChange: (open: boolean) => void;
}) {
  return (
    <div className="flex gap-1">
      <Button variant="ghost" size="sm" onClick={() => onChange(true)}>
        Expand all
      </Button>
      <Button variant="ghost" size="sm" onClick={() => onChange(false)}>
        Collapse all
      </Button>
    </div>
  );
}

export function Chip({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex max-w-full items-center gap-1 rounded-md bg-muted px-2 py-0.5 text-xs text-foreground [overflow-wrap:anywhere]",
        className,
      )}
    >
      {children}
    </span>
  );
}

export function Disclosure({
  summary,
  meta,
  defaultOpen = false,
  follow = true,
  className,
  children,
}: {
  summary: React.ReactNode;
  meta?: React.ReactNode;
  defaultOpen?: boolean;
  follow?: boolean;
  className?: string;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useDisclosure(defaultOpen, follow);
  return (
    <details
      open={open}
      onToggle={(event) => setOpen(event.currentTarget.open)}
      className={cn(
        "min-w-0 [&[open]>summary>svg:first-child]:rotate-90",
        className,
      )}
    >
      <summary className="flex cursor-pointer list-none items-center gap-1.5 rounded-md py-0.5 outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <ChevronRight
          aria-hidden="true"
          className="size-3.5 shrink-0 text-muted-foreground transition-transform"
        />
        {summary}
        {meta !== undefined && (
          <span className="ml-auto shrink-0 pl-2 text-xs text-muted-foreground">
            {meta}
          </span>
        )}
      </summary>
      {open && <div className="min-w-0 pt-1">{children}</div>}
    </details>
  );
}

function ShowAll({ hidden, onClick }: { hidden: string; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="ml-1 rounded-sm font-sans text-xs font-medium text-primary underline-offset-2 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring"
    >
      Show all {hidden}
    </button>
  );
}

function LongString({ value }: { value: string }) {
  const [full, setFull] = useState(false);
  const inline = /^data:([^;,]*)(;base64)?,/.exec(value);
  if (!full && inline)
    return (
      <span className="text-muted-foreground">
        {inline[0]}…
        <ShowAll
          hidden={`(${formatBytes(value.length)})`}
          onClick={() => setFull(true)}
        />
      </span>
    );
  if (full || value.length <= LONG_STRING) return <>{value}</>;
  return (
    <>
      {value.slice(0, LONG_STRING / 2)}…
      <ShowAll
        hidden={`(${formatBytes(new TextEncoder().encode(value).length)})`}
        onClick={() => setFull(true)}
      />
    </>
  );
}

function Scalar({ value }: { value: JSONValue }) {
  if (value instanceof JSONNumber)
    return <span className="break-all text-primary">{value.source}</span>;
  if (value === null || typeof value === "boolean")
    return <span className="text-muted-foreground">{String(value)}</span>;
  if (typeof value === "number")
    return <span className="text-primary">{String(value)}</span>;
  if (typeof value === "string")
    return (
      <span className="whitespace-pre-wrap break-words [overflow-wrap:anywhere]">
        {value === "" ? (
          <span className="text-muted-foreground">empty string</span>
        ) : (
          <LongString value={value} />
        )}
      </span>
    );
  return (
    <span className="text-muted-foreground">
      {Array.isArray(value) ? "[]" : "{}"}
    </span>
  );
}

const isContainer = (value: JSONValue) =>
  value !== null &&
  typeof value === "object" &&
  !(value instanceof JSONNumber) &&
  Object.keys(value).length > 0;

/** A compact field tree that keeps exact numbers and clamps huge strings. */
export function JsonTree({
  value,
  label,
  depth = 0,
  defaultOpen,
}: {
  value: JSONValue;
  label?: string;
  depth?: number;
  defaultOpen?: boolean;
}) {
  if (!isContainer(value))
    return (
      <div className="font-mono text-xs leading-5">
        <Scalar value={value} />
      </div>
    );
  // Very deep values fall back to their exact text rather than more nesting.
  if (depth >= 8)
    return (
      <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all font-mono text-xs">
        {exactJSON(value)}
      </pre>
    );
  const entries = Object.entries(value as object) as [string, JSONValue][];
  const isArray = Array.isArray(value);
  return (
    <Disclosure
      defaultOpen={defaultOpen ?? depth === 0}
      follow={depth < EXPAND_ALL_DEPTH}
      summary={
        <span className="min-w-0 break-all font-mono text-xs">
          {label ?? (isArray ? "Items" : "Fields")}
          <span className="ml-1.5 text-muted-foreground">
            {isArray ? `[${entries.length}]` : `{${entries.length}}`}
          </span>
        </span>
      }
    >
      <JsonEntries entries={entries} isArray={isArray} depth={depth} />
    </Disclosure>
  );
}

function JsonEntries({
  entries,
  isArray,
  depth,
}: {
  entries: [string, JSONValue][];
  isArray: boolean;
  depth: number;
}) {
  const [limit, setLimit] = useState(ENTRY_PAGE);
  const hidden = entries.length - limit;
  return (
    <ul className="ml-[6px] space-y-0.5 border-l pl-3">
      {entries.slice(0, limit).map(([key, child]) => (
        <li key={key} className="min-w-0">
          {isContainer(child) ? (
            <JsonTree value={child} label={key} depth={depth + 1} />
          ) : (
            <div className="flex min-w-0 gap-2 font-mono text-xs leading-5">
              <span
                className={cn(
                  "shrink-0",
                  isArray ? "text-muted-foreground" : "text-foreground/70",
                )}
              >
                {key}
              </span>
              <span className="min-w-0">
                <Scalar value={child} />
              </span>
            </div>
          )}
        </li>
      ))}
      {hidden > 0 && (
        <li>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setLimit((current) => current + ENTRY_PAGE)}
          >
            Show {Math.min(ENTRY_PAGE, hidden)} more ({hidden} hidden)
          </Button>
        </li>
      )}
    </ul>
  );
}

/** Model and prompt text with fenced code separated; everything stays escaped. */
export function Prose({ text, muted }: { text: string; muted?: boolean }) {
  const [full, setFull] = useState(false);
  const long = text.length > LONG_TEXT;
  const shown = long && !full ? text.slice(0, LONG_TEXT) : text;
  const chunks = useMemo(() => splitFences(shown), [shown]);
  if (!text)
    return <p className="text-sm italic text-muted-foreground">Empty text</p>;
  return (
    <div
      className={cn(
        "max-w-[72ch] space-y-3 text-sm leading-6",
        muted && "text-muted-foreground",
      )}
    >
      {chunks.map((chunk, i) =>
        chunk.code ? (
          <div
            key={i}
            className="overflow-hidden rounded-md border bg-muted/50"
          >
            <div className="border-b px-3 py-1 text-xs text-muted-foreground">
              {chunk.lang || "Code"}
            </div>
            <pre className="max-h-96 overflow-auto p-3 font-mono text-xs leading-5">
              <code>{chunk.text}</code>
            </pre>
          </div>
        ) : (
          <p
            key={i}
            className="whitespace-pre-wrap break-words [overflow-wrap:anywhere]"
          >
            {chunk.text}
          </p>
        ),
      )}
      {long && !full && (
        <p>
          …
          <ShowAll
            hidden={`(${text.length.toLocaleString()} characters)`}
            onClick={() => setFull(true)}
          />
        </p>
      )}
    </div>
  );
}

function IdChip({ id }: { id: string }) {
  return (
    <code className="min-w-0 break-all rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
      {id}
    </code>
  );
}

function ImagePart({ part }: { part: Extract<Part, { kind: "image" }> }) {
  const [large, setLarge] = useState(false);
  const kind = part.mediaType?.replace(/^image\//, "").toUpperCase();
  const facts = [
    kind ? `${kind} image` : "Image",
    part.bytes !== undefined ? formatBytes(part.bytes) : "",
    part.detail ? `${part.detail} detail` : "",
  ].filter(Boolean);
  return (
    <figure className="min-w-0 space-y-1.5">
      {part.src ? (
        <button
          type="button"
          onClick={() => setLarge(!large)}
          aria-label={large ? "Show smaller image" : "Show larger image"}
          className="block rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {/* Inline data only: rendering it fetches nothing over the network. */}
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            src={part.src}
            alt={facts.join(", ")}
            className={cn(
              "rounded-md border bg-muted object-contain",
              large ? "max-h-[70vh] w-auto max-w-full" : "max-h-40 max-w-60",
            )}
          />
        </button>
      ) : (
        <div className="flex items-center gap-2 rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
          <ImageIcon className="size-4 shrink-0" aria-hidden="true" />
          {part.url
            ? "Remote image, not loaded here."
            : part.fileId
              ? "Image uploaded to the provider."
              : "Image that can't be previewed here."}
        </div>
      )}
      <figcaption className="text-xs text-muted-foreground [overflow-wrap:anywhere]">
        {facts.join(", ")}
        {part.url && <span className="block break-all">{part.url}</span>}
        {part.fileId && <span className="block break-all">{part.fileId}</span>}
      </figcaption>
    </figure>
  );
}

export function PartView({ part }: { part: Part }) {
  switch (part.kind) {
    case "text":
      return <Prose text={part.text} />;
    case "refusal":
      return (
        <div className="flex gap-2 text-sm text-destructive">
          <Ban className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          <div className="min-w-0">
            <p className="font-medium">Refused</p>
            <Prose text={part.text} />
          </div>
        </div>
      );
    case "image":
      return <ImagePart part={part} />;
    case "attachment":
      return (
        <div className="flex min-w-0 items-start gap-2 text-sm">
          <FileText
            className="mt-0.5 size-4 shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
          <div className="min-w-0">
            <p className="font-medium [overflow-wrap:anywhere]">{part.title}</p>
            <p className="text-xs text-muted-foreground [overflow-wrap:anywhere]">
              {[
                part.mediaType,
                part.bytes !== undefined ? formatBytes(part.bytes) : "",
                part.reference,
              ]
                .filter(Boolean)
                .join(", ") || "No details recorded"}
            </p>
          </div>
        </div>
      );
    case "reasoning":
      return (
        <Disclosure
          summary={
            <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
              <Brain className="size-3.5" aria-hidden="true" />
              Reasoning
            </span>
          }
          meta={
            part.text
              ? `${part.text.length.toLocaleString()} characters`
              : part.redacted
                ? "Redacted"
                : part.encrypted
                  ? "Encrypted"
                  : "Empty"
          }
        >
          <div className="border-l-2 border-muted pl-3">
            {part.text ? (
              <Prose text={part.text} muted />
            ) : (
              <p className="text-xs text-muted-foreground">
                {part.encrypted || part.redacted
                  ? "The provider sent this reasoning encrypted, so its text can't be shown."
                  : "No reasoning text was included."}
              </p>
            )}
          </div>
        </Disclosure>
      );
    case "tool_call":
      return (
        <div className="min-w-0 rounded-md border bg-card">
          <div className="flex min-w-0 flex-wrap items-center gap-2 border-b px-3 py-1.5">
            <Wrench
              className="size-3.5 shrink-0 text-warn"
              aria-hidden="true"
            />
            <code className="min-w-0 break-all font-mono text-[13px] font-medium">
              {part.name}
            </code>
            {part.id && <IdChip id={part.id} />}
          </div>
          <div className="px-3 py-2">
            {isContainer(part.arguments) ||
            typeof part.arguments === "string" ? (
              <JsonTree value={part.arguments} label="Arguments" defaultOpen />
            ) : (
              <p className="text-xs text-muted-foreground">No arguments</p>
            )}
          </div>
        </div>
      );
    case "tool_result":
      return (
        <div
          className={cn(
            "min-w-0 rounded-md border bg-card",
            part.isError && "border-destructive/30",
          )}
        >
          <div className="flex min-w-0 flex-wrap items-center gap-2 border-b px-3 py-1.5 text-sm">
            <CornerDownRight
              className={cn(
                "size-3.5 shrink-0",
                part.isError ? "text-destructive" : "text-warn",
              )}
              aria-hidden="true"
            />
            <span
              className={cn("font-medium", part.isError && "text-destructive")}
            >
              {part.isError ? "Error from" : "Result of"}
            </span>
            <code className="min-w-0 break-all font-mono text-[13px]">
              {part.name || "a tool call"}
            </code>
            {part.id && <IdChip id={part.id} />}
          </div>
          <div className="space-y-3 px-3 py-2">
            {part.parts.length ? (
              part.parts.map((child, i) => <PartView key={i} part={child} />)
            ) : (
              <p className="text-xs text-muted-foreground">No output</p>
            )}
          </div>
        </div>
      );
    case "data":
      return (
        <JsonTree
          value={part.value}
          label={part.title.charAt(0).toUpperCase() + part.title.slice(1)}
          defaultOpen={part.open ?? false}
        />
      );
  }
}

const roles: Record<Role, { label: string; dot: string }> = {
  instructions: { label: "Instructions", dot: "bg-muted-foreground" },
  system: { label: "System", dot: "bg-muted-foreground" },
  developer: { label: "Developer", dot: "bg-muted-foreground" },
  user: { label: "User", dot: "bg-primary" },
  assistant: { label: "Assistant", dot: "bg-foreground" },
  tool: { label: "Tool", dot: "bg-warn" },
};

function preview(parts: Part[]): string {
  for (const part of parts) {
    if (part.kind === "text" && part.text.trim())
      return part.text.replace(/\s+/g, " ").trim().slice(0, 160);
    if (part.kind === "tool_call") return `Calls ${part.name}`;
    if (part.kind === "tool_result")
      return `Result of ${part.name || "a tool call"}`;
    if (part.kind === "image") return "Image";
  }
  return `${parts.length} ${parts.length === 1 ? "part" : "parts"}`;
}

function TurnRow({
  turn,
  position,
  last,
}: {
  turn: Turn;
  position: number;
  last: boolean;
}) {
  const [open, setOpen] = useDisclosure(true);
  const role = roles[turn.role];
  return (
    <li className="relative grid gap-x-6 gap-y-2 pb-6 last:pb-0 sm:grid-cols-[8.5rem_minmax(0,1fr)]">
      {!last && (
        <span
          aria-hidden="true"
          className="absolute bottom-0 left-[4.5px] top-4 w-px bg-border"
        />
      )}
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="relative flex items-start gap-3 self-start rounded-md text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span
          aria-hidden="true"
          className={cn(
            "mt-1.5 size-2.5 shrink-0 rounded-full ring-4 ring-background",
            role.dot,
          )}
        />
        <span className="min-w-0">
          <span className="block text-sm font-semibold [overflow-wrap:anywhere]">
            {role.label}
            {turn.name && (
              <span className="font-normal text-muted-foreground">
                {" "}
                {turn.name}
              </span>
            )}
          </span>
          <span className="block text-xs tabular-nums text-muted-foreground">
            Turn {position}
          </span>
        </span>
      </button>
      <div className="min-w-0 pl-[22px] sm:pl-0">
        {open ? (
          <div className="space-y-3">
            {turn.parts.length ? (
              turn.parts.map((part, i) => <PartView key={i} part={part} />)
            ) : (
              <p className="text-sm italic text-muted-foreground">No content</p>
            )}
          </div>
        ) : (
          <p className="truncate pt-0.5 text-sm text-muted-foreground">
            {preview(turn.parts)}
          </p>
        )}
      </div>
    </li>
  );
}

/** A conversation as an ordered spine of turns. */
export function Transcript({ turns, label }: { turns: Turn[]; label: string }) {
  const [limit, setLimit] = useState(TURN_PAGE);
  const shown = turns.slice(0, limit);
  return (
    <div className="space-y-4">
      <ol aria-label={label} className="min-w-0">
        {shown.map((turn, i) => (
          <TurnRow
            key={i}
            turn={turn}
            position={i + 1}
            last={i === shown.length - 1}
          />
        ))}
      </ol>
      {turns.length > limit && (
        <Button
          variant="outline"
          onClick={() => setLimit((n) => n + TURN_PAGE)}
        >
          Show {Math.min(TURN_PAGE, turns.length - limit)} more turns ({limit}{" "}
          of {turns.length} shown)
        </Button>
      )}
    </div>
  );
}

/** Short values read inline; larger ones open as a tree. */
function FieldValue({ value }: { value: JSONValue }) {
  if (isContainer(value)) {
    // exactJSON escapes newlines inside strings, so this only joins structure.
    const compact = exactJSON(value).replace(/\n\s*/g, "");
    if (compact.length > 100)
      return <JsonTree value={value} label="Value" defaultOpen={false} />;
    return (
      <code className="break-all font-mono text-xs leading-5">{compact}</code>
    );
  }
  return <JsonTree value={value} />;
}

/** Top-level fields as label/value rows; nested values open as trees. */
export function FieldList({ fields }: { fields: Record<string, JSONValue> }) {
  const entries = Object.entries(fields);
  if (!entries.length)
    return <p className="text-sm text-muted-foreground">No other fields.</p>;
  return (
    <dl className="divide-y rounded-md border bg-card">
      {entries.map(([key, value]) => (
        <div
          key={key}
          className="grid min-w-0 gap-x-4 gap-y-1 px-3 py-2 sm:grid-cols-[minmax(8rem,14rem)_minmax(0,1fr)]"
        >
          <dt className="break-all font-mono text-xs leading-5 text-muted-foreground">
            {key}
          </dt>
          <dd className="min-w-0">
            <FieldValue value={value} />
          </dd>
        </div>
      ))}
    </dl>
  );
}

export function RawPanel({
  text,
  label,
  description,
}: {
  text: string;
  label: string;
  description: string;
}) {
  const [status, setStatus] = useState("");
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setStatus("Copied");
      setTimeout(() => setStatus(""), 1500);
    } catch {
      setStatus("Copy unavailable. Select the text to copy it.");
    }
  }
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">{description}</p>
        <Button size="sm" variant="outline" onClick={() => void copy()}>
          {status === "Copied" ? (
            <Check className="size-3.5" aria-hidden="true" />
          ) : (
            <Copy className="size-3.5" aria-hidden="true" />
          )}
          {status === "Copied" ? "Copied" : `Copy ${label.toLowerCase()}`}
        </Button>
      </div>
      {status && status !== "Copied" && (
        <p role="status" className="text-xs text-muted-foreground">
          {status}
        </p>
      )}
      <pre
        aria-label={label}
        tabIndex={0}
        className="max-h-[60vh] overflow-auto whitespace-pre-wrap break-all rounded-md border bg-muted/40 p-4 font-mono text-xs leading-5"
      >
        {text}
      </pre>
    </div>
  );
}
