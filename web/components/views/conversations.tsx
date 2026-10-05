"use client";
import { useEffect, useMemo, useState } from "react";
import { ArrowLeft, MessagesSquare, RefreshCw, Search, X } from "lucide-react";
import { Empty } from "@/components/common";
import { ThreadDetailView } from "@/components/conversations/thread-detail";
import { ThreadList } from "@/components/conversations/thread-list";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  APIError,
  api,
  type History,
  type ThreadDetail,
  type ThreadSummary,
} from "@/lib/api";
import {
  THREAD_OUTCOME_FILTERS,
  THREAD_SORTS,
  distinctModels,
  filterThreads,
  sortThreads,
  threadFilterFromOutcome,
  type ThreadOutcomeFilter,
  type ThreadSort,
  type TimelineTurn,
} from "@/lib/threads";
import type { ViewProps } from "./types";

const THREAD_LIMIT = 200;
const ALL_MODELS = "__all__";

type Load<T> = { data: T | null; error: string; loading: boolean };

/**
 * Conversations: requests grouped by their thread fingerprint. The list shows
 * how each conversation went; a thread opens as a turn-by-turn timeline.
 */
export function ConversationsView(props: ViewProps) {
  return (
    <div className="space-y-5">
      <header className="space-y-1">
        <h2 className="text-xl font-semibold tracking-tight">Conversations</h2>
        <p className="max-w-[75ch] text-sm text-muted-foreground">
          Requests grouped into conversation threads: how each one grew, what
          replayed, and where replay broke.
        </p>
      </header>
      <Conversations {...props} />
    </div>
  );
}

