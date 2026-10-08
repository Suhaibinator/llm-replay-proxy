"use client";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Check } from "lucide-react";
import {
  type Collection,
  type History,
  type Settings,
  api,
  date,
  defaults,
} from "@/lib/api";
import type { ViewLink } from "@/components/views/types";
import { AuthGate } from "@/components/auth-required";
import { ErrorBanner, Outcome } from "@/components/common";
import { HistoryDetail } from "@/components/history-detail";
import { Inspector } from "@/components/inspector";
import { Header } from "@/components/shell/header";
import { ModeBanner } from "@/components/shell/mode-switch";
import {
  type Route,
  VIEWS,
  currentRoute,
  pushRoute,
  replaceRoute,
} from "@/components/shell/routing";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ConversationsView } from "@/components/views/conversations";
import { OverviewView } from "@/components/views/overview";
import { RecordingsView } from "@/components/views/recordings";
import { SettingsView } from "@/components/views/settings";
import { TrafficView } from "@/components/views/traffic";

const POLL_MS = 5000;
const message = (e: unknown) =>
  e instanceof Error ? e.message : "Something went wrong";

function useRoute(): Route {
  const [route, setRoute] = useState<Route>({ view: "overview" });
  useEffect(() => {
    const read = () => setRoute(currentRoute());
    read();
    window.addEventListener("hashchange", read);
    return () => window.removeEventListener("hashchange", read);
  }, []);
  return route;
}

