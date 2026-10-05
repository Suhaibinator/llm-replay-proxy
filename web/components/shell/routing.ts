// Hash routing for the static export: "#/traffic?outcome=miss" is a deep link
// into a view with filters. The shell also keeps two of its own parameters in
// the hash so the dialogs it owns can be linked to: "recording=<id>" opens the
// recording inspector and "history=<id>" the history-item dialog.
import type { ViewLink, ViewName } from "@/components/views/types";

export const VIEWS: { name: ViewName; label: string }[] = [
  { name: "overview", label: "Overview" },
  { name: "traffic", label: "Traffic" },
  { name: "conversations", label: "Conversations" },
  { name: "recordings", label: "Recordings" },
  { name: "settings", label: "Settings" },
];
const names = new Set<string>(VIEWS.map((v) => v.name));
const isView = (s: string): s is ViewName => names.has(s);

export type Route = ViewLink & { recording?: number; history?: number };

export function parseHash(hash: string): Route {
  const raw = hash.replace(/^#\/?/, "");
  const [path, query = ""] = raw.split("?", 2);
  const view = isView(path) ? path : "overview";
  const q = new URLSearchParams(query);
  const route: Route = { view };
  for (const key of ["thread", "model", "outcome"] as const) {
    const v = q.get(key);
    if (v) route[key] = v;
  }
  for (const key of ["recording", "history"] as const) {
    const n = Number(q.get(key));
    if (Number.isInteger(n) && n > 0) route[key] = n;
  }
  return route;
}

export function formatHash(route: Route): string {
  const q = new URLSearchParams();
  for (const key of ["thread", "model", "outcome"] as const)
    if (route[key]) q.set(key, route[key]!);
  for (const key of ["recording", "history"] as const)
    if (route[key]) q.set(key, String(route[key]));
  const query = q.toString();
  return `#/${route.view}${query ? `?${query}` : ""}`;
}

export const currentRoute = () =>
  parseHash(typeof window === "undefined" ? "" : window.location.hash);

/** Navigates by pushing a hash entry (so the back button works). */
export function pushRoute(route: Route) {
  const next = formatHash(route);
  if (window.location.hash === next) return;
  window.location.hash = next;
}

/** Rewrites the hash without a history entry (closing a dialog, for example). */
export function replaceRoute(route: Route) {
  const next = formatHash(route);
  if (window.location.hash === next) return;
  window.history.replaceState(
    null,
    "",
    `${window.location.pathname}${window.location.search}${next}`,
  );
  window.dispatchEvent(new HashChangeEvent("hashchange"));
}
