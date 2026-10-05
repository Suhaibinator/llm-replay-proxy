"use client";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Archive,
  ArrowDownUp,
  ChevronLeft,
  ChevronRight,
  Database,
  Search,
} from "lucide-react";
import { Empty } from "@/components/common";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { RecordingsTable } from "@/components/recordings/recordings-table";
import {
  Chip,
  FacetSelect,
  SearchBox,
} from "@/components/traffic/filter-controls";
import { api, type RecordingRow } from "@/lib/api";
import {
  defaultRecordingSort,
  emptyRecordingFilter,
  filterRecordings,
  firstDir,
  formatCost,
  isEdited,
  nextSort,
  page,
  recordingFacet,
  recordingModel,
  recordingTotals,
  sortRecordings,
  type RecordingFilter,
  type RecordingSort,
  type RecordingSortKey,
} from "@/lib/recordings";
import { shortRoute } from "@/lib/traffic";
import type { ViewProps } from "./types";

const PAGE_SIZE = 100;

const sortLabels: Record<RecordingSortKey, string> = {
  updated: "Updated",
  request: "Request",
  model: "Model",
  input: "Input tokens",
  output: "Output tokens",
  reasoning: "Reasoning tokens",
  cost: "Cost",
  replays: "Replays",
  last_replay: "Last replay",
  revisions: "Revisions",
  size: "Request size",
};

