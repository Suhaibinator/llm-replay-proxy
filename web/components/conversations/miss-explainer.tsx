"use client";
// "Why didn't this replay?": the closest recordings to a request that was not
// served from one, and a ranked explanation of how it differs from the chosen
// candidate (POST /api/compare).
import { useEffect, useState } from "react";
import { Database, Link2, RefreshCw, SearchX } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Chip } from "@/components/transcript";
import {
  api,
  type Collection,
  type Diff,
  type History,
  type NearestCandidate,
} from "@/lib/api";
import { cn } from "@/lib/utils";
import { DiffReportView } from "./diff-report";

type Compare = { diffs: Diff[] | null; error: string };

const percent = (v: number) =>
  `${Math.round(Math.max(0, Math.min(1, v)) * 100)}%`;

export function MissExplainer({
  item,
  inspectRecording,
}: {
  item: History;
  inspectRecording: (recordingId: number) => void;
}) {
  const [candidates, setCandidates] = useState<NearestCandidate[] | null>(null);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const [chosen, setChosen] = useState(0);
  const [compares, setCompares] = useState<Record<number, Compare>>({});
  const [exclusions, setExclusions] = useState<string[]>([]);
  const [forItem, setForItem] = useState(item.id);
  if (forItem !== item.id) {
    setForItem(item.id);
    setCandidates(null);
    setChosen(0);
    setCompares({});
    setError("");
  }

  useEffect(() => {
    let live = true;
    api<{ candidates: NearestCandidate[] }>(`/api/history/${item.id}/nearest`)
      .then((r) => {
        if (!live) return;
        const list = r?.candidates || [];
        setCandidates(list);
        setChosen((c) => c || list[0]?.recording_id || 0);
        setError("");
      })
      .catch(
        (e) => live && setError(e instanceof Error ? e.message : String(e)),
      );
    api<Collection[]>("/api/collections")
      .then((cs) => {
        if (!live) return;
        const c = cs?.find((x) => x.id === item.collection_id);
        setExclusions(c?.exclusions || []);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [item.id, item.collection_id, reload]);

  const current = compares[chosen];
  useEffect(() => {
    if (!chosen || current) return;
    let live = true;
    api<{ differences: Diff[] }>("/api/compare", {
      method: "POST",
      body: JSON.stringify({ recording_id: chosen, history_id: item.id }),
    })
      .then(
        (r) =>
          live &&
          setCompares((m) => ({
            ...m,
            [chosen]: { diffs: r?.differences || [], error: "" },
          })),
      )
      .catch(
        (e) =>
          live &&
          setCompares((m) => ({
            ...m,
            [chosen]: {
              diffs: null,
              error: e instanceof Error ? e.message : String(e),
            },
          })),
      );
    return () => {
      live = false;
    };
  }, [chosen, current, item.id]);

  const miss = item.outcome === "miss";
  const best = candidates?.find((c) => c.recording_id === chosen);
  return (
    <section
      aria-labelledby="why-heading"
      className="space-y-4 rounded-xl border bg-card p-3 shadow-sm sm:p-4"
    >
      <div className="space-y-1">
        <h3 id="why-heading" className="font-semibold">
          {miss ? "Why didn't this replay?" : "Closest recording"}
        </h3>
        <p className="text-sm text-muted-foreground">
          {miss
            ? "Replay needs an exact match. This is the closest recording in the collection and exactly how this request differs from it."
            : "Nothing was served from a recording for this request. This is the closest one in the collection and how the request differs from it."}
        </p>
      </div>

      {error ? (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-3 rounded-lg border border-error/40 bg-error/10 p-3 text-sm"
        >
          <p className="min-w-0 flex-1">
            Couldn&apos;t look for similar recordings: {error}
          </p>
          <Button
            size="sm"
            variant="outline"
            className="bg-background"
            onClick={() => setReload((n) => n + 1)}
          >
            Try again
          </Button>
        </div>
      ) : candidates === null ? (
        <Pending label="Finding the closest recordings" />
      ) : !candidates.length ? (
        <div className="flex gap-3 rounded-lg border border-dashed p-4 text-sm">
          <SearchX
            className="mt-0.5 size-4 shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
          <div className="space-y-1">
            <p className="font-medium">No similar recording exists</p>
            <p className="text-muted-foreground">
              No recording in this collection comes from the same conversation
              or shares at least a fifth of this request&apos;s content. This
              request was most likely never recorded: run it once in Record or
              Auto mode to make it replayable.
            </p>
          </div>
        </div>
      ) : (
        <>
          {best && (
            <CandidateCard c={best} inspectRecording={inspectRecording} />
          )}
          {candidates.length > 1 && (
            <fieldset className="space-y-1.5">
              <legend className="mb-1.5 text-xs font-medium text-muted-foreground">
                Compare against
              </legend>
              <div className="grid gap-1.5">
                {candidates.map((c, i) => (
                  <label
                    key={c.recording_id}
                    className={cn(
                      "flex cursor-pointer items-center gap-3 rounded-lg border px-3 py-2 text-sm transition-colors has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring",
                      c.recording_id === chosen
                        ? "border-foreground/30 bg-muted/70"
                        : "hover:bg-muted/40",
                    )}
                  >
                    <input
                      type="radio"
                      name={`candidate-${item.id}`}
                      className="sr-only"
                      checked={c.recording_id === chosen}
                      onChange={() => setChosen(c.recording_id)}
                    />
                    <span
                      aria-hidden="true"
                      className={cn(
                        "grid size-4 shrink-0 place-items-center rounded-full border",
                        c.recording_id === chosen && "border-foreground",
                      )}
                    >
                      {c.recording_id === chosen && (
                        <span className="size-2 rounded-full bg-foreground" />
                      )}
                    </span>
                    <span className="w-14 shrink-0 tabular-nums text-muted-foreground">
                      #{c.recording_id}
                    </span>
                    <span className="min-w-0 flex-1 truncate">
                      {c.preview || (
                        <span className="italic text-muted-foreground">
                          no user text
                        </span>
                      )}
                    </span>
                    {c.reason === "same_thread" && (
                      <span className="hidden shrink-0 text-xs text-muted-foreground sm:inline">
                        same conversation
                      </span>
                    )}
                    <Similarity value={c.similarity} compact />
                    {i === 0 && <span className="sr-only">(closest)</span>}
                  </label>
                ))}
              </div>
            </fieldset>
          )}
          {!current ? (
            <Pending label="Comparing with the recording" />
          ) : current.error ? (
            <div
              role="alert"
              className="flex flex-wrap items-center gap-3 rounded-lg border border-error/40 bg-error/10 p-3 text-sm"
            >
              <p className="min-w-0 flex-1">
                Comparison failed: {current.error}
              </p>
              <Button
                size="sm"
                variant="outline"
                className="bg-background"
                onClick={() =>
                  setCompares((m) => {
                    const next = { ...m };
                    delete next[chosen];
                    return next;
                  })
                }
              >
                Try again
              </Button>
            </div>
          ) : (
            <DiffReportView
              diffs={current.diffs || []}
              exclusions={exclusions}
            />
          )}
        </>
      )}
    </section>
  );
}

function Similarity({ value, compact }: { value: number; compact?: boolean }) {
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-2",
        compact ? "w-24" : "w-36",
      )}
      title="Share of this request's content also found in the recording's request"
    >
      <span
        aria-hidden="true"
        className="h-1.5 flex-1 overflow-hidden rounded-full bg-foreground/10"
      >
        <span
          className="block h-full rounded-full bg-foreground/60"
          style={{ width: percent(value) }}
        />
      </span>
      <span className="w-9 text-right text-xs tabular-nums">
        {percent(value)}
        <span className="sr-only"> similar</span>
      </span>
    </span>
  );
}

