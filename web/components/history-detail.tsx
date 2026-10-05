"use client";
import type { History } from "@/lib/api";

/**
 * Body of the history-item dialog the shell owns: the request, and for a miss
 * an explanation of why it did not replay. Placeholder until built; keep the
 * export name and props.
 */
export function HistoryDetail(_props: {
  item: History;
  inspectRecording: (recordingId: number) => void;
}) {
  return null;
}
