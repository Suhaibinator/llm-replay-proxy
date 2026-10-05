"use client";
import { useEffect, useRef, useState } from "react";
import { api, type History } from "@/lib/api";
import {
  historyURL,
  mergeHistory,
  newestId,
  TRAFFIC_ROWS,
} from "@/lib/traffic";

/** Every Nth tick reloads the newest page so deletes and retention show up. */
const FULL_RELOAD_EVERY = 12;
/** How long a newly arrived row stays highlighted. */
export const FRESH_MS = 2600;

/**
 * The newest history rows of a collection, kept current by asking only for
 * rows after the newest id on each `refreshKey` tick (history rows never
 * change). Returns ids that arrived on the latest poll in `fresh`.
 */
export function useLiveHistory(collectionId: number, refreshKey: number) {
  const [rows, setRows] = useState<History[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [fresh, setFresh] = useState<ReadonlySet<number>>(new Set());
  const [now, setNow] = useState(() => Date.now());
  const rowsRef = useRef<History[]>([]);
  const collectionRef = useRef(0);
  const version = useRef(0);
  const inFlight = useRef(false);
  const ticks = useRef(0);
  const freshTimer = useRef<number | undefined>(undefined);

  useEffect(() => {
    const switched = collectionRef.current !== collectionId;
    if (switched) {
      collectionRef.current = collectionId;
      rowsRef.current = [];
      ticks.current = 0;
      inFlight.current = false;
      setRows([]);
      setFresh(new Set());
      setError("");
      setLoaded(!collectionId);
    }
    setNow(Date.now());
    if (!collectionId) return;
    if (!switched && (inFlight.current || document.hidden)) return;
    const full =
      switched ||
      !rowsRef.current.length ||
      ++ticks.current % FULL_RELOAD_EVERY === 0;
    const before = newestId(rowsRef.current);
    const v = ++version.current;
    inFlight.current = true;
    api<History[]>(historyURL(collectionId, full ? 0 : before))
      .then((incoming) => {
        if (v !== version.current) return;
        const next = full
          ? mergeHistory([], incoming, TRAFFIC_ROWS)
          : mergeHistory(rowsRef.current, incoming, TRAFFIC_ROWS);
        const arrived =
          before > 0 ? next.filter((h) => h.id > before).map((h) => h.id) : [];
        rowsRef.current = next;
        setRows(next);
        setLoaded(true);
        setError("");
        setNow(Date.now());
        if (arrived.length) {
          setFresh(new Set(arrived));
          window.clearTimeout(freshTimer.current);
          freshTimer.current = window.setTimeout(
            () => setFresh(new Set()),
            FRESH_MS,
          );
        }
      })
      .catch((e: unknown) => {
        if (v !== version.current) return;
        setLoaded(true);
        setError(e instanceof Error ? e.message : "Could not load traffic");
      })
      .finally(() => {
        if (v === version.current) inFlight.current = false;
      });
  }, [collectionId, refreshKey]);

  useEffect(() => () => window.clearTimeout(freshTimer.current), []);

  return { rows, loaded, error, fresh, now };
}
