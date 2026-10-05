"use client";
import { Circle, Play, Shuffle, type LucideIcon } from "lucide-react";
import type { Settings } from "@/lib/api";
import { cn } from "@/lib/utils";

type Mode = Settings["mode"];

/**
 * The three ways the proxy can answer a request. Replay is the only one that
 * never calls the upstream provider, so it is the quiet, primary-colored
 * state; Auto and Record spend money and are shown in the warning color.
 */
export const MODES: {
  value: Mode;
  label: string;
  short: string;
  long: string;
  spends: boolean;
  icon: LucideIcon;
}[] = [
  {
    value: "replay",
    label: "Replay",
    short: "Recordings only, never calls upstream",
    long: "Every request is answered from recordings. A request without a recording fails with 404 recording_not_found and nothing is billed.",
    spends: false,
    icon: Play,
  },
  {
    value: "auto",
    label: "Auto",
    short: "Replay hits, record misses upstream",
    long: "Requests that match a recording replay for free. Requests that miss are sent to the upstream provider, billed, and recorded.",
    spends: true,
    icon: Shuffle,
  },
  {
    value: "record",
    label: "Record",
    short: "Always calls upstream and records",
    long: "Every request is sent to the upstream provider and billed, even when a recording exists. Successful responses become new revisions.",
    spends: true,
    icon: Circle,
  },
];
export const modeInfo = (mode: Mode) =>
  MODES.find((m) => m.value === mode) ?? MODES[0];

export function ModeSwitch({
  mode,
  disabled,
  onChange,
  className,
}: {
  mode: Mode;
  disabled?: boolean;
  onChange: (mode: Mode) => void;
  className?: string;
}) {
  const spends = modeInfo(mode).spends;
  return (
    <div
      role="radiogroup"
      aria-label="Traffic mode"
      className={cn(
        "inline-flex h-9 shrink-0 items-stretch rounded-md border p-0.5",
        spends ? "border-warn/60 bg-warn/10" : "border-border bg-muted",
        className,
      )}
    >
      {MODES.map((m) => {
        const active = m.value === mode;
        const Icon = m.icon;
        return (
          <button
            key={m.value}
            type="button"
            role="radio"
            aria-checked={active}
            aria-label={m.label}
            aria-describedby={`mode-${m.value}-description`}
            title={m.short}
            disabled={disabled}
            onClick={() => !active && onChange(m.value)}
            className={cn(
              "inline-flex items-center gap-1.5 rounded px-2 text-[13px] font-medium outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50 sm:px-3",
              active
                ? m.spends
                  ? "bg-warn text-background shadow-sm"
                  : "bg-primary text-primary-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {m.value === "record" ? (
              <span
                aria-hidden
                className={cn(
                  "size-2 rounded-full",
                  active ? "rec-light bg-current" : "border-2 border-current",
                )}
              />
            ) : (
              <Icon
                aria-hidden
                className={cn(
                  "size-3.5 max-sm:hidden",
                  !active && "opacity-70",
                )}
                fill={m.value === "replay" && active ? "currentColor" : "none"}
              />
            )}
            {m.label}
          </button>
        );
      })}
      {MODES.map((m) => (
        <span key={m.value} id={`mode-${m.value}-description`} hidden>
          {m.short}
        </span>
      ))}
    </div>
  );
}

/** The plain-language line under the header while a mode spends money. */
export function ModeBanner({ mode }: { mode: Mode }) {
  const info = modeInfo(mode);
  if (!info.spends) return null;
  return (
    <div
      aria-live="polite"
      className="border-b border-warn/40 bg-warn/10 text-[13px] text-foreground"
    >
      <div className="mx-auto flex max-w-[1440px] items-start gap-2.5 px-4 py-2 lg:px-6">
        <span
          aria-hidden
          className={cn(
            "mt-[5px] size-2 shrink-0 rounded-full bg-warn",
            mode === "record" && "rec-light",
          )}
        />
        <p>
          <span className="font-semibold text-warn">{info.label} mode.</span>{" "}
          {info.long}
        </p>
      </div>
    </div>
  );
}