export function RecordingsView(props: ViewProps) {
  const { collectionId, refreshKey, inspectRecording, filter: link } = props;
  const [rows, setRows] = useState<RecordingRow[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [now, setNow] = useState(() => Date.now());
  const [filter, setFilter] = useState<RecordingFilter>(() => ({
    ...emptyRecordingFilter,
    model: link.model ?? "",
  }));
  const [sort, setSort] = useState<RecordingSort>(defaultRecordingSort);
  const [pageIndex, setPageIndex] = useState(0);
  const version = useRef(0);
  const loadedFor = useRef(0);
  const topRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setFilter((f) => ({ ...f, model: link.model ?? "" }));
  }, [link.model]);

  useEffect(() => {
    if (loadedFor.current !== collectionId) {
      loadedFor.current = collectionId;
      setRows([]);
      setLoaded(!collectionId);
      setError("");
      setPageIndex(0);
    }
    if (!collectionId) return;
    const v = ++version.current;
    api<RecordingRow[]>(`/api/recordings?collection_id=${collectionId}`)
      .then((rs) => {
        if (v !== version.current) return;
        setRows(rs);
        setError("");
      })
      .catch((e: unknown) => {
        if (v === version.current)
          setError(
            e instanceof Error ? e.message : "Could not load recordings",
          );
      })
      .finally(() => {
        if (v === version.current) {
          setLoaded(true);
          setNow(Date.now());
        }
      });
  }, [collectionId, refreshKey]);

  const patch = (p: Partial<RecordingFilter>) => {
    setFilter((f) => ({ ...f, ...p }));
    setPageIndex(0);
  };
  const filtered = useMemo(
    () => filterRecordings(rows, filter),
    [rows, filter],
  );
  const sorted = useMemo(
    () => sortRecordings(filtered, sort),
    [filtered, sort],
  );
  const current = page(sorted, pageIndex, PAGE_SIZE);
  const models = useMemo(() => recordingFacet(rows, recordingModel), [rows]);
  const routes = useMemo(() => recordingFacet(rows, (r) => r.route), [rows]);
  const never = useMemo(() => rows.filter((r) => !r.hits).length, [rows]);
  const edited = useMemo(() => rows.filter(isEdited).length, [rows]);
  const totals = useMemo(() => recordingTotals(filtered), [filtered]);
  const isFiltered =
    !!(filter.model || filter.route || filter.query.trim()) ||
    filter.neverReplayed ||
    filter.edited;
  const onSort = useCallback((key: RecordingSortKey) => {
    setSort((s) => nextSort(s, key));
    setPageIndex(0);
  }, []);
  const goTo = (index: number) => {
    setPageIndex(index);
    topRef.current?.scrollIntoView({ block: "nearest" });
  };

  if (!collectionId)
    return (
      <Empty
        icon={Database}
        title="Choose a collection"
        body="Select or create a collection to view its recordings."
      />
    );

  return (
    <div className="space-y-3" ref={topRef}>
      <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1 text-xs text-muted-foreground tabular-nums">
        <h2 className="text-base font-semibold text-foreground">Recordings</h2>
        <p aria-live="polite">
          {loaded &&
            (isFiltered
              ? `${filtered.length.toLocaleString()} of ${rows.length.toLocaleString()} recordings`
              : `${rows.length.toLocaleString()} recording${rows.length === 1 ? "" : "s"}`)}
        </p>
        {loaded && filtered.length > 0 && (
          <>
            <p>{totals.replays.toLocaleString()} replays</p>
            {totals.cost !== null && (
              <p title="Sum of provider-reported costs of the recorded responses">
                {formatCost(totals.cost)} recorded cost
              </p>
            )}
          </>
        )}
        {isFiltered && (
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2 text-xs"
            onClick={() => patch({ ...emptyRecordingFilter })}
          >
            Clear filters
          </Button>
        )}
        {error && (
          <p role="status" className="text-destructive">
            {error}
          </p>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <SearchBox
          label="Search recordings"
          placeholder="Search message, model, route, key…"
          value={filter.query}
          onChange={(query) => patch({ query })}
        />
        <FacetSelect
          label="Model"
          value={filter.model}
          options={models}
          onChange={(model) => patch({ model })}
        />
        <FacetSelect
          label="Route"
          value={filter.route}
          options={routes}
          format={shortRoute}
          onChange={(route) => patch({ route })}
        />
        <Chip
          pressed={filter.neverReplayed}
          onClick={() => patch({ neverReplayed: !filter.neverReplayed })}
        >
          Never replayed
          <span className="tabular-nums opacity-70">{never}</span>
        </Chip>
        <Chip
          pressed={filter.edited}
          onClick={() => patch({ edited: !filter.edited })}
        >
          Edited
          <span className="tabular-nums opacity-70">{edited}</span>
        </Chip>
        <SortSelect sort={sort} onChange={setSort} />
      </div>

      {!loaded ? (
        <div
          aria-busy="true"
          aria-label="Loading recordings"
          className="h-64 rounded-xl border bg-card motion-safe:animate-pulse"
        />
      ) : rows.length === 0 ? (
        <div className="rounded-xl border bg-card">
          <Empty
            icon={Archive}
            title={error ? "Recordings unavailable" : "No recordings yet"}
            body={
              error ||
              "Warm the collection in Record or Auto mode, then requests will appear here."
            }
          />
        </div>
      ) : filtered.length === 0 ? (
        <div className="rounded-xl border bg-card">
          <Empty
            icon={Search}
            title="No matching recordings"
            body="Search matches the user message, model, route and key."
          />
        </div>
      ) : (
        <>
          <RecordingsTable
            rows={current.rows}
            sort={sort}
            onSort={onSort}
            onInspect={inspectRecording}
            now={now}
          />
          {current.pages > 1 && (
            <nav
              aria-label="Recordings pages"
              className="flex items-center justify-end gap-2 text-xs text-muted-foreground tabular-nums"
            >
              <span>
                {current.from.toLocaleString()}–{current.to.toLocaleString()} of{" "}
                {sorted.length.toLocaleString()}
              </span>
              <Button
                variant="outline"
                size="icon"
                className="size-8"
                aria-label="Previous page"
                disabled={current.page === 0}
                onClick={() => goTo(current.page - 1)}
              >
                <ChevronLeft className="size-4" />
              </Button>
              <Button
                variant="outline"
                size="icon"
                className="size-8"
                aria-label="Next page"
                disabled={current.page >= current.pages - 1}
                onClick={() => goTo(current.page + 1)}
              >
                <ChevronRight className="size-4" />
              </Button>
            </nav>
          )}
        </>
      )}
    </div>
  );
}

/** Sorting for narrow screens, where most column headers are hidden. */
function SortSelect({
  sort,
  onChange,
}: {
  sort: RecordingSort;
  onChange: (sort: RecordingSort) => void;
}) {
  const value = `${sort.key}:${sort.dir}`;
  return (
    <Select
      value={value}
      onValueChange={(v) => {
        const [key, dir] = v.split(":") as [RecordingSortKey, "asc" | "desc"];
        onChange({ key, dir });
      }}
    >
      <SelectTrigger
        aria-label="Sort recordings"
        className="h-8 w-auto gap-1.5 text-xs md:hidden"
      >
        <ArrowDownUp aria-hidden className="size-3.5 text-muted-foreground" />
        <SelectValue>
          {sortLabels[sort.key]} {sort.dir === "asc" ? "↑" : "↓"}
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        {(Object.keys(sortLabels) as RecordingSortKey[]).map((key) => {
          const dir = firstDir(key);
          return (
            <SelectItem key={key} value={`${key}:${dir}`}>
              {sortLabels[key]}{" "}
              {key === "request" || key === "model"
                ? "A–Z"
                : key === "updated" || key === "last_replay"
                  ? "newest"
                  : "highest"}
            </SelectItem>
          );
        })}
        {!Object.keys(sortLabels).some(
          (k) => `${k}:${firstDir(k as RecordingSortKey)}` === value,
        ) && (
          <SelectItem value={value}>
            {sortLabels[sort.key]} {sort.dir === "asc" ? "↑" : "↓"}
          </SelectItem>
        )}
      </SelectContent>
    </Select>
  );
}
