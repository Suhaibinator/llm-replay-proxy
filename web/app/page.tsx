"use client";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
  BarChart3,
  Archive,
  ArrowDownToLine,
  Braces,
  Check,
  ChevronRight,
  Clock3,
  Copy,
  Database,
  Download,
  FileDiff,
  Gauge,
  History as HistoryIcon,
  Pencil,
  Plus,
  Radio,
  RefreshCw,
  RotateCcw,
  Search,
  Settings2,
  Upload,
  X,
  XCircle,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input, Textarea } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { ResponseViewer } from "@/components/response-viewer";
import { AuthGate, notifyAuthRequired } from "@/components/auth-required";
import { cn } from "@/lib/utils";

type Collection = {
  id: number;
  name: string;
  exclusions: string[];
  created_at: string;
};
type Settings = {
  mode: "record" | "replay" | "auto";
  active_collection_id: number;
  first_event_delay_ms: number;
  delay_multiplier: number;
};
type Event = { data: string; offset_ms: number };
type Revision = {
  id: number;
  recording_id: number;
  status: number;
  headers: Record<string, string>;
  body: string;
  events: Event[];
  source: string;
  created_at: string;
};
type Recording = {
  id: number;
  collection_id: number;
  key: string;
  route: string;
  request: unknown;
  matching_input: unknown;
  upstream_identity: string;
  streaming: boolean;
  active_revision_id: number;
  created_at: string;
};
type Entry = {
  recording: Recording;
  revision: Revision;
  revisions?: Revision[];
  text?: string;
  text_unavailable_reason?: string;
  request_text?: string;
  matching_input_text?: string;
};
type History = {
  id: number;
  collection_id: number;
  route: string;
  key: string;
  request: unknown;
  outcome: string;
  detail: string;
  recording_id: number;
  created_at: string;
  request_text?: string;
  source: string;
  duration_ms: number | null;
  first_event_ms: number | null;
  lookup_outcome: string;
};
type Percentiles = {
  p50: number | null;
  p95: number | null;
  p99: number | null;
  samples: number;
};
type Analytics = {
  total: number;
  lifetime_total: number;
  hits: number;
  misses: number;
  errors: number;
  recorded: number;
  hit_rate: number | null;
  sources: Record<
    string,
    { total: number; duration_ms: Percentiles; first_event_ms: Percentiles }
  >;
  series: {
    start: string;
    total: number;
    hits: number;
    misses: number;
    errors: number;
  }[];
};
const emptyAnalytics: Analytics = {
  total: 0,
  lifetime_total: 0,
  hits: 0,
  misses: 0,
  errors: 0,
  recorded: 0,
  hit_rate: null,
  sources: {},
  series: [],
};
type Diff = {
  path: string;
  request: unknown;
  recorded: unknown;
  request_exists: boolean;
  recorded_exists: boolean;
  request_display: string;
  recorded_display: string;
};
const defaults: Settings = {
  mode: "replay",
  active_collection_id: 0,
  first_event_delay_ms: 0,
  delay_multiplier: 1,
};
class APIError extends Error {
  code: string;
  constructor(message: string, code = "request_failed") {
    super(message);
    this.code = code;
  }
}
async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      ...(init?.body instanceof Uint8Array
        ? {}
        : { "Content-Type": "application/json" }),
      ...init?.headers,
    },
  });
  if (res.status === 401) notifyAuthRequired();
  if (!res.ok) {
    let msg = `Request failed (${res.status})`,
      code = "request_failed";
    try {
      const x = await res.json();
      msg = x.error?.message || msg;
      code = x.error?.code || code;
    } catch {}
    throw new APIError(msg, code);
  }
  return res.status === 204 ? (undefined as T) : res.json();
}
const dateFormat = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});
const utcDayFormat = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  timeZone: "UTC",
});
const formatDate = (format: Intl.DateTimeFormat, s: string) => {
  if (!s) return "—";
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : format.format(d);
};
const date = (s: string) => formatDate(dateFormat, s);
const bucketLabel = (s: string, range: string) =>
  range === "24h" ? date(s) : `${formatDate(utcDayFormat, s)} (UTC)`;
