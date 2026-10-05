"use client";
import { useEffect, useState } from "react";
import { Database, RefreshCw } from "lucide-react";
import { MissExplainer } from "@/components/conversations/miss-explainer";
import { AccentBar, OutcomePill } from "@/components/conversations/outcome";
import { RequestViewer } from "@/components/request-viewer";
import { Button } from "@/components/ui/button";
import { api, date, type History } from "@/lib/api";
import { explainOutcome, formatMs, needsMissExplanation } from "@/lib/threads";
import { cn } from "@/lib/utils";

const SOURCE: Record<string, string> = {
  replay: "Replayed by the proxy",
  upstream: "Fetched from upstream",
  proxy: "Answered by the proxy",
};
const LOOKUP: Record<string, string> = {
  hit: "Matched a recording",
  miss: "No exact match",
  bypass: "Skipped (Record mode)",
};

/**
 * Body of the history-item dialog the shell owns: what happened in plain
 * language, the request, and for a request that was not served from a
 * recording, an explanation of why it did not replay.
 */
export function HistoryDetail({
  item,
  inspectRecording,
}: {
  item: History;
  inspectRecording: (recordingId: number) => void;
}) {
  const [full, setFull] = useState<History | null>(
    item.request_text !== undefined ? item : null,
  );
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const [forItem, setForItem] = useState(item.id);
  if (forItem !== item.id) {
    setForItem(item.id);
    setFull(item.request_text !== undefined ? item : null);
    setError("");
  }

  useEffect(() => {
    if (item.request_text !== undefined) return;
    let live = true;
    api<History>(`/api/history/${item.id}`)
      .then((h) => live && setFull(h))
      .catch(
        (e) => live && setError(e instanceof Error ? e.message : String(e)),
      );
    return () => {
      live = false;
    };
  }, [item.id, item.request_text, reload]);

  const row = full ?? item;
  const why = explainOutcome(row);
  const facts: [string, React.ReactNode][] = [
    ["When", date(row.created_at)],
    [
      "Route",
      <code key="r" className="font-mono">
        {row.route}
      </code>,
    ],
    ["Served", SOURCE[row.source] || row.source || "—"],
    ["Lookup", LOOKUP[row.lookup_outcome] || row.lookup_outcome || "—"],
    ["Duration", formatMs(row.duration_ms)],
    ["First event", formatMs(row.first_event_ms)],
  ];

  return (
    <div className="min-w-0 space-y-5">
      <section
        aria-label="What happened"
        className="relative space-y-3 overflow-hidden rounded-xl border bg-card p-4 pl-5 shadow-sm"
      >
        <AccentBar kind={why.kind} />
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <OutcomePill outcome={row.outcome} />
              <h3 className="font-semibold">{why.title}</h3>
            </div>
            <p className="max-w-[75ch] text-sm text-muted-foreground">
              {why.body}
            </p>
          </div>
          {row.recording_id > 0 && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => inspectRecording(row.recording_id)}
            >
              <Database className="size-3.5" aria-hidden="true" />
              Recording #{row.recording_id}
            </Button>
          )}
        </div>
        {row.detail && (
          <p className="rounded-md bg-muted/60 px-2.5 py-1.5 font-mono text-xs leading-5 [overflow-wrap:anywhere]">
            <span className="font-sans text-muted-foreground">
              Server detail:{" "}
            </span>
            {row.detail}
          </p>
        )}
        <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-3">
          {facts.map(([k, v]) => (
            <div key={k} className="min-w-0">
              <dt className="text-xs text-muted-foreground">{k}</dt>
              <dd className="tabular-nums [overflow-wrap:anywhere]">{v}</dd>
            </div>
          ))}
        </dl>
      </section>

      {needsMissExplanation(row) && (
        <MissExplainer item={row} inspectRecording={inspectRecording} />
      )}

      {error ? (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-3 rounded-lg border border-error/40 bg-error/10 p-3 text-sm"
        >
          <p className="min-w-0 flex-1">
            Couldn&apos;t load the request: {error}
          </p>
          <Button
            size="sm"
            variant="outline"
            className="bg-background"
            onClick={() => {
              setError("");
              setReload((n) => n + 1);
            }}
          >
            Try again
          </Button>
        </div>
      ) : !full ? (
        <div className="flex h-32 items-center justify-center" role="status">
          <RefreshCw
            className="size-5 animate-spin text-muted-foreground"
            aria-hidden="true"
          />
          <span className="sr-only">Loading request</span>
        </div>
      ) : full.request_text ? (
        <RequestViewer route={full.route} raw={full.request_text} />
      ) : (
        <p className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
          No request body was stored for this call.
        </p>
      )}

      {row.key && (
        <p className="break-all font-mono text-xs text-muted-foreground">
          Match key: {row.key}
        </p>
      )}
    </div>
  );
}
