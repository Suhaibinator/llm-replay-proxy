import type { Collection, History, Settings } from "@/lib/api";

export type ViewName =
  "overview" | "traffic" | "conversations" | "recordings" | "settings";

/** A destination inside the console, optionally pre-filtered. */
export type ViewLink = {
  view: ViewName;
  thread?: string;
  model?: string;
  outcome?: string;
};

/**
 * Props the shell passes to every view. Views fetch their own data through
 * `@/lib/api` and refetch whenever `refreshKey` changes (the shell bumps it on
 * its poll tick and after mutations).
 */
export type ViewProps = {
  collectionId: number;
  collections: Collection[];
  settings: Settings;
  refreshKey: number;
  /** Filters the view was opened with (from a ViewLink). */
  filter: Omit<ViewLink, "view">;
  /** Opens the recording inspector dialog the shell owns. */
  inspectRecording: (recordingId: number) => void;
  /** Opens the history-item dialog the shell owns (renders HistoryDetail). */
  openHistory: (item: History) => void;
  navigate: (to: ViewLink) => void;
};
