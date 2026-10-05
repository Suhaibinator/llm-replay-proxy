"use client";
import { useState } from "react";
import {
  Check,
  Copy,
  FileWarning,
  ListPlus,
  Sparkles,
  Equal,
  GitCompareArrows,
  EyeOff,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import type { Diff } from "@/lib/api";
import {
  CATEGORY_LABEL,
  VOLATILE_LABEL,
  explainDiffs,
  type DiffReport,
  type ExplainedDiff,
  type Verdict,
} from "@/lib/diff";
import { cn } from "@/lib/utils";

const INITIAL = 6;
const LONG = 280;

export function DiffReportView({
  diffs,
  exclusions,
}: {
  diffs: Diff[];
  exclusions: string[];
}) {
  const report: DiffReport = explainDiffs(diffs, exclusions);
  const [all, setAll] = useState(false);
  const shown = all ? report.items : report.items.slice(0, INITIAL);
  return (
    <div className="space-y-3">
      <VerdictBanner verdict={report.verdict} />
      {(shown.length > 0 || report.appended || report.truncated) && (
        <ol
          aria-label="Differences, most likely cause first"
          className="space-y-2"
        >
          {shown.map((e, i) => (
            <DiffItem key={`${e.diff.path}-${i}`} e={e} />
          ))}
          {report.appended && (
            <li className="flex gap-3 rounded-lg border border-dashed bg-card px-3 py-2.5 text-sm">
              <ListPlus
                className="mt-0.5 size-4 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
              <p>
                <span className="font-medium">
                  {report.appended.count.toLocaleString()} more{" "}
                  {report.appended.collection} item
                  {report.appended.count === 1 ? "" : "s"} in this request
                </span>{" "}
                <span className="text-muted-foreground">
                  from item {report.appended.from + 1} onward. The recording
                  ends before them, as an earlier turn of the same conversation
                  would.
                </span>
              </p>
            </li>
          )}
          {report.truncated && (
            <li className="flex gap-3 rounded-lg border border-dashed bg-card px-3 py-2.5 text-sm">
              <ListPlus
                className="mt-0.5 size-4 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
              <p>
                <span className="font-medium">
                  {report.truncated.count.toLocaleString()}{" "}
                  {report.truncated.collection} item
                  {report.truncated.count === 1 ? "" : "s"} only in the
                  recording
                </span>{" "}
                <span className="text-muted-foreground">
                  This request is shorter: the recording comes from later in a
                  conversation.
                </span>
              </p>
            </li>
          )}
        </ol>
      )}
      {report.items.some((e) => e.suggestion) && (
        <p className="rounded-lg bg-muted/60 p-3 text-xs text-muted-foreground">
          <span className="font-medium text-foreground">
            About match exclusions.{" "}
          </span>
          An excluded field is ignored when matching but still sent upstream
          unchanged. A collection&apos;s exclusions are fixed when it is
          created, so to use a suggested pointer, create a new collection with
          it as an exclusion and record into that collection; requests that
          differ only there will then replay.
        </p>
      )}
      {report.items.length > INITIAL && (
        <Button variant="outline" size="sm" onClick={() => setAll((v) => !v)}>
          {all
            ? "Show fewer"
            : `Show ${report.items.length - INITIAL} more difference${report.items.length - INITIAL === 1 ? "" : "s"}`}
        </Button>
      )}
    </div>
  );
}

const VERDICT_STYLE: Record<Verdict["tone"], string> = {
  culprit: "border-miss/50 bg-miss/10",
  continued: "border-recorded/45 bg-recorded/10",
  different: "border-border bg-muted/60",
  identical: "border-border bg-muted/60",
  excluded: "border-border bg-muted/60",
};
const VERDICT_ICON = {
  culprit: Sparkles,
  continued: ListPlus,
  different: GitCompareArrows,
  identical: Equal,
  excluded: EyeOff,
};

function VerdictBanner({ verdict }: { verdict: Verdict }) {
  const Icon = VERDICT_ICON[verdict.tone];
  return (
    <div
      role="status"
      className={cn(
        "flex gap-3 rounded-lg border p-3",
        VERDICT_STYLE[verdict.tone],
      )}
    >
      <Icon className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <div className="min-w-0 space-y-0.5">
        <p className="text-sm font-semibold">{verdict.headline}</p>
        <p className="text-sm text-muted-foreground">{verdict.body}</p>
      </div>
    </div>
  );
}

function DiffItem({ e }: { e: ExplainedDiff }) {
  const d = e.diff;
  const culprit = e.category === "volatile";
  return (
    <li
      className={cn(
        "min-w-0 space-y-2 rounded-lg border bg-card p-3",
        culprit && "border-miss/60 ring-1 ring-miss/25",
        e.excluded && "opacity-75",
      )}
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span
          className={cn(
            "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-semibold",
            culprit
              ? "bg-miss/20 text-foreground"
              : "bg-muted text-muted-foreground",
          )}
        >
          {culprit && <FileWarning className="size-3" aria-hidden="true" />}
          {culprit && e.volatile
            ? `Likely culprit: ${VOLATILE_LABEL[e.volatile.kind]}`
            : CATEGORY_LABEL[e.category]}
        </span>
        <span className="min-w-0 text-sm font-medium [overflow-wrap:anywhere]">
          {e.label}
        </span>
        <code className="min-w-0 text-xs text-muted-foreground [overflow-wrap:anywhere]">
          {d.path}
        </code>
      </div>
      {e.volatile && (
        <p className="text-xs text-muted-foreground">
          Looks volatile: {e.volatile.reason}
          {e.volatile.request && e.volatile.recorded && !e.volatile.whole && (
            <>
              {" "}
              (<code className="text-foreground">
                {e.volatile.request}
              </code> vs{" "}
              <code className="text-foreground">{e.volatile.recorded}</code>)
            </>
          )}
          .
        </p>
      )}
      {e.excluded && (
        <p className="text-xs text-muted-foreground">
          This collection already excludes this field from matching, so it did
          not cause the miss.
        </p>
      )}
      {e.inline ? <InlineValues e={e} /> : <SideBySide d={d} />}
      {e.suggestion && <ExclusionSuggestion e={e} />}
    </li>
  );
}

function Side({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="min-w-0">
      <p className="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </p>
      {children}
    </div>
  );
}

/** Strings: the shared context once, with each side's differing middle marked. */
function InlineValues({ e }: { e: ExplainedDiff }) {
  const c = e.inline!;
  const line = (middle: string, mark: string, label: string) => (
    <Side label={label}>
      <p className="whitespace-pre-wrap rounded-md bg-muted/50 px-2.5 py-1.5 font-mono text-xs leading-5 [overflow-wrap:anywhere]">
        {c.clippedStart && <span className="text-muted-foreground">…</span>}
        <span className="text-muted-foreground">{c.before}</span>
        {middle ? (
          <mark className={cn("rounded-[3px] px-0.5 text-foreground", mark)}>
            {middle}
          </mark>
        ) : (
          <span
            className="mx-0.5 inline-block h-3.5 w-px translate-y-0.5 bg-foreground/60"
            title="nothing here"
          />
        )}
        <span className="text-muted-foreground">{c.after}</span>
        {c.clippedEnd && <span className="text-muted-foreground">…</span>}
      </p>
    </Side>
  );
  return (
    <div className="grid gap-2 md:grid-cols-2">
      {line(c.request, "bg-miss/30", "This request")}
      {line(c.recorded, "bg-recorded/25", "Recording")}
    </div>
  );
}

function Value({ text, exists }: { text: string; exists: boolean }) {
  const [full, setFull] = useState(false);
  if (!exists)
    return (
      <p className="rounded-md bg-muted/50 px-2.5 py-1.5 text-xs italic text-muted-foreground">
        not present
      </p>
    );
  const long = text.length > LONG;
  return (
    <p className="whitespace-pre-wrap rounded-md bg-muted/50 px-2.5 py-1.5 font-mono text-xs leading-5 [overflow-wrap:anywhere]">
      {long && !full ? `${text.slice(0, LONG)}…` : text}
      {long && (
        <button
          type="button"
          onClick={() => setFull((v) => !v)}
          className="ml-1 rounded-sm font-sans font-medium text-primary underline-offset-2 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring"
        >
          {full
            ? "Show less"
            : `Show all (${text.length.toLocaleString()} chars)`}
        </button>
      )}
    </p>
  );
}

function SideBySide({ d }: { d: Diff }) {
  return (
    <div className="grid gap-2 md:grid-cols-2">
      <Side label="This request">
        <Value text={d.request_display} exists={d.request_exists} />
      </Side>
      <Side label="Recording">
        <Value text={d.recorded_display} exists={d.recorded_exists} />
      </Side>
    </div>
  );
}

function ExclusionSuggestion({ e }: { e: ExplainedDiff }) {
  const s = e.suggestion!;
  const [copied, setCopied] = useState("");
  async function copy() {
    try {
      await navigator.clipboard.writeText(s.pointer);
      setCopied("Copied");
      setTimeout(() => setCopied(""), 1500);
    } catch {
      setCopied("Select the pointer to copy it");
    }
  }
  return (
    <div className="space-y-1.5 rounded-md border border-dashed p-2.5 text-xs">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">Suggested exclusion</span>
        <code className="rounded bg-muted px-1.5 py-0.5 text-foreground [overflow-wrap:anywhere]">
          {s.pointer}
        </code>
        <Button
          size="sm"
          variant="ghost"
          className="h-6 px-2 text-xs"
          onClick={() => void copy()}
          aria-label={`Copy pointer ${s.pointer}`}
        >
          {copied === "Copied" ? (
            <Check className="size-3" aria-hidden="true" />
          ) : (
            <Copy className="size-3" aria-hidden="true" />
          )}
          {copied || "Copy"}
        </Button>
      </div>
      {s.caveats.map((c) => (
        <p key={c} className="text-muted-foreground">
          <span className="font-medium text-foreground">Note: </span>
          {c}
        </p>
      ))}
    </div>
  );
}