const pretty = (v: unknown) => JSON.stringify(v, null, 2);
const editableRevision = (revision: Revision) => ({
  status: revision.status,
  headers: revision.headers,
  body: revision.body,
  events: revision.events,
});
const download = (href: string) => {
  const link = document.createElement("a");
  link.href = href;
  link.download = "";
  link.click();
};
const shortKey = (s: string) => (s ? `${s.slice(0, 8)}…${s.slice(-5)}` : "—");
function Outcome({ value }: { value: string }) {
  const hit = /hit|recorded|success/i.test(value),
    miss = /miss/i.test(value);
  return (
    <Badge
      className={cn(
        hit && "border-emerald-200 bg-emerald-50 text-emerald-700",
        miss && "border-amber-200 bg-amber-50 text-amber-700",
        !hit && !miss && "border-red-200 bg-red-50 text-red-700",
      )}
    >
      {value || "unknown"}
    </Badge>
  );
}
function Empty({
  icon: Icon,
  title,
  body,
}: {
  icon: typeof Database;
  title: string;
  body: string;
}) {
  return (
    <div className="flex min-h-56 flex-col items-center justify-center px-5 text-center">
      <div className="mb-3 rounded-xl bg-muted p-3">
        <Icon className="size-5 text-muted-foreground" />
      </div>
      <p className="font-medium">{title}</p>
      <p className="mt-1 max-w-sm text-sm text-muted-foreground">{body}</p>
    </div>
  );
}
function AnalyticsDashboard({
  analytics,
  collections,
  collectionID,
  range,
  onCollection,
  onRange,
}: {
  analytics: Analytics;
  collections: Collection[];
  collectionID: number;
  range: string;
  onCollection: (id: number) => void;
  onRange: (range: string) => void;
}) {
  const max = Math.max(1, ...analytics.series.map((p) => p.total));
  // One tab stop for the chart; arrow keys move between buckets.
  const [focusedBar, setFocusedBar] = useState(-1);
  const activeBar =
    focusedBar >= 0 && focusedBar < analytics.series.length
      ? focusedBar
      : analytics.series.length - 1;
  const moveBar = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const last = analytics.series.length - 1;
    const next =
      event.key === "ArrowLeft"
        ? Math.max(0, activeBar - 1)
        : event.key === "ArrowRight"
          ? Math.min(last, activeBar + 1)
          : event.key === "Home"
            ? 0
            : event.key === "End"
              ? last
              : null;
    if (next === null) return;
    event.preventDefault();
    setFocusedBar(next);
    (event.currentTarget.children[next] as HTMLElement | undefined)?.focus();
  };
  const pct =
    analytics.hit_rate == null
      ? "—"
      : `${Math.round(analytics.hit_rate * 100)}%`;
  const upstream = analytics.sources.upstream,
    replay = analytics.sources.replay;
  const metric = (p: Percentiles | undefined) =>
    p?.samples ? `${p.p50} / ${p.p95} ms` : "No samples";
  return (
    <Card>
      <CardHeader className="gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <CardTitle className="flex items-center gap-2">
            <BarChart3 className="size-4" />
            Traffic analytics
          </CardTitle>
          <CardDescription>
            Complete request history for the selected UTC range. Latency shows
            p50 / p95.
          </CardDescription>
        </div>
        <div className="flex flex-wrap gap-2">
          <Select
            disabled={!collections.length}
            value={collectionID ? String(collectionID) : ""}
            onValueChange={(v) => onCollection(Number(v))}
          >
            <SelectTrigger
              aria-label="Analytics collection"
              className="w-auto min-w-36"
            >
              <SelectValue placeholder="Select collection" />
            </SelectTrigger>
            <SelectContent>
              {collections.map((c) => (
                <SelectItem key={c.id} value={String(c.id)}>
                  {c.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={range} onValueChange={onRange}>
            <SelectTrigger
              aria-label="Analytics date range"
              className="w-auto min-w-36"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="24h">Last 24 hours</SelectItem>
              <SelectItem value="7d">Last 7 days</SelectItem>
              <SelectItem value="30d">Last 30 days</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </CardHeader>
      <CardContent className="space-y-5">
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-6">
          {[
            ["Requests", analytics.total],
            ["Cache hit rate", pct],
            ["Cache hits", analytics.hits],
            ["Cache misses", analytics.misses],
            ["Recorded", analytics.recorded],
            ["Completion errors", analytics.errors],
          ].map(([label, value]) => (
            <div key={label} className="rounded-lg bg-muted p-3">
              <p className="text-xl font-semibold">{value}</p>
              <p className="text-xs text-muted-foreground">{label}</p>
            </div>
          ))}
        </div>
        <div className="grid gap-4 lg:grid-cols-[2fr_1fr]">
          <div>
            <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Requests over time
            </p>
            {analytics.series.every((p) => p.total === 0) ? (
              <div className="flex h-32 items-center justify-center rounded-lg border text-sm text-muted-foreground">
                No traffic in this range
              </div>
            ) : (
              <TooltipProvider delayDuration={0}>
                <div
                  className="flex h-32 items-end gap-1 rounded-lg border px-3 pt-3"
                  role="group"
                  aria-label="Request volume over time. Use arrow keys to move between buckets."
                  onKeyDown={moveBar}
                >
                  {analytics.series.map((p, i) => (
                    <Tooltip key={p.start}>
                      <TooltipTrigger asChild>
                        {/* Full-height target so empty buckets still have a tooltip. */}
                        <div
                          role="img"
                          tabIndex={i === activeBar ? 0 : -1}
                          aria-label={`${bucketLabel(p.start, range)}: ${p.total} requests, ${p.hits} hits, ${p.misses} misses, ${p.errors} errors`}
                          onFocus={() => setFocusedBar(i)}
                          className="group flex h-full min-w-0 flex-1 items-end rounded-t outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          <div
                            className="w-full rounded-t bg-primary/75 group-hover:bg-primary group-focus-visible:bg-primary"
                            style={{
                              height:
                                p.total === 0
                                  ? 0
                                  : `${Math.max(3, (p.total / max) * 100)}%`,
                            }}
                          />
                        </div>
                      </TooltipTrigger>
                      <TooltipContent side="top">
                        <p className="font-medium">
                          {bucketLabel(p.start, range)}
                        </p>
                        <p>
                          {p.total} requests · {p.hits} hits · {p.misses} misses
                          · {p.errors} errors
                        </p>
                      </TooltipContent>
                    </Tooltip>
                  ))}
                </div>
              </TooltipProvider>
            )}
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <caption className="mb-2 text-left font-semibold uppercase tracking-wide text-muted-foreground">
                Timing summary
              </caption>
              <thead>
                <tr className="border-b">
                  <th className="py-2">Path</th>
                  <th>Duration</th>
                  <th>First event</th>
                </tr>
              </thead>
              <tbody>
                <tr className="border-b">
                  <td className="py-2 font-medium">Upstream</td>
                  <td>{metric(upstream?.duration_ms)}</td>
                  <td>{metric(upstream?.first_event_ms)}</td>
                </tr>
                <tr>
                  <td className="py-2 font-medium">Replay</td>
                  <td>{metric(replay?.duration_ms)}</td>
                  <td>{metric(replay?.first_event_ms)}</td>
                </tr>
              </tbody>
            </table>
            <p className="mt-2 text-[11px] text-muted-foreground">
              First event is measured only for complete SSE events. Historical
              rows without timing are excluded.
            </p>
          </div>
        </div>
        <details>
          <summary className="cursor-pointer rounded-sm text-xs text-muted-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring">
            Accessible traffic table
          </summary>
          <div className="mt-2 max-h-48 overflow-auto">
            <table className="w-full text-left text-xs">
              <thead>
                <tr>
                  <th>UTC bucket</th>
                  <th>Total</th>
                  <th>Hits</th>
                  <th>Misses</th>
                  <th>Errors</th>
                </tr>
              </thead>
              <tbody>
                {analytics.series.map((p) => (
                  <tr key={p.start} className="border-t">
                    <td className="py-1">{p.start}</td>
                    <td>{p.total}</td>
                    <td>{p.hits}</td>
                    <td>{p.misses}</td>
                    <td>{p.errors}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </details>
      </CardContent>
    </Card>
  );
}
function App() {
  const [collections, setCollections] = useState<Collection[]>([]),
    [settings, setSettings] = useState<Settings>(defaults),
    [recordings, setRecordings] = useState<Recording[]>([]),
    [history, setHistory] = useState<History[]>([]),
    [analytics, setAnalytics] = useState<Analytics>(emptyAnalytics),
    [activeLifetime, setActiveLifetime] = useState(0);
  const [analyticsCollection, setAnalyticsCollection] = useState(0),
    [analyticsRange, setAnalyticsRange] = useState("24h"),
    [activeTab, setActiveTab] = useState("recordings");
  const [selected, setSelected] = useState<Entry | null>(null),
    [busy, setBusy] = useState(true),
    [saving, setSaving] = useState(false),
    [creating, setCreating] = useState(false),
    [importing, setImporting] = useState(false),
    [settingsLoaded, setSettingsLoaded] = useState(false),
    [error, setError] = useState(""),
    [toast, setToast] = useState("");
  const [historyDetail, setHistoryDetail] = useState<History | null>(null);
  const [query, setQuery] = useState(""),
    [inspectOpen, setInspectOpen] = useState(false),
    [createOpen, setCreateOpen] = useState(false),
    [createName, setCreateName] = useState(""),
    [pointers, setPointers] = useState("");
  const [editText, setEditText] = useState(""),
    [advanced, setAdvanced] = useState(""),
    [compare, setCompare] = useState<Diff[] | null>(null),
    [inspectError, setInspectError] = useState(""),
    [createError, setCreateError] = useState(""),
    [revising, setRevising] = useState(false),
    [drafts, setDrafts] = useState<
      Partial<Record<"first_event_delay_ms" | "delay_multiplier", string>>
    >({});
  const fileRef = useRef<HTMLInputElement>(null);
  const inspectTriggerRef = useRef<HTMLButtonElement | null>(null);
  const historyTriggerRef = useRef<HTMLButtonElement | null>(null);
  const createTriggerRef = useRef<HTMLButtonElement | null>(null);
  const loadVersion = useRef(0);
  const collectionVersion = useRef(0);
  const inspectVersion = useRef(0);
  // Recording shown in the open inspector (0 when closed).
  const inspectedID = useRef(0);
  const compareVersion = useRef(0);
  const settingsRef = useRef(settings);
  const confirmedSettings = useRef(settings);
  const pendingPatches = useRef<Partial<Settings>[]>([]);
  const settingsVersion = useRef(0);
  // Writes PUT whole settings, so none may be sent before the server's are known.
  const settingsLoadedRef = useRef(false);
  const busyCount = useRef(0);
  const toastTimer = useRef(0);
  const settingsQueue = useRef(Promise.resolve());
  const pendingWrites = useRef(0);
  const analyticsCollectionRef = useRef(analyticsCollection);
  const analyticsRangeRef = useRef(analyticsRange);
  const analyticsVersion = useRef(0);
  const pollInFlight = useRef(false);
  const active = collections.find(
    (c) => c.id === settings.active_collection_id,
  );
  const notify = (s: string) => {
    setToast(s);
    window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast(""), 3000);
  };
  const startBusy = () => {
    busyCount.current += 1;
    setBusy(true);
  };
  const endBusy = () => {
    busyCount.current -= 1;
    if (!busyCount.current) setBusy(false);
  };
  const analyticsURL = useCallback((collectionID: number, range: string) => {
    const to = new Date();
    // Align to the server's UTC buckets so the first bar is a full bucket.
    const from =
      range === "24h"
        ? new Date(Math.floor(to.getTime() / 3600000) * 3600000 - 23 * 3600000)
        : new Date(
            Date.UTC(
              to.getUTCFullYear(),
              to.getUTCMonth(),
              to.getUTCDate() - (range === "7d" ? 6 : 29),
            ),
          );
    return `/api/analytics?collection_id=${collectionID}&from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`;
  }, []);
  const message = (e: unknown) =>
    e instanceof Error ? e.message : "Something went wrong";
  const fail = (e: unknown) => setError(message(e));
  const failInspect = (e: unknown) => setInspectError(message(e));
  const refreshAnalytics = useCallback(
    async (collectionID: number, range: string, reportError = true) => {
      const version = reportError
        ? ++analyticsVersion.current
        : analyticsVersion.current;
      try {
        const value = await api<Analytics>(analyticsURL(collectionID, range));
        if (
          version === analyticsVersion.current &&
          analyticsCollectionRef.current === collectionID &&
          analyticsRangeRef.current === range
        )
          setAnalytics(value);
        if (
          version === analyticsVersion.current &&
          settingsRef.current.active_collection_id === collectionID
        )
          setActiveLifetime(value.lifetime_total);
      } catch (e) {
        if (
          reportError &&
          version === analyticsVersion.current &&
          analyticsCollectionRef.current === collectionID &&
          analyticsRangeRef.current === range
        ) {
          setAnalytics(emptyAnalytics);
          fail(e);
        }
      }
    },
    [analyticsURL],
  );
  const load = useCallback(async () => {
    const version = ++loadVersion.current;
    const settingsAtStart = settingsVersion.current,
      writesAtStart = pendingWrites.current;
    startBusy();
    setError("");
    try {
      const [cs, fetched] = await Promise.all([
        api<Collection[]>("/api/collections"),
        api<Settings>("/api/settings"),
      ]);
      if (version !== loadVersion.current) return;
      setCollections(cs);
      // A write pending at either end, or queued meanwhile, may be newer than
      // `fetched` (the GET can be answered before an in-flight PUT commits).
      if (
        !writesAtStart &&
        !pendingWrites.current &&
        settingsVersion.current === settingsAtStart
      ) {
        confirmedSettings.current = fetched;
        settingsRef.current = fetched;
        setSettings(fetched);
        settingsLoadedRef.current = true;
        setSettingsLoaded(true);
      }
      const ss = settingsRef.current;
      if (!analyticsCollectionRef.current) {
        analyticsCollectionRef.current = ss.active_collection_id;
        setAnalyticsCollection(ss.active_collection_id);
      }
      if (ss.active_collection_id) {
        const [rs, hs] = await Promise.all([
          api<Recording[]>(
            `/api/recordings?collection_id=${ss.active_collection_id}`,
          ),
          api<History[]>(
            `/api/history?collection_id=${ss.active_collection_id}`,
          ),
        ]);
        if (version !== loadVersion.current) return;
        setRecordings(rs);
        setHistory(hs);
        const analyticsID =
          analyticsCollectionRef.current || ss.active_collection_id;
        void refreshAnalytics(analyticsID, analyticsRangeRef.current);
        if (analyticsID !== ss.active_collection_id) {
          void api<Analytics>(analyticsURL(ss.active_collection_id, "24h"))
            .then((value) => {
              if (
                settingsRef.current.active_collection_id ===
                  ss.active_collection_id &&
                version === loadVersion.current
              )
                setActiveLifetime(value.lifetime_total);
            })
            .catch(() => undefined);
        }
      } else {
        setRecordings([]);
        setHistory([]);
      }
    } catch (e) {
      // The modal inspector would hide the page banner.
      if (version === loadVersion.current)
        (inspectedID.current ? failInspect : fail)(e);
    } finally {
      endBusy();
    }
  }, [refreshAnalytics]);
  useEffect(() => {
    void load();
  }, [load]);
  useEffect(() => {
    const timer = window.setInterval(async () => {
      const cid = settingsRef.current.active_collection_id;
      const analyticsCID = analyticsCollectionRef.current || cid;
      const analyticsWindow = analyticsRangeRef.current;
      if (
        !cid ||
        document.hidden ||
        pendingWrites.current ||
        pollInFlight.current
      )
        return;
      pollInFlight.current = true;
      try {
        const version = collectionVersion.current,
          fullVersion = loadVersion.current;
        const [rs, hs] = await Promise.all([
          api<Recording[]>(`/api/recordings?collection_id=${cid}`),
          api<History[]>(`/api/history?collection_id=${cid}`),
        ]);
        if (
          settingsRef.current.active_collection_id === cid &&
          collectionVersion.current === version &&
          loadVersion.current === fullVersion
        ) {
          setRecordings(rs);
          setHistory(hs);
        }
        if (
          analyticsCollectionRef.current === analyticsCID &&
          analyticsRangeRef.current === analyticsWindow
        ) {
          await refreshAnalytics(analyticsCID, analyticsWindow, false);
        }
        if (analyticsCID !== cid) {
          const active = await api<Analytics>(analyticsURL(cid, "24h"));
          if (
            settingsRef.current.active_collection_id === cid &&
            collectionVersion.current === version &&
            loadVersion.current === fullVersion
          )
            setActiveLifetime(active.lifetime_total);
        }
      } catch {
        /* Keep the last good dashboard; manual refresh reports errors. */
      } finally {
        pollInFlight.current = false;
      }
    }, 5000);
    return () => window.clearInterval(timer);
  }, [analyticsURL, refreshAnalytics]);
  async function loadCollection(collectionID: number) {
    const version = ++collectionVersion.current;
    startBusy();
    setRecordings([]);
    setHistory([]);
    try {
      if (!collectionID) {
        setRecordings([]);
        setHistory([]);
        return;
      }
      const [rs, hs] = await Promise.all([
        api<Recording[]>(`/api/recordings?collection_id=${collectionID}`),
        api<History[]>(`/api/history?collection_id=${collectionID}`),
      ]);
      const totals = await api<Analytics>(analyticsURL(collectionID, "24h"));
      if (
        version === collectionVersion.current &&
        settingsRef.current.active_collection_id === collectionID
      ) {
        setRecordings(rs);
        setHistory(hs);
        setActiveLifetime(totals.lifetime_total);
      }
    } finally {
      endBusy();
    }
  }
  function saveSettings(patch: Partial<Settings>) {
    if (!settingsLoadedRef.current) {
      fail(new Error("Settings are still loading. Refresh and try again."));
      return Promise.resolve();
    }
    settingsVersion.current += 1;
    pendingPatches.current.push(patch);
    const optimistic = { ...settingsRef.current, ...patch };
    settingsRef.current = optimistic;
    setSettings(optimistic);
    pendingWrites.current += 1;
    setSaving(true);
    const task = settingsQueue.current.then(async () => {
      // Build from confirmed server state so a failed earlier write
      // doesn't leak into this one.
      const previous = confirmedSettings.current;
      const next = { ...previous, ...patch };
      let failed = false;
      try {
        await api("/api/settings", {
          method: "PUT",
          body: JSON.stringify(next),
        });
        confirmedSettings.current = next;
        if (next.active_collection_id !== previous.active_collection_id) {
          analyticsCollectionRef.current = next.active_collection_id;
          setAnalyticsCollection(next.active_collection_id);
          // The write succeeded; a failed refresh must not roll it back.
          try {
            await loadCollection(next.active_collection_id);
            await refreshAnalytics(
              next.active_collection_id,
              analyticsRangeRef.current,
            );
            notify("Collection activated");
          } catch (e) {
            await load();
            fail(e);
          }
        } else {
          notify("Settings saved");
        }
      } catch (e) {
        failed = true;
        fail(e);
      } finally {
        pendingPatches.current.splice(pendingPatches.current.indexOf(patch), 1);
        if (failed) {
          const rolledBack = pendingPatches.current.reduce<Settings>(
            (current, pending) => ({ ...current, ...pending }),
            confirmedSettings.current,
          );
          settingsRef.current = rolledBack;
          setSettings(rolledBack);
        }
        pendingWrites.current -= 1;
        if (pendingWrites.current === 0) setSaving(false);
      }
    });
    settingsQueue.current = task.catch(() => undefined);
    return task;
  }
  function commitNumber(field: "first_event_delay_ms" | "delay_multiplier") {
    const raw = drafts[field];
    setDrafts(({ [field]: _, ...rest }) => rest);
    if (raw === undefined) return;
    const value = Number(raw);
    const valid =
      raw.trim() !== "" &&
      Number.isFinite(value) &&
      value >= 0 &&
      (field === "first_event_delay_ms"
        ? Number.isInteger(value) && value <= 86_400_000
        : value <= 1_000_000);
    if (!valid) {
      setError(
        field === "first_event_delay_ms"
          ? "First event delay must be a whole number of milliseconds from 0 to 86400000"
          : "Delay multiplier must be between 0 and 1000000",
      );
      return;
    }
    if (value !== settingsRef.current[field])
      void saveSettings({ [field]: value });
  }
  async function createCollection() {
    if (creating || !settingsLoadedRef.current) return;
    setCreating(true);
    setCreateError("");
    try {
      const exclusions = pointers
        .split("\n")
        .map((x) => x.trim())
        .filter(Boolean);
      const c = await api<Collection>("/api/collections", {
        method: "POST",
        body: JSON.stringify({ name: createName, exclusions }),
      });
      setCollections((current) =>
        current.some((item) => item.id === c.id) ? current : [...current, c],
      );
      setCreateOpen(false);
      setCreateName("");
      setPointers("");
      await saveSettings({ active_collection_id: c.id });
    } catch (e) {
      setCreateError(message(e));
    } finally {
      setCreating(false);
    }
  }
  async function inspect(id: number, refresh = false) {
    const version = ++inspectVersion.current;
    if (!refresh) {
      inspectedID.current = id;
      compareVersion.current += 1;
      setInspectError("");
      setInspectOpen(true);
      setSelected(null);
      setCompare(null);
    }
    try {
      const e = await api<Entry>(`/api/recordings/${id}`);
      if (version !== inspectVersion.current) return;
      setSelected(e);
      setEditText(e.text || "");
      setAdvanced(pretty(editableRevision(e.revision)));
    } catch (e) {
      if (version === inspectVersion.current) failInspect(e);
    }
  }
  async function afterRevisionChange(id: number) {
    // Refresh even if the dialog was reopened meanwhile: that GET may predate
    // the new revision and would leave a stale base_revision_id.
    if (inspectedID.current === id) await inspect(id, true);
    await load();
  }
  async function edit(kind: "text" | "advanced") {
    if (!selected || revising) return;
    setRevising(true);
    setInspectError("");
    try {
      let body: string;
      if (kind === "text") {
        body = JSON.stringify({
          text: editText,
          base_revision_id: selected.revision.id,
        });
      } else {
        try {
          JSON.parse(advanced);
        } catch (e) {
          throw new Error(`Revision must be valid JSON: ${message(e)}`);
        }
        body = `{"revision":${advanced},"base_revision_id":${selected.revision.id}}`;
      }
      await api(`/api/recordings/${selected.recording.id}/edit`, {
        method: "POST",
        body,
      });
      notify("New revision activated");
      await afterRevisionChange(selected.recording.id);
    } catch (e) {
      failInspect(e);
    } finally {
      setRevising(false);
    }
  }
  async function restore(id: number) {
    if (!selected || revising) return;
    setRevising(true);
    setInspectError("");
    try {
      await api(`/api/recordings/${selected.recording.id}/restore`, {
        method: "POST",
        body: JSON.stringify({
          revision_id: id,
          base_revision_id: selected.revision.id,
        }),
      });
      notify("Revision restored");
      await afterRevisionChange(selected.recording.id);
    } catch (e) {
      failInspect(e);
    } finally {
      setRevising(false);
    }
  }
  async function compareRequest(raw: string, historyID?: number) {
    if (!selected) return;
    const version = ++compareVersion.current;
    setInspectError("");
    try {
      let body: string;
      if (historyID) {
        body = JSON.stringify({
          history_id: historyID,
          recording_id: selected.recording.id,
        });
      } else {
        JSON.parse(raw);
        body = `{"request":${raw},"recording_id":${selected.recording.id}}`;
      }
      const r = await api<{ differences: Diff[] }>("/api/compare", {
        method: "POST",
        body,
      });
      if (version === compareVersion.current) setCompare(r.differences);
    } catch (e) {
      if (version !== compareVersion.current) return;
      setCompare(null);
      if (e instanceof SyntaxError)
        failInspect(new Error("Request must be valid JSON"));
      else failInspect(e);
    }
  }
  function clearCompare() {
    compareVersion.current += 1;
    setCompare(null);
  }
  async function importDB(file: File) {
    setImporting(true);
    try {
      await api("/api/import", {
        method: "POST",
        headers: { "Content-Type": "application/octet-stream" },
        body: file,
      });
      notify("Snapshot imported");
      await load();
    } catch (e) {
      fail(e);
    } finally {
      setImporting(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  }
  const filtered = useMemo(
    () =>
      recordings.filter((r) =>
        `${r.route} ${r.key} ${pretty(r.request)}`
          .toLowerCase()
          .includes(query.toLowerCase()),
      ),
    [recordings, query],
  );
  const filteredHistory = useMemo(
    () =>
      history.filter((h) =>
        `${h.route} ${h.key} ${h.outcome} ${h.detail} ${h.request_text || pretty(h.request)}`
          .toLowerCase()
          .includes(query.toLowerCase()),
      ),
    [history, query],
  );
  async function chooseAnalyticsCollection(id: number) {
    analyticsCollectionRef.current = id;
    setAnalyticsCollection(id);
    await refreshAnalytics(id, analyticsRangeRef.current);
  }
  async function chooseAnalyticsRange(range: string) {
    analyticsRangeRef.current = range;
    setAnalyticsRange(range);
    const id = analyticsCollectionRef.current;
    if (!id) return;
    await refreshAnalytics(id, range);
  }
  const duration = analytics.sources.upstream?.duration_ms;
  const firstEvent = analytics.sources.upstream?.first_event_ms;
  const replayDuration = analytics.sources.replay?.duration_ms;
  return (
    <div className="grid-bg min-h-screen">
      <header className="sticky top-0 z-30 border-b bg-background/90 backdrop-blur-xl">
        <div className="mx-auto flex h-16 max-w-[1500px] items-center gap-3 px-4 lg:px-7">
          <div className="flex size-9 items-center justify-center rounded-xl bg-primary text-primary-foreground shadow-sm">
            <Radio className="size-5" />
          </div>
          <div>
            <h1 className="text-sm font-bold tracking-tight">Replay Lab</h1>
            <p className="text-xs text-muted-foreground">
              Inference recorder & replay proxy
            </p>
          </div>
          <div className="ml-auto flex items-center gap-2">
            <span
              className={cn(
                "hidden items-center gap-2 rounded-full border bg-card px-3 py-1.5 text-xs sm:flex",
                saving && "opacity-60",
              )}
            >
              <span
                className={cn(
                  "size-2 rounded-full",
                  error ? "bg-red-500" : "bg-emerald-500",
                )}
              />
              {error ? "Needs attention" : saving ? "Saving…" : "Proxy ready"}
            </span>
            <Button
              variant="outline"
              size="icon"
              disabled={busy || saving}
              onClick={() => void load()}
              aria-label="Refresh"
            >
              <RefreshCw className={cn("size-4", busy && "animate-spin")} />
            </Button>
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-[1500px] space-y-5 p-4 lg:p-7">
        {error && (
          <div
            role="alert"
            className="flex items-start gap-3 rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-800"
          >
            <XCircle className="mt-0.5 size-4 shrink-0" />
            <span className="flex-1">{error}</span>
            <Button
              variant="ghost"
              size="icon"
              className="-my-1 size-6 text-red-800 hover:bg-red-100"
              onClick={() => setError("")}
              aria-label="Dismiss error"
            >
              <X className="size-4" />
            </Button>
          </div>
        )}
        <AnalyticsDashboard
          analytics={analytics}
          collections={collections}
          collectionID={analyticsCollection}
          range={analyticsRange}
          onCollection={chooseAnalyticsCollection}
          onRange={chooseAnalyticsRange}
        />
        <section className="grid gap-4 xl:grid-cols-[1.55fr_1fr]">
          <Card>
            <CardHeader className="pb-3">
              <div className="flex items-start justify-between">
                <div>
                  <CardTitle>Traffic mode</CardTitle>
                  <CardDescription>
                    Choose how inference requests are handled.
                  </CardDescription>
                </div>
                <Gauge className="size-5 text-muted-foreground" />
              </div>
            </CardHeader>
            <CardContent>
              <div
                role="radiogroup"
                aria-label="Traffic mode"
                className="grid gap-2 sm:grid-cols-3"
              >
                {(
                  [
                    ["record", "Record", "Always call upstream"],
                    ["replay", "Replay", "Recordings only"],
                    ["auto", "Auto", "Replay, then record"],
                  ] as const
                ).map(([v, label, desc]) => (
                  <button
                    key={v}
                    role="radio"
                    aria-checked={settings.mode === v}
                    disabled={busy || !settingsLoaded}
                    onClick={() => void saveSettings({ mode: v })}
                    className={cn(
                      "rounded-xl border p-3 text-left transition outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-45",
                      settings.mode === v
                        ? "border-primary bg-primary text-primary-foreground shadow-sm"
                        : "bg-background hover:bg-muted",
                    )}
                  >
                    <span className="flex items-center justify-between text-sm font-semibold">
                      {label}
                      {settings.mode === v && <Check className="size-4" />}
                    </span>
                    <span
                      className={cn(
                        "mt-1 block text-xs",
                        settings.mode === v
                          ? "text-primary-foreground/75"
                          : "text-muted-foreground",
                      )}
                    >
                      {desc}
                    </span>
                  </button>
                ))}
              </div>
            </CardContent>
          </Card>
          <Card>
            <CardHeader className="pb-3">
              <CardTitle className="flex items-center gap-2">
                <Clock3 className="size-4" />
                Replay timing
              </CardTitle>
              <CardDescription>
                Delay the first event and scale the recorded cadence.
              </CardDescription>
            </CardHeader>
            <CardContent className="grid grid-cols-2 gap-4">
              <label className="text-xs font-medium">
                First event (ms)
                <Input
                  className="mt-1.5"
                  type="number"
                  min="0"
                  max="86400000"
                  step="1"
                  disabled={busy || !settingsLoaded}
                  value={
                    drafts.first_event_delay_ms ?? settings.first_event_delay_ms
                  }
                  onChange={(e) =>
                    setDrafts((d) => ({
                      ...d,
                      first_event_delay_ms: e.target.value,
                    }))
                  }
                  onBlur={() => commitNumber("first_event_delay_ms")}
                />
              </label>
              <label className="text-xs font-medium">
                Delay multiplier
                <Input
                  className="mt-1.5"
                  type="number"
                  min="0"
                  max="1000000"
                  step="0.1"
                  disabled={busy || !settingsLoaded}
                  value={drafts.delay_multiplier ?? settings.delay_multiplier}
                  onChange={(e) =>
                    setDrafts((d) => ({
                      ...d,
                      delay_multiplier: e.target.value,
                    }))
                  }
                  onBlur={() => commitNumber("delay_multiplier")}
                />
              </label>
              <Button
                className="col-span-2"
                variant="secondary"
                size="sm"
                disabled={busy || !settingsLoaded}
                onClick={() =>
                  void saveSettings({
                    first_event_delay_ms: 0,
                    delay_multiplier: 0,
                  })
                }
              >
                Instant playback
              </Button>
            </CardContent>
          </Card>
        </section>
        <section className="grid gap-5 xl:grid-cols-[310px_minmax(0,1fr)]">
          <div className="space-y-5">
            <Card>
              <CardHeader className="pb-3">
                <div className="flex items-center justify-between">
                  <div>
                    <CardTitle>Collection</CardTitle>
                    <CardDescription>
                      Matching rules and recordings
                    </CardDescription>
                  </div>
                  <Button
                    ref={createTriggerRef}
                    size="icon"
                    variant="ghost"
                    disabled={!settingsLoaded}
                    onClick={() => {
                      setCreateError("");
                      setCreateOpen(true);
                    }}
                    aria-label="New collection"
                  >
                    <Plus className="size-4" />
                  </Button>
                </div>
              </CardHeader>
              <CardContent className="space-y-3">
                <Select
                  disabled={!collections.length || !settingsLoaded}
                  value={
                    settings.active_collection_id
                      ? String(settings.active_collection_id)
                      : ""
                  }
                  onValueChange={(v) =>
                    void saveSettings({ active_collection_id: Number(v) })
                  }
                >
                  <SelectTrigger aria-label="Active collection">
                    <SelectValue placeholder="Select a collection" />
                  </SelectTrigger>
                  <SelectContent>
                    {collections.map((c) => (
                      <SelectItem key={c.id} value={String(c.id)}>
                        {c.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {active && (
                  <div className="rounded-lg bg-muted p-3">
                    <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                      Match exclusions
                    </p>
                    {active.exclusions.length ? (
                      <div className="mt-2 flex flex-wrap gap-1">
                        {active.exclusions.map((x) => (
                          <code
                            key={x}
                            className="rounded bg-background px-1.5 py-1 text-[11px]"
                          >
                            {x}
                          </code>
                        ))}
                      </div>
                    ) : (
                      <p className="mt-1 text-xs text-muted-foreground">
                        Exact body matching; no exclusions.
                      </p>
                    )}
                  </div>
                )}
                <div className="grid grid-cols-2 gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={!active}
                    onClick={() =>
                      active && download(`/api/collections/${active.id}/export`)
                    }
                  >
                    <Download className="size-3.5" />
                    Export
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={importing}
                    onClick={() => fileRef.current?.click()}
                  >
                    <Upload className="size-3.5" />
                    {importing ? "Importing…" : "Import"}
                  </Button>
                  <input
                    ref={fileRef}
                    className="hidden"
                    type="file"
                    accept=".sqlite,.db,application/octet-stream"
                    onChange={(e) =>
                      e.target.files?.[0] && void importDB(e.target.files[0])
                    }
                  />
                </div>
              </CardContent>
            </Card>
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="flex items-center gap-2">
                  <Activity className="size-4" />
                  At a glance
                </CardTitle>
              </CardHeader>
              <CardContent className="grid grid-cols-2 gap-2">
                <div className="rounded-lg bg-muted p-3">
                  <p className="text-2xl font-semibold">{recordings.length}</p>
                  <p className="text-xs text-muted-foreground">Recordings</p>
                </div>
                <div className="rounded-lg bg-muted p-3">
                  <p className="text-2xl font-semibold">{activeLifetime}</p>
                  <p className="text-xs text-muted-foreground">
                    Total requests
                  </p>
                </div>
              </CardContent>
            </Card>
          </div>
          <Card className="min-w-0">
            <Tabs value={activeTab} onValueChange={setActiveTab}>
              <CardHeader className="gap-3 pb-2 sm:flex-row sm:items-center sm:justify-between">
                <TabsList>
                  <TabsTrigger value="recordings">
                    <Archive className="size-3.5" />
                    Recordings
                  </TabsTrigger>
                  <TabsTrigger value="history">
                    <HistoryIcon className="size-3.5" />
                    History
                  </TabsTrigger>
                </TabsList>
                <div className="relative w-full sm:max-w-64 sm:flex-1">
                  <Search className="absolute left-2.5 top-2.5 size-4 text-muted-foreground" />
                  <Input
                    aria-label={`Search ${activeTab}`}
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    className="pl-8"
                    placeholder="Search requests…"
                  />
                </div>
              </CardHeader>
              <CardContent>
                <TabsContent value="recordings">
                  {!active ? (
                    <Empty
                      icon={Database}
                      title="Choose a collection"
                      body="Select or create a collection to view its recordings."
                    />
                  ) : filtered.length === 0 && query ? (
                    <Empty
                      icon={Search}
                      title="No matching recordings"
                      body="Try a different route, key, or request field."
                    />
                  ) : filtered.length === 0 ? (
                    <Empty
                      icon={Archive}
                      title="No recordings yet"
                      body="Warm the collection in Record or Auto mode, then requests will appear here."
                    />
                  ) : (
                    <div className="overflow-x-auto">
                      <table className="w-full text-left text-sm">
                        <thead className="border-b text-xs text-muted-foreground">
                          <tr>
                            <th className="px-3 py-2 font-medium">Route</th>
                            <th className="px-3 py-2 font-medium">Kind</th>
                            <th className="px-3 py-2 font-medium">Key</th>
                            <th className="px-3 py-2 font-medium">Recorded</th>
                            <th />
                          </tr>
                        </thead>
                        <tbody>
                          {filtered.map((r) => (
                            <tr
                              key={r.id}
                              className="border-b last:border-0 hover:bg-muted/50"
                            >
                              <td className="px-3 py-3 font-medium">
                                {r.route.replace("/v1/", "")}
                              </td>
                              <td className="px-3 py-3">
                                <Badge>{r.streaming ? "stream" : "json"}</Badge>
                              </td>
                              <td className="px-3 py-3 font-mono text-xs text-muted-foreground">
                                {shortKey(r.key)}
                              </td>
                              <td className="px-3 py-3 text-xs text-muted-foreground">
                                {date(r.created_at)}
                              </td>
                              <td className="px-3 py-3 text-right">
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  onClick={(event) => {
                                    inspectTriggerRef.current =
                                      event.currentTarget;
                                    void inspect(r.id);
                                  }}
                                >
                                  Inspect
                                  <ChevronRight className="size-3.5" />
                                </Button>
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </TabsContent>
                <TabsContent value="history">
                  {history.length === 0 ? (
                    <Empty
                      icon={HistoryIcon}
                      title="No request history"
                      body="Inference traffic for this collection will appear here."
                    />
                  ) : filteredHistory.length === 0 ? (
                    <Empty
                      icon={Search}
                      title="No matching requests"
                      body="Try a different route, outcome, detail, key, or request field."
                    />
                  ) : (
                    <div className="space-y-1">
                      {filteredHistory.map((h) => (
                        <button
                          key={h.id}
                          onClick={(event) => {
                            if (h.recording_id) {
                              inspectTriggerRef.current = event.currentTarget;
                              void inspect(h.recording_id);
                            } else {
                              historyTriggerRef.current = event.currentTarget;
                              setHistoryDetail(h);
                            }
                          }}
                          className="flex w-full items-center gap-3 rounded-lg p-3 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          <Outcome value={h.outcome} />
                          <div className="min-w-0 flex-1">
                            <p className="truncate text-sm font-medium">
                              {h.route}
                            </p>
                            <p className="truncate text-xs text-muted-foreground">
                              {h.detail || shortKey(h.key)}
                            </p>
                          </div>
                          <time className="hidden text-xs text-muted-foreground sm:block">
                            {date(h.created_at)}
                          </time>
                          <ChevronRight className="size-4 text-muted-foreground" />
                        </button>
                      ))}
                    </div>
                  )}
                </TabsContent>
              </CardContent>
            </Tabs>
          </Card>
        </section>
      </main>
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent
          className="max-w-lg"
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            createTriggerRef.current?.focus();
          }}
        >
          <DialogHeader>
            <DialogTitle>New collection</DialogTitle>
            <DialogDescription>
              Create immutable matching rules for a new recording set.
            </DialogDescription>
          </DialogHeader>
          {createError && (
            <div
              role="alert"
              className="mb-4 flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-800"
            >
              <XCircle className="mt-0.5 size-4 shrink-0" />
              <span className="flex-1">{createError}</span>
              <Button
                variant="ghost"
                size="icon"
                className="-my-1 size-6 text-red-800 hover:bg-red-100"
                onClick={() => setCreateError("")}
                aria-label="Dismiss error"
              >
                <X className="size-4" />
              </Button>
            </div>
          )}
          <div className="space-y-4">
            <label className="block text-sm font-medium">
              Name
              <Input
                autoFocus
                className="mt-1.5"
                value={createName}
                onChange={(e) => setCreateName(e.target.value)}
                placeholder="Checkout demo"
              />
            </label>
            <label className="block text-sm font-medium">
              JSON Pointer exclusions{" "}
              <span className="font-normal text-muted-foreground">
                (one per line)
              </span>
              <Textarea
                className="code mt-1.5"
                value={pointers}
                onChange={(e) => setPointers(e.target.value)}
                placeholder={"/metadata/request_id\n/user/timestamp"}
              />
            </label>
            <Button
              className="w-full"
              disabled={!createName.trim() || creating || !settingsLoaded}
              onClick={() => void createCollection()}
            >
              {creating ? "Creating…" : "Create and activate"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Inspector
        open={inspectOpen}
        setOpen={(next) => {
          if (!next) {
            inspectVersion.current += 1;
            inspectedID.current = 0;
          }
          setInspectOpen(next);
        }}
        entry={selected}
        editText={editText}
        setEditText={setEditText}
        advanced={advanced}
        setAdvanced={setAdvanced}
        edit={edit}
        restore={restore}
        compare={compare}
        compareRequest={compareRequest}
        clearCompare={clearCompare}
        history={history}
        saving={revising}
        error={inspectError}
        clearError={() => setInspectError("")}
        returnFocus={() => {
          const trigger = inspectTriggerRef.current;
          if (trigger?.isConnected) trigger.focus();
        }}
      />
      <Dialog
        open={historyDetail !== null}
        onOpenChange={(next) => !next && setHistoryDetail(null)}
      >
        <DialogContent
          className="max-w-3xl"
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            const trigger = historyTriggerRef.current;
            if (trigger?.isConnected) trigger.focus();
          }}
        >
          <DialogHeader>
            <div className="flex flex-wrap items-center gap-2 pr-8">
              <DialogTitle>Request history</DialogTitle>
              {historyDetail && <Outcome value={historyDetail.outcome} />}
            </div>
            <DialogDescription>
              {historyDetail?.route} · {date(historyDetail?.created_at || "")}
            </DialogDescription>
          </DialogHeader>
          {historyDetail && (
            <div className="space-y-4">
              {historyDetail.detail && (
                <div className="rounded-lg bg-muted p-3 text-sm">
                  {historyDetail.detail}
                </div>
              )}
              <Data
                title="Request"
                value={historyDetail.request}
                raw={historyDetail.request_text}
              />
              <p className="break-all font-mono text-xs text-muted-foreground">
                Match key: {historyDetail.key}
              </p>
            </div>
          )}
        </DialogContent>
      </Dialog>
      {toast && (
        <div
          role="status"
          className="fixed bottom-5 left-1/2 z-[100] flex -translate-x-1/2 items-center gap-2 rounded-full bg-zinc-900 px-4 py-2 text-sm text-white shadow-xl"
        >
          <Check className="size-4 text-emerald-400" />
          {toast}
        </div>
      )}
    </div>
  );
}
function Inspector({
  open,
  setOpen,
  entry,
  editText,
  setEditText,
  advanced,
  setAdvanced,
  edit,
  restore,
  compare,
  compareRequest,
  clearCompare,
  history,
  saving,
  error,
  clearError,
  returnFocus,
}: {
  open: boolean;
  setOpen: (x: boolean) => void;
  entry: Entry | null;
  editText: string;
  setEditText: (x: string) => void;
  advanced: string;
  setAdvanced: (x: string) => void;
  edit: (k: "text" | "advanced") => void;
  restore: (id: number) => void;
  compare: Diff[] | null;
  compareRequest: (raw: string, historyID?: number) => void;
  clearCompare: () => void;
  history: History[];
  saving: boolean;
  error: string;
  clearError: () => void;
  returnFocus: () => void;
}) {
  const [compareRaw, setCompareRaw] = useState("");
  const [chosenHistoryID, setHistoryID] = useState(0);
  const recordingID = entry?.recording.id;
  const [compareFor, setCompareFor] = useState(recordingID);
  if (recordingID !== undefined && recordingID !== compareFor) {
    setCompareFor(recordingID);
    setCompareRaw("");
    setHistoryID(0);
  }
  const misses = history.filter((h) => /miss/i.test(h.outcome));
  const historyID = misses.some((h) => h.id === chosenHistoryID)
    ? chosenHistoryID
    : 0;
  // The chosen miss left polled history: drop its result with the selection.
  useEffect(() => {
    if (chosenHistoryID && !historyID) {
      setHistoryID(0);
      clearCompare();
    }
  }, [chosenHistoryID, historyID, clearCompare]);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent
        className="max-w-5xl"
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          returnFocus();
        }}
      >
        {!entry ? (
          error ? (
            <div role="alert" className="space-y-4 py-10 text-center">
              <XCircle className="mx-auto size-7 text-red-600" />
              <div>
                <p className="font-medium">Could not load this recording</p>
                <p className="mt-1 text-sm text-red-700">{error}</p>
              </div>
              <Button variant="outline" onClick={() => setOpen(false)}>
                Close
              </Button>
            </div>
          ) : (
            <div
              className="flex h-48 items-center justify-center"
              role="status"
            >
              <RefreshCw className="size-5 animate-spin text-muted-foreground" />
              <span className="sr-only">Loading recording</span>
            </div>
          )
        ) : (
          <>
            <DialogHeader>
              <div className="flex flex-wrap items-center gap-2 pr-8">
                <DialogTitle>{entry.recording.route}</DialogTitle>
                <Badge>
                  {entry.recording.streaming ? "SSE stream" : "JSON response"}
                </Badge>
                <Badge className="normal-case tracking-normal">
                  HTTP {entry.revision.status}
                </Badge>
              </div>
              <DialogDescription className="break-all font-mono">
                {entry.recording.key}
              </DialogDescription>
            </DialogHeader>
            {error && (
              <div
                role="alert"
                className="mb-4 flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-800"
              >
                <XCircle className="mt-0.5 size-4 shrink-0" />
                <span className="flex-1">{error}</span>
                <Button
                  variant="ghost"
                  size="icon"
                  className="-my-1 size-6 text-red-800 hover:bg-red-100"
                  onClick={clearError}
                  aria-label="Dismiss error"
                >
                  <X className="size-4" />
                </Button>
              </div>
            )}
            <Tabs keepMounted defaultValue="response">
              <TabsList className="flex h-auto flex-wrap">
                <TabsTrigger value="response">Response</TabsTrigger>
                <TabsTrigger value="inspect">Inspect</TabsTrigger>
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
              <TabsContent
                value="inspect"
                className="grid gap-4 lg:grid-cols-2"
              >
                <Data
                  title="Original request"
                  value={entry.recording.request}
                  raw={entry.request_text}
                />
                <Data
                  title="Matching input"
                  value={entry.recording.matching_input}
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
                  raw={
                    entry.recording.streaming ? undefined : entry.revision.body
                  }
                />
              </TabsContent>
              <TabsContent value="text">
                <div className="mb-3">
                  <p className="text-sm font-medium">Assistant answer</p>
                  <p className="text-xs text-muted-foreground">
                    Rebuilds protocol deltas and final text while preserving
                    event identities and order.
                  </p>
                </div>
                {entry.text_unavailable_reason ? (
                  <div className="rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900">
                    <p className="font-semibold">
                      Plain-text editing unavailable
                    </p>
                    <p className="mt-1">{entry.text_unavailable_reason}</p>
                    <p className="mt-2 text-xs">
                      Use the Advanced editor for mixed text/tool, reasoning, or
                      multimodal output.
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
                    <div className="mt-3 flex items-center justify-between">
                      <p className="text-xs text-muted-foreground">
                        Usage remains historical after edits.
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
                <div className="mb-3 flex items-end justify-between">
                  <div>
                    <p className="text-sm font-medium">Revision JSON</p>
                    <p className="text-xs text-muted-foreground">
                      Validation checks JSON, required event structure,
                      completion, and final output agreement.
                    </p>
                  </div>
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
                <div className="space-y-2">
                  {(entry.revisions || [entry.revision]).map((r) => (
                    <div key={r.id} className="rounded-lg border p-3">
                      <div className="flex items-center gap-3">
                        <div className="rounded-lg bg-muted p-2">
                          <Archive className="size-4" />
                        </div>
                        <div className="min-w-0 flex-1">
                          <p className="text-sm font-medium">
                            Revision {r.id} · {r.source}
                          </p>
                          <p className="text-xs text-muted-foreground">
                            {date(r.created_at)} · HTTP {r.status}
                            {r.id === entry.recording.active_revision_id
                              ? " · active"
                              : ""}
                          </p>
                        </div>
                        {r.id === entry.recording.active_revision_id ? (
                          <Badge className="border-emerald-200 bg-emerald-50 text-emerald-700">
                            Active
                          </Badge>
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
                      <details className="mt-2 border-t pt-2 text-xs">
                        <summary className="cursor-pointer rounded-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">
                          Inspect revision data
                        </summary>
                        <pre className="mt-2 max-h-64 overflow-auto rounded-lg bg-zinc-950 p-3 font-mono leading-5 text-zinc-200">
                          {pretty(r)}
                        </pre>
                      </details>
                    </div>
                  ))}
                </div>
              </TabsContent>
              <TabsContent value="compare">
                <div className="space-y-3 rounded-lg border p-4">
                  <div>
                    <p className="text-sm font-medium">
                      Compare a missed request
                    </p>
                    <p className="text-xs text-muted-foreground">
                      Select a stored miss to preserve exact numeric precision,
                      or paste request JSON.
                    </p>
                  </div>
                  {misses.length > 0 && (
                    <Select
                      value={String(historyID)}
                      onValueChange={(v) => {
                        setHistoryID(Number(v));
                        clearCompare();
                      }}
                    >
                      <SelectTrigger aria-label="Missed request">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent className="max-w-[var(--radix-select-trigger-width)]">
                        <SelectItem value="0">
                          Paste request JSON instead
                        </SelectItem>
                        {misses.map((h) => (
                          <SelectItem key={h.id} value={String(h.id)}>
                            {date(h.created_at)} · {h.route} · {shortKey(h.key)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  )}
                  {!historyID && (
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
                      disabled={!historyID && !compareRaw.trim()}
                      onClick={() =>
                        compareRequest(compareRaw, historyID || undefined)
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
                        <tr className="border-b">
                          <th className="p-2">JSON path</th>
                          <th className="p-2">Missed request</th>
                          <th className="p-2">Recording</th>
                        </tr>
                      </thead>
                      <tbody>
                        {compare.length ? (
                          compare.map((d, i) => (
                            <tr className="border-b" key={`${d.path}${i}`}>
                              <td className="p-2 font-mono">{d.path || "/"}</td>
                              <td className="p-2 font-mono text-amber-700">
                                {d.request_exists
                                  ? d.request_display
                                  : "(missing)"}
                              </td>
                              <td className="p-2 font-mono text-emerald-700">
                                {d.recorded_exists
                                  ? d.recorded_display
                                  : "(missing)"}
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
        )}
      </DialogContent>
    </Dialog>
  );
}
function Data({
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
      <div className="mb-2 flex items-center justify-between">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {title}
        </h3>
        <Button
          size="sm"
          variant="ghost"
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
      <pre className="max-h-72 overflow-auto rounded-lg bg-zinc-950 p-3 text-[11px] leading-5 text-zinc-200">
        {display}
      </pre>
    </section>
  );
}
export default function Page() {
  return (
    <AuthGate>
      <App />
    </AuthGate>
  );
}
