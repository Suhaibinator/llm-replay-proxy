"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  Archive,
  Braces,
  Check,
  Copy,
  FileDiff,
  Pencil,
  RotateCcw,
  Trash2,
  XCircle,
} from "lucide-react";
import {
  type Diff,
  type Entry,
  type History,
  type Revision,
  api,
  date,
  editableRevision,
  pretty,
  shortKey,
} from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ResponseViewer } from "@/components/response-viewer";
import { RequestViewer } from "@/components/request-viewer";
import { CodeBlock, ErrorBanner, Spinner } from "@/components/common";

const message = (e: unknown) =>
  e instanceof Error ? e.message : "Something went wrong";

/**
 * The recording inspector dialog. It is open while `recordingId` is set and
 * loads, edits, restores, compares and deletes that recording itself; the
 * shell only decides which recording is open and learns when one changed.
 */
export function Inspector({
  recordingId,
  onClose,
  onChanged,
  notify,
  returnFocus,
}: {
  /** 0 or null when closed. */
  recordingId: number | null;
  onClose: () => void;
  /** Called after a revision changed or the recording was deleted. */
  onChanged: () => void | Promise<void>;
  notify: (text: string) => void;
  returnFocus?: () => void;
}) {
  const open = !!recordingId;
  const [entry, setEntry] = useState<Entry | null>(null);
  const [error, setError] = useState("");
  const [editText, setEditText] = useState("");
  const [advanced, setAdvanced] = useState("");
  const [compare, setCompare] = useState<Diff[] | null>(null);
  const [saving, setSaving] = useState(false);
  const [misses, setMisses] = useState<History[]>([]);
  const version = useRef(0);
  const compareVersion = useRef(0);
  // Recording shown in the open inspector (0 when closed).
  const shown = useRef(0);
  const fail = (e: unknown) => setError(message(e));

  const load = useCallback(async (id: number, refresh = false) => {
    const v = ++version.current;
    if (!refresh) {
      shown.current = id;
      compareVersion.current += 1;
      setError("");
      setEntry(null);
      setCompare(null);
      setMisses([]);
    }
    try {
      const e = await api<Entry>(`/api/recordings/${id}?revision_details=lazy`);
      if (v !== version.current) return;
      setEntry(e);
      setEditText(e.text || "");
      setAdvanced(pretty(editableRevision(e.revision)));
      if (!refresh) {
        // Misses in this collection feed the compare picker.
        void api<History[]>(
          `/api/history?collection_id=${e.recording.collection_id}`,
        )
          .then((rows) => {
            if (v === version.current)
              setMisses(rows.filter((h) => /miss/i.test(h.outcome)));
          })
          .catch(() => undefined);
      }
    } catch (e) {
      if (v === version.current) fail(e);
    }
  }, []);

  useEffect(() => {
    if (recordingId) void load(recordingId);
    else {
      version.current += 1;
      shown.current = 0;
    }
  }, [recordingId, load]);

  async function afterRevisionChange(id: number) {
    // Refresh even if the dialog was reopened meanwhile: that GET may predate
    // the new revision and would leave a stale base_revision_id.
    if (shown.current === id) await load(id, true);
    await onChanged();
  }
  async function edit(kind: "text" | "advanced") {
    if (!entry || saving) return;
    setSaving(true);
    setError("");
    try {
      let body: string;
      if (kind === "text") {
        body = JSON.stringify({
          text: editText,
          base_revision_id: entry.revision.id,
        });
      } else {
        try {
          JSON.parse(advanced);
        } catch (e) {
          throw new Error(`Revision must be valid JSON: ${message(e)}`);
        }
        body = `{"revision":${advanced},"base_revision_id":${entry.revision.id}}`;
      }
      await api(`/api/recordings/${entry.recording.id}/edit`, {
        method: "POST",
        body,
      });
      notify("New revision activated");
      await afterRevisionChange(entry.recording.id);
    } catch (e) {
      fail(e);
    } finally {
      setSaving(false);
    }
  }
  async function restore(revisionId: number) {
    if (!entry || saving) return;
    setSaving(true);
    setError("");
    try {
      await api(`/api/recordings/${entry.recording.id}/restore`, {
        method: "POST",
        body: JSON.stringify({
          revision_id: revisionId,
          base_revision_id: entry.revision.id,
        }),
      });
      notify("Revision restored");
      await afterRevisionChange(entry.recording.id);
    } catch (e) {
      fail(e);
    } finally {
      setSaving(false);
    }
  }
  async function remove() {
    if (!entry || saving) return;
    setSaving(true);
    setError("");
    try {
      await api(`/api/recordings/${entry.recording.id}`, { method: "DELETE" });
      version.current += 1;
      shown.current = 0;
      onClose();
      notify("Recording deleted");
      await onChanged();
    } catch (e) {
      fail(e);
    } finally {
      setSaving(false);
    }
  }
  async function compareRequest(raw: string, historyId?: number) {
    if (!entry) return;
    const v = ++compareVersion.current;
    setError("");
    try {
      let body: string;
      if (historyId) {
        body = JSON.stringify({
          history_id: historyId,
          recording_id: entry.recording.id,
        });
      } else {
        JSON.parse(raw);
        body = `{"request":${raw},"recording_id":${entry.recording.id}}`;
      }
      const r = await api<{ differences: Diff[] }>("/api/compare", {
        method: "POST",
        body,
      });
      if (v === compareVersion.current) setCompare(r.differences);
    } catch (e) {
      if (v !== compareVersion.current) return;
      setCompare(null);
      fail(
        e instanceof SyntaxError ? new Error("Request must be valid JSON") : e,
      );
    }
  }
  const clearCompare = useCallback(() => {
    compareVersion.current += 1;
    setCompare(null);
  }, []);

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent
        className="max-w-5xl"
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          returnFocus?.();
        }}
      >
        {!entry ? (
          error ? (
            <div role="alert" className="space-y-4 py-10 text-center">
              <XCircle className="mx-auto size-7 text-error" aria-hidden />
              <div>
                <p className="font-medium">Could not load this recording</p>
                <p className="mt-1 text-sm text-muted-foreground">{error}</p>
              </div>
              <Button variant="outline" onClick={onClose}>
                Close
              </Button>
            </div>
          ) : (
            <>
              <DialogTitle className="sr-only">Recording</DialogTitle>
              <Spinner label="Loading recording" className="h-48" />
            </>
          )
        ) : (
          <InspectorBody
            key={entry.recording.id}
            entry={entry}
            error={error}
            clearError={() => setError("")}
            editText={editText}
            setEditText={setEditText}
            advanced={advanced}
            setAdvanced={setAdvanced}
            edit={edit}
            restore={restore}
            remove={remove}
            compare={compare}
            compareRequest={compareRequest}
            clearCompare={clearCompare}
            misses={misses}
            saving={saving}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

function InspectorBody({
  entry,
  error,
  clearError,
  editText,
  setEditText,
  advanced,
  setAdvanced,
  edit,
  restore,
  remove,
  compare,
  compareRequest,
  clearCompare,
  misses,
  saving,
}: {
  entry: Entry;
  error: string;
  clearError: () => void;
  editText: string;
  setEditText: (x: string) => void;
  advanced: string;
  setAdvanced: (x: string) => void;
  edit: (k: "text" | "advanced") => void;
  restore: (id: number) => void;
  remove: () => void;
  compare: Diff[] | null;
  compareRequest: (raw: string, historyId?: number) => void;
  clearCompare: () => void;
  misses: History[];
  saving: boolean;
}) {
  const [compareRaw, setCompareRaw] = useState("");
  const [chosenHistoryId, setHistoryId] = useState(0);
  const recordingId = entry.recording.id;
  const [compareFor, setCompareFor] = useState(recordingId);
  if (recordingId !== compareFor) {
    setCompareFor(recordingId);
    setCompareRaw("");
    setHistoryId(0);
  }
  const historyId = misses.some((h) => h.id === chosenHistoryId)
    ? chosenHistoryId
    : 0;
  // The chosen miss left the list: drop its result with the selection.
  useEffect(() => {
    if (chosenHistoryId && !historyId && misses.length) {
      setHistoryId(0);
      clearCompare();
    }
  }, [chosenHistoryId, historyId, misses.length, clearCompare]);
  const revisions = entry.revision_summaries ||
    entry.revisions || [entry.revision];
  return (
    <>
      <DialogHeader>
        <div className="flex flex-wrap items-center gap-2">
          <DialogTitle className="font-mono text-[15px]">
            {entry.recording.route}
          </DialogTitle>
          <Badge>{entry.recording.streaming ? "SSE stream" : "JSON"}</Badge>
          <Badge tone={entry.revision.status < 400 ? "hit" : "error"}>
            HTTP {entry.revision.status}
          </Badge>
          {revisions.length > 1 && (
            <Badge tone="neutral">{revisions.length} revisions</Badge>
          )}
        </div>
        <DialogDescription className="break-all font-mono text-xs">
          Recording {entry.recording.id} · key {entry.recording.key}
        </DialogDescription>
      </DialogHeader>
      <ErrorBanner message={error} onDismiss={clearError} className="mb-4" />
      <Tabs keepMounted defaultValue="response">
        <TabsList className="h-auto flex-wrap">
          <TabsTrigger value="response">Response</TabsTrigger>
          <TabsTrigger value="request">Request</TabsTrigger>
          <TabsTrigger value="inspect">Raw</TabsTrigger>
          <TabsTrigger value="text">Plain text</TabsTrigger>
          <TabsTrigger value="events">Advanced</TabsTrigger>
          <TabsTrigger value="revisions">Revisions</TabsTrigger>
          <TabsTrigger value="compare">Compare</TabsTrigger>
        </TabsList>
        <TabsContent value="response">
          <ResponseViewer
            key={entry.revision.id}
            route={entry.recording.route}
            streaming={entry.recording.streaming}
            revision={entry.revision}
          />
        </TabsContent>
        <TabsContent value="request">
          <RequestViewer
            route={entry.recording.route}
            raw={entry.request_text ?? ""}
          />
        </TabsContent>
        <TabsContent value="inspect" className="grid gap-4 lg:grid-cols-2">
          <Data
            title="Original request"
            value={null}
            raw={entry.request_text}
          />
          <Data
            title="Matching input"
            value={null}
            raw={entry.matching_input_text}
          />
          <Data title="Response headers" value={entry.revision.headers} />
          <Data
            title={
              entry.recording.streaming
                ? `${entry.revision.events.length} ordered events`
                : "Response body"
            }
            value={
              entry.recording.streaming
                ? entry.revision.events
                : entry.revision.body
            }
            raw={entry.recording.streaming ? undefined : entry.revision.body}
          />
        </TabsContent>
        <TabsContent value="text">
          <div className="mb-3">
            <p className="text-sm font-medium">Assistant answer</p>
            <p className="text-xs text-muted-foreground">
              Rewrites the answer text inside the recorded response while
              keeping every event's identity and order.
            </p>
          </div>
          {entry.text_unavailable_reason ? (
            <div className="rounded-md border border-warn/40 bg-warn/10 p-4 text-sm">
              <p className="font-semibold">Plain-text editing unavailable</p>
              <p className="mt-1">{entry.text_unavailable_reason}</p>
              <p className="mt-2 text-xs text-muted-foreground">
                Use the Advanced editor for tool calls, reasoning or multimodal
                output.
              </p>
            </div>
          ) : (
            <>
              <Textarea
                aria-label="Assistant answer"
                className="min-h-72 resize-y leading-6"
                value={editText}
                onChange={(e) => setEditText(e.target.value)}
              />
              <div className="mt-3 flex items-center justify-between gap-3">
                <p className="text-xs text-muted-foreground">
                  Token usage stays as the provider reported it.
                </p>
                <Button disabled={saving} onClick={() => edit("text")}>
                  <Pencil className="size-4" />
                  Save new revision
                </Button>
              </div>
            </>
          )}
        </TabsContent>
        <TabsContent value="events">
          <div className="mb-3">
            <p className="text-sm font-medium">Revision JSON</p>
            <p className="text-xs text-muted-foreground">
              Validation checks JSON, event structure, completion and final
              output agreement before the revision is activated.
            </p>
          </div>
          <Textarea
            aria-label="Revision JSON"
            spellCheck={false}
            className="code min-h-96 resize-y whitespace-pre text-xs leading-5"
            value={advanced}
            onChange={(e) => setAdvanced(e.target.value)}
          />
          <div className="mt-3 flex justify-end">
            <Button disabled={saving} onClick={() => edit("advanced")}>
              <Braces className="size-4" />
              Validate & activate
            </Button>
          </div>
        </TabsContent>
        <TabsContent value="revisions">
          <ol className="space-y-2">
            {revisions.map((r) => {
              const active = r.id === entry.recording.active_revision_id;
              return (
                <li key={r.id} className="rounded-md border p-3">
                  <div className="flex items-center gap-3">
                    <Archive
                      className="size-4 shrink-0 text-muted-foreground"
                      aria-hidden
                    />
                    <div className="min-w-0 flex-1">
                      <p className="text-sm font-medium">
                        Revision {r.id} · {r.source}
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {date(r.created_at)} · HTTP {r.status}
                        {active ? " · active" : ""}
                      </p>
                    </div>
                    {active ? (
                      <Badge tone="hit">Active</Badge>
                    ) : (
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={saving}
                        onClick={() => restore(r.id)}
                      >
                        <RotateCcw className="size-3.5" />
                        Restore
                      </Button>
                    )}
                  </div>
                  <RevisionDetails
                    recordingId={recordingId}
                    revisionId={r.id}
                    initial={
                      active
                        ? entry.revision
                        : entry.revisions?.find((v) => v.id === r.id)
                    }
                  />
                </li>
              );
            })}
          </ol>
          <div className="mt-6 border-t pt-4">
            <ConfirmAction
              label="Delete recording"
              prompt={`Delete this recording and ${
                revisions.length === 1
                  ? "its revision"
                  : `all ${revisions.length} revisions`
              }? Replay will miss this request until it is recorded again.`}
              disabled={saving}
              onConfirm={remove}
            />
          </div>
        </TabsContent>
        <TabsContent value="compare">
          <div className="space-y-3 rounded-md border p-4">
            <div>
              <p className="text-sm font-medium">Compare a missed request</p>
              <p className="text-xs text-muted-foreground">
                Pick a stored miss to keep exact numeric precision, or paste
                request JSON. Differences point at fields to exclude from
                matching.
              </p>
            </div>
            {misses.length > 0 && (
              <Select
                value={String(historyId)}
                onValueChange={(v) => {
                  setHistoryId(Number(v));
                  clearCompare();
                }}
              >
                <SelectTrigger aria-label="Missed request">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent className="max-w-[var(--radix-select-trigger-width)]">
                  <SelectItem value="0">Paste request JSON instead</SelectItem>
                  {misses.map((h) => (
                    <SelectItem key={h.id} value={String(h.id)}>
                      {date(h.created_at)} · {h.route} · {shortKey(h.key)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            {!historyId && (
              <Textarea
                aria-label="Request JSON"
                spellCheck={false}
                className="code min-h-36 text-xs"
                value={compareRaw}
                onChange={(e) => {
                  setCompareRaw(e.target.value);
                  if (compare) clearCompare();
                }}
                placeholder='{"model":"…","messages":[]}'
              />
            )}
            <div className="flex justify-end">
              <Button
                variant="outline"
                disabled={!historyId && !compareRaw.trim()}
                onClick={() =>
                  compareRequest(compareRaw, historyId || undefined)
                }
              >
                <FileDiff className="size-4" />
                Compare
              </Button>
            </div>
          </div>
          {compare && (
            <div className="mt-3 overflow-x-auto">
              <table className="w-full text-left text-xs">
                <thead>
                  <tr className="border-b text-muted-foreground">
                    <th className="p-2 font-medium">JSON path</th>
                    <th className="p-2 font-medium">Missed request</th>
                    <th className="p-2 font-medium">Recording</th>
                  </tr>
                </thead>
                <tbody>
                  {compare.length ? (
                    compare.map((d, i) => (
                      <tr className="border-b" key={`${d.path}${i}`}>
                        <td className="p-2 font-mono">{d.path || "/"}</td>
                        <td className="p-2 font-mono text-miss">
                          {d.request_exists ? d.request_display : "(missing)"}
                        </td>
                        <td className="p-2 font-mono text-hit">
                          {d.recorded_exists ? d.recorded_display : "(missing)"}
                        </td>
                      </tr>
                    ))
                  ) : (
                    <tr>
                      <td
                        colSpan={3}
                        className="p-8 text-center text-muted-foreground"
                      >
                        No differences found.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          )}
        </TabsContent>
      </Tabs>
    </>
  );
}

// Keep historical payloads out of the initial inspector request and render.
// A keyed parent unmounts these readers on recording changes; late requests
// are ignored after unmount. Immutable details may be reused after collapse.
function RevisionDetails({
  recordingId,
  revisionId,
  initial,
}: {
  recordingId: number;
  revisionId: number;
  initial?: Revision;
}) {
  const [open, setOpen] = useState(false);
  const [revision, setRevision] = useState<Revision | undefined>(initial);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const detail = initial || revision;
  const live = useRef(true);
  const pending = useRef(false);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);
  async function load() {
    if (detail || pending.current) return;
    pending.current = true;
    setLoading(true);
    setError("");
    try {
      const result = await api<Revision>(
        `/api/recordings/${recordingId}/revisions/${revisionId}`,
      );
      if (live.current) setRevision(result);
    } catch (e) {
      if (live.current) setError(message(e));
    } finally {
      pending.current = false;
      if (live.current) setLoading(false);
    }
  }
  return (
    <details
      className="mt-2 border-t pt-2 text-xs"
      onToggle={(event) => {
        const expanded = event.currentTarget.open;
        setOpen(expanded);
        if (expanded) void load();
      }}
    >
      <summary className="cursor-pointer rounded-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">
        Inspect revision data
      </summary>
      {open &&
        (detail ? (
          <CodeBlock className="mt-2 max-h-64">{pretty(detail)}</CodeBlock>
        ) : error ? (
          <div role="alert" className="mt-2">
            <p>{error}</p>
            <Button variant="outline" size="sm" onClick={() => void load()}>
              Retry
            </Button>
          </div>
        ) : loading ? (
          <Spinner label="Loading revision" className="mt-2" />
        ) : null)}
    </details>
  );
}

// ConfirmAction asks inline before a destructive action, so it also works
// inside a dialog.
export function ConfirmAction({
  label,
  prompt,
  disabled,
  onConfirm,
  size = "sm",
}: {
  label: string;
  prompt: string;
  disabled?: boolean;
  onConfirm: () => void;
  size?: "sm" | "xs";
}) {
  const [asking, setAsking] = useState(false);
  if (!asking)
    return (
      <Button
        variant="destructive-outline"
        size={size}
        disabled={disabled}
        onClick={() => setAsking(true)}
      >
        <Trash2 className="size-3.5" />
        {label}
      </Button>
    );
  return (
    <div
      role="group"
      aria-label={label}
      className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm"
    >
      <p className="min-w-48 flex-1">{prompt}</p>
      <Button
        variant="outline"
        size="sm"
        autoFocus
        onClick={() => setAsking(false)}
      >
        Cancel
      </Button>
      <Button
        variant="destructive"
        size="sm"
        disabled={disabled}
        onClick={() => {
          setAsking(false);
          onConfirm();
        }}
      >
        {label}
      </Button>
    </div>
  );
}

export function Data({
  title,
  value,
  raw,
}: {
  title: string;
  value: unknown;
  raw?: string;
}) {
  const [copied, setCopied] = useState(false);
  const display = raw ?? pretty(value);
  return (
    <section className="min-w-0">
      <div className="mb-1.5 flex items-center justify-between">
        <h3 className="text-[13px] font-medium">{title}</h3>
        <Button
          size="xs"
          variant="ghost"
          className="text-muted-foreground"
          onClick={() => {
            navigator.clipboard?.writeText(display).then(
              () => {
                setCopied(true);
                setTimeout(() => setCopied(false), 1200);
              },
              () => setCopied(false),
            );
          }}
        >
          {copied ? (
            <Check className="size-3.5" />
          ) : (
            <Copy className="size-3.5" />
          )}
          {copied ? "Copied" : "Copy"}
        </Button>
      </div>
      <CodeBlock>{display}</CodeBlock>
    </section>
  );
}