function CandidateCard({
  c,
  inspectRecording,
}: {
  c: NearestCandidate;
  inspectRecording: (id: number) => void;
}) {
  return (
    <div className="grid gap-3 rounded-lg border bg-background p-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
      <div className="min-w-0 space-y-1.5">
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-sm font-semibold">
            Recording #{c.recording_id}
          </span>
          {c.reason === "same_thread" && (
            <span className="inline-flex items-center gap-1 rounded-full border border-recorded/40 bg-recorded/10 px-2 py-0.5 text-xs font-medium">
              <Link2 className="size-3" aria-hidden="true" />
              Same conversation
            </span>
          )}
          {c.model && <Chip>{c.model}</Chip>}
          <Chip>{c.items.toLocaleString()} items</Chip>
        </div>
        <p className="line-clamp-2 text-sm text-muted-foreground">
          {c.preview || "No user text"}
        </p>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span>Shared content</span>
          <Similarity value={c.similarity} />
        </div>
      </div>
      <Button
        size="sm"
        variant="outline"
        onClick={() => inspectRecording(c.recording_id)}
      >
        <Database className="size-3.5" aria-hidden="true" />
        Inspect recording
      </Button>
    </div>
  );
}

function Pending({ label }: { label: string }) {
  return (
    <div
      role="status"
      className="flex items-center gap-2 py-3 text-sm text-muted-foreground"
    >
      <RefreshCw className="size-4 animate-spin" aria-hidden="true" />
      {label}…
    </div>
  );
}