function Conversations(props: ViewProps) {
  const { collectionId, refreshKey, filter } = props;
  const [selected, setSelected] = useState<string | null>(
    filter.thread || null,
  );
  const [query, setQuery] = useState("");
  const [outcome, setOutcome] = useState<ThreadOutcomeFilter>(
    threadFilterFromOutcome(filter.outcome),
  );
  const [model, setModel] = useState(filter.model || "");
  const [sort, setSort] = useState<ThreadSort>("recent");
  // Follow links into this view (ViewLink filters) without discarding the
  // user's own filters when a link only names a thread.
  const [linked, setLinked] = useState(filter);
  if (
    linked.thread !== filter.thread ||
    linked.outcome !== filter.outcome ||
    linked.model !== filter.model
  ) {
    setLinked(filter);
    if (linked.thread !== filter.thread) setSelected(filter.thread || null);
    if (filter.outcome && filter.outcome !== linked.outcome)
      setOutcome(threadFilterFromOutcome(filter.outcome));
    if (filter.model && filter.model !== linked.model) setModel(filter.model);
  }
  const [reload, setReload] = useState(0);

  const [list, setList] = useState<Load<ThreadSummary[]>>({
    data: null,
    error: "",
    loading: true,
  });
  const [detail, setDetail] = useState<Load<ThreadDetail>>({
    data: null,
    error: "",
    loading: false,
  });
  const [pending, setPending] = useState<number | null>(null);

  // Reset when the collection changes so one collection's threads never show
  // under another's name.
  const [forCollection, setForCollection] = useState(collectionId);
  if (forCollection !== collectionId) {
    setForCollection(collectionId);
    setList({ data: null, error: "", loading: true });
    setDetail({ data: null, error: "", loading: false });
  }

  useEffect(() => {
    if (!collectionId) return;
    let live = true;
    setList((s) => ({ ...s, loading: true }));
    api<ThreadSummary[]>(
      `/api/threads?collection_id=${collectionId}&limit=${THREAD_LIMIT}`,
    )
      .then(
        (data) =>
          live && setList({ data: data || [], error: "", loading: false }),
      )
      .catch(
        (e) =>
          live && setList((s) => ({ ...s, error: message(e), loading: false })),
      );
    return () => {
      live = false;
    };
  }, [collectionId, refreshKey, reload]);

  useEffect(() => {
    if (!collectionId || !selected) return;
    let live = true;
    setDetail((s) => ({
      data: s.data?.thread === selected ? s.data : null,
      error: "",
      loading: true,
    }));
    api<ThreadDetail>(
      `/api/threads/${encodeURIComponent(selected)}?collection_id=${collectionId}`,
    )
      .then((data) => live && setDetail({ data, error: "", loading: false }))
      .catch(
        (e) =>
          live &&
          setDetail((s) => ({
            data: s.data?.thread === selected ? s.data : null,
            error:
              e instanceof APIError && e.code === "not_found"
                ? "This conversation has no requests in the selected collection. Its history may have been cleared or trimmed by retention."
                : message(e),
            loading: false,
          })),
      );
    return () => {
      live = false;
    };
  }, [collectionId, selected, refreshKey, reload]);

  const threads = useMemo(() => list.data || [], [list.data]);
  const models = useMemo(() => distinctModels(threads), [threads]);
  const shown = useMemo(
    () => sortThreads(filterThreads(threads, { query, outcome, model }), sort),
    [threads, query, outcome, model, sort],
  );
  const filtering = !!query || outcome !== "all" || !!model;

  function open(thread: string | null) {
    setSelected(thread);
    // Lets the shell reflect the open thread (e.g. in the URL); harmless if not.
    props.navigate({ view: "conversations", ...(thread ? { thread } : {}) });
    if (typeof window !== "undefined") window.scrollTo({ top: 0 });
  }

  async function openTurn(t: TimelineTurn, thread: ThreadDetail) {
    setPending(t.turn.history_id);
    try {
      const item = await api<History>(`/api/history/${t.turn.history_id}`);
      props.openHistory(item);
    } catch {
      // Open what we know so the dialog can still explain the turn.
      props.openHistory({
        id: t.turn.history_id,
        collection_id: collectionId,
        route: thread.route,
        key: "",
        request: null,
        outcome: t.turn.outcome,
        detail: t.turn.detail,
        recording_id: t.turn.recording_id,
        created_at: t.turn.created_at,
        source: "",
        duration_ms: t.turn.duration_ms,
        first_event_ms: t.turn.first_event_ms,
        lookup_outcome: t.turn.lookup_outcome,
      });
    } finally {
      setPending(null);
    }
  }

  if (selected) {
    if (detail.data && detail.data.thread === selected)
      return (
        <ThreadDetailView
          detail={detail.data}
          pending={pending}
          onBack={() => open(null)}
          onOpenTurn={(t) => void openTurn(t, detail.data!)}
          inspectRecording={props.inspectRecording}
        />
      );
    return (
      <div className="space-y-4">
        <Button
          variant="ghost"
          size="sm"
          className="-ml-2"
          onClick={() => open(null)}
        >
          <ArrowLeft className="size-3.5" aria-hidden="true" />
          All conversations
        </Button>
        {detail.error ? (
          <ErrorPanel
            text={detail.error}
            retry={() => setReload((n) => n + 1)}
          />
        ) : (
          <Loading label="Loading conversation" />
        )}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-center">
        <div className="relative min-w-0 flex-1">
          <Search
            className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <Input
            type="search"
            aria-label="Search conversations"
            placeholder="Search messages, models or thread ids"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="bg-card pl-9"
          />
        </div>
        <div className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap">
          <Select
            value={outcome}
            onValueChange={(v) => setOutcome(v as ThreadOutcomeFilter)}
          >
            <SelectTrigger
              aria-label="Filter by outcome"
              className="bg-card sm:w-44"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {THREAD_OUTCOME_FILTERS.map((f) => (
                <SelectItem key={f.value} value={f.value}>
                  {f.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            value={model || ALL_MODELS}
            onValueChange={(v) => setModel(v === ALL_MODELS ? "" : v)}
          >
            <SelectTrigger
              aria-label="Filter by model"
              className="bg-card sm:w-44"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_MODELS}>All models</SelectItem>
              {(model && !models.includes(model)
                ? [model, ...models]
                : models
              ).map((m) => (
                <SelectItem key={m} value={m}>
                  {m}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={sort} onValueChange={(v) => setSort(v as ThreadSort)}>
            <SelectTrigger
              aria-label="Sort conversations"
              className="col-span-2 bg-card sm:col-span-1 sm:w-48"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {THREAD_SORTS.map((s) => (
                <SelectItem key={s.value} value={s.value}>
                  {s.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      {list.data && (
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
          <p role="status">
            {filtering
              ? `${shown.length.toLocaleString()} of ${threads.length.toLocaleString()} conversations`
              : `${threads.length.toLocaleString()} conversation${threads.length === 1 ? "" : "s"}, most recently active first`}
            {threads.length >= THREAD_LIMIT && ` (latest ${THREAD_LIMIT})`}
          </p>
          {filtering && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setQuery("");
                setOutcome("all");
                setModel("");
              }}
            >
              <X className="size-3.5" aria-hidden="true" />
              Clear filters
            </Button>
          )}
        </div>
      )}

      {list.error && !list.data ? (
        <ErrorPanel text={list.error} retry={() => setReload((n) => n + 1)} />
      ) : !list.data ? (
        <Loading label="Loading conversations" />
      ) : !threads.length ? (
        <div className="rounded-xl border bg-card">
          <Empty
            icon={MessagesSquare}
            title="No conversations yet"
            body="Requests that carry a conversation appear here, grouped by how the conversation opens. Send traffic through the proxy to see each thread's turns, replays and misses."
          />
        </div>
      ) : !shown.length ? (
        <div className="rounded-xl border bg-card">
          <Empty
            icon={Search}
            title="No conversations match"
            body="Try a different search, outcome or model."
          />
        </div>
      ) : (
        <ThreadList threads={shown} onOpen={open} />
      )}
    </div>
  );
}

const message = (e: unknown) =>
  e instanceof Error ? e.message : "Something went wrong";

function Loading({ label }: { label: string }) {
  return (
    <div
      role="status"
      className="flex h-48 items-center justify-center rounded-xl border bg-card"
    >
      <RefreshCw
        className="size-5 animate-spin text-muted-foreground"
        aria-hidden="true"
      />
      <span className="sr-only">{label}</span>
    </div>
  );
}

function ErrorPanel({ text, retry }: { text: string; retry: () => void }) {
  return (
    <div
      role="alert"
      className="flex flex-wrap items-center gap-3 rounded-xl border border-error/40 bg-error/10 p-4 text-sm"
    >
      <p className="min-w-0 flex-1">{text}</p>
      <Button
        size="sm"
        variant="outline"
        className="bg-background"
        onClick={retry}
      >
        Try again
      </Button>
    </div>
  );
}