function Shell() {
  const route = useRoute();
  const [collections, setCollections] = useState<Collection[]>([]);
  const [settings, setSettings] = useState<Settings>(defaults);
  const [settingsLoaded, setSettingsLoaded] = useState(false);
  const [busy, setBusy] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [toast, setToast] = useState("");
  // Views refetch whenever this changes: on the poll tick and after mutations.
  const [refreshKey, setRefreshKey] = useState(0);
  const bump = useCallback(() => setRefreshKey((k) => k + 1), []);

  const loadVersion = useRef(0);
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
  const pollInFlight = useRef(false);

  const notify = useCallback((s: string) => {
    setToast(s);
    window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast(""), 3000);
  }, []);
  const fail = useCallback((e: unknown) => setError(message(e)), []);
  const startBusy = () => {
    busyCount.current += 1;
    setBusy(true);
  };
  const endBusy = () => {
    busyCount.current -= 1;
    if (!busyCount.current) setBusy(false);
  };

  /** Fetches collections and settings. Quiet loads (the poll) show no spinner and keep the last error. */
  const load = useCallback(
    async (quiet = false) => {
      const version = ++loadVersion.current;
      const settingsAtStart = settingsVersion.current,
        writesAtStart = pendingWrites.current;
      if (!quiet) {
        startBusy();
        setError("");
      }
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
      } catch (e) {
        if (!quiet && version === loadVersion.current) fail(e);
        else throw e;
      } finally {
        if (!quiet) endBusy();
      }
    },
    [fail],
  );
  useEffect(() => {
    void load();
  }, [load]);

  // Poll while the tab is visible: refresh collections/settings quietly and
  // tell the views to refetch. Skipped while a settings write is in flight so
  // optimistic values are never replaced by an older server state.
  useEffect(() => {
    const timer = window.setInterval(async () => {
      if (document.hidden || pendingWrites.current || pollInFlight.current)
        return;
      pollInFlight.current = true;
      try {
        await load(true);
        bump();
      } catch {
        /* Keep the last good state; manual refresh reports errors. */
      } finally {
        pollInFlight.current = false;
      }
    }, POLL_MS);
    return () => window.clearInterval(timer);
  }, [load, bump]);

  const refresh = useCallback(async () => {
    await load();
    bump();
  }, [load, bump]);

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
          notify("Collection activated");
        } else if (next.mode !== previous.mode) {
          notify(
            next.mode === "replay"
              ? "Replay mode: nothing calls upstream"
              : next.mode === "auto"
                ? "Auto mode: misses call upstream"
                : "Record mode: every request calls upstream",
          );
        } else {
          notify("Settings saved");
        }
        bump();
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

  async function createCollection(name: string, exclusions: string[]) {
    if (!settingsLoadedRef.current)
      throw new Error("Settings are still loading. Refresh and try again.");
    const c = await api<Collection>("/api/collections", {
      method: "POST",
      body: JSON.stringify({ name, exclusions }),
    });
    setCollections((current) =>
      current.some((item) => item.id === c.id) ? current : [...current, c],
    );
    await saveSettings({ active_collection_id: c.id });
  }
  async function deleteCollection(id: number) {
    await api(`/api/collections/${id}`, { method: "DELETE" });
    setCollections((current) => current.filter((c) => c.id !== id));
    notify("Collection deleted");
    bump();
  }
  async function importSnapshot(file: File) {
    await api("/api/import", {
      method: "POST",
      headers: { "Content-Type": "application/octet-stream" },
      body: file,
    });
    notify("Snapshot imported");
    await refresh();
  }

  // Navigation. Dialog state lives in the hash too, so a recording or a
  // history item can be linked to; opening and closing them never add
  // history entries.
  const navigate = useCallback((to: ViewLink) => pushRoute(to), []);
  const returnTo = useRef<HTMLElement | null>(null);
  const rememberFocus = () => {
    returnTo.current =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
  };
  const returnFocus = () => {
    const el = returnTo.current;
    if (el?.isConnected) el.focus();
  };
  const inspectRecording = useCallback((id: number) => {
    rememberFocus();
    replaceRoute({ ...currentRoute(), history: undefined, recording: id });
  }, []);
  const closeInspector = useCallback(() => {
    replaceRoute({ ...currentRoute(), recording: undefined });
  }, []);
  const [historyItem, setHistoryItem] = useState<History | null>(null);
  const openHistory = useCallback((item: History) => {
    rememberFocus();
    setHistoryItem(item);
    replaceRoute({ ...currentRoute(), recording: undefined, history: item.id });
  }, []);
  const closeHistory = useCallback(() => {
    replaceRoute({ ...currentRoute(), history: undefined });
  }, []);
  // A deep link to a history item arrives without the row: fetch it.
  useEffect(() => {
    if (!route.history) {
      setHistoryItem(null);
      return;
    }
    if (historyItem?.id === route.history) return;
    const id = route.history;
    api<History>(`/api/history/${id}`)
      .then((item) => {
        if (currentRoute().history === id) setHistoryItem(item);
      })
      .catch((e) => {
        if (currentRoute().history === id) {
          fail(e);
          closeHistory();
        }
      });
  }, [route.history, historyItem?.id, fail, closeHistory]);
  useEffect(() => {
    const label = VIEWS.find((v) => v.name === route.view)?.label ?? "Console";
    document.title = `${label} · Replay Lab`;
  }, [route.view]);

  const viewProps = useMemo(
    () => ({
      collectionId: settings.active_collection_id,
      collections,
      settings,
      refreshKey,
      filter: {
        thread: route.thread,
        model: route.model,
        provider: route.provider,
        outcome: route.outcome,
      },
      inspectRecording,
      openHistory,
      navigate,
    }),
    [
      settings,
      collections,
      refreshKey,
      route.thread,
      route.model,
      route.provider,
      route.outcome,
      inspectRecording,
      openHistory,
      navigate,
    ],
  );
  const view =
    route.view === "traffic" ? (
      <TrafficView {...viewProps} />
    ) : route.view === "conversations" ? (
      <ConversationsView {...viewProps} />
    ) : route.view === "recordings" ? (
      <RecordingsView {...viewProps} />
    ) : route.view === "settings" ? (
      <SettingsView
        {...viewProps}
        shell={{
          settingsLoaded,
          saving,
          busy,
          saveSettings,
          createCollection,
          deleteCollection,
          importSnapshot,
          notify,
        }}
      />
    ) : (
      <OverviewView {...viewProps} />
    );

  return (
    <TooltipProvider delayDuration={200}>
      <div className="flex min-h-screen flex-col">
        <Header
          view={route.view}
          onNavigate={(name) => navigate({ view: name })}
          settings={settings}
          settingsLoaded={settingsLoaded}
          collections={collections}
          saving={saving}
          busy={busy}
          error={error}
          onMode={(mode) => void saveSettings({ mode })}
          onCollection={(id) => void saveSettings({ active_collection_id: id })}
          onRefresh={() => void refresh()}
        />
        <ModeBanner mode={settings.mode} />
        <main className="mx-auto w-full max-w-[1440px] flex-1 px-4 py-5 lg:px-6 lg:py-6">
          <ErrorBanner
            message={error}
            onDismiss={() => setError("")}
            className="mb-4"
          />
          {view}
        </main>
      </div>
      <Inspector
        recordingId={route.recording ?? 0}
        onClose={closeInspector}
        onChanged={bump}
        notify={notify}
        returnFocus={returnFocus}
      />
      <Dialog
        open={!!route.history}
        onOpenChange={(next) => !next && closeHistory()}
      >
        <DialogContent
          className="max-w-4xl"
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            returnFocus();
          }}
        >
          <DialogHeader>
            <div className="flex flex-wrap items-center gap-2">
              <DialogTitle>Request</DialogTitle>
              {historyItem && <Outcome value={historyItem.outcome} />}
            </div>
            <DialogDescription className="font-mono text-xs">
              {historyItem
                ? `${historyItem.route} · ${date(historyItem.created_at)}`
                : "Loading…"}
            </DialogDescription>
          </DialogHeader>
          {historyItem && (
            <HistoryDetail
              item={historyItem}
              inspectRecording={inspectRecording}
            />
          )}
        </DialogContent>
      </Dialog>
      {toast && (
        <div
          role="status"
          className="fixed bottom-5 left-1/2 z-[100] flex max-w-[calc(100vw-2rem)] -translate-x-1/2 items-center gap-2 rounded-md border border-border bg-card px-3.5 py-2 text-[13px] text-card-foreground shadow-xl"
        >
          <Check className="size-4 shrink-0 text-hit" aria-hidden />
          {toast}
        </div>
      )}
    </TooltipProvider>
  );
}

export default function Page() {
  return (
    <AuthGate>
      <Shell />
    </AuthGate>
  );
}
