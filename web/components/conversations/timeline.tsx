"use client";
// The shape of a conversation: one column per request, three lanes sharing
// the turn axis (items in the request, latency, output tokens). Each column is
// one keyboard stop (arrow keys move, Enter opens); the inspector below names
// whatever is hovered or focused, and the turn table is the full text fallback.
import { useRef, type KeyboardEvent } from "react";
import {
  compactNumber,
  describeTurn,
  formatMs,
  type Timeline,
} from "@/lib/threads";
import { cn } from "@/lib/utils";
import { OutcomeLegend, outcomeFill } from "./outcome";

const ITEMS_H = 120;
const LANE_H = 44;
const CAPTION_H = 22;
const COL_MIN = 12;

function axisStep(n: number) {
  if (n <= 12) return 1;
  if (n <= 30) return 5;
  if (n <= 120) return 10;
  return 25;
}

const pct = (v: number, max: number) =>
  `${Math.max(0, Math.min(100, (v / max) * 100))}%`;

export function TimelineChart({
  timeline,
  active,
  onActive,
  onOpen,
}: {
  timeline: Timeline;
  active: number;
  onActive: (index: number) => void;
  onOpen: (index: number) => void;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const { turns, maxItems, maxDuration, maxOutput } = timeline;
  const n = turns.length;
  const step = axisStep(n);
  const hasLatency = turns.some((t) => t.duration !== null);
  const hasTokens = turns.some((t) => t.output !== null);

  function move(e: KeyboardEvent, i: number) {
    let next = i;
    if (e.key === "ArrowRight" || e.key === "ArrowDown") next = i + 1;
    else if (e.key === "ArrowLeft" || e.key === "ArrowUp") next = i - 1;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = n - 1;
    else if (e.key === "PageDown") next = i + 10;
    else if (e.key === "PageUp") next = i - 10;
    else return;
    e.preventDefault();
    next = Math.max(0, Math.min(n - 1, next));
    onActive(next);
    refs.current[next]?.focus();
  }

  const lanes = [
    {
      key: "items",
      title: "Items in request",
      note: `max ${maxItems.toLocaleString()}`,
      top: 0,
      show: true,
    },
    {
      key: "latency",
      title: "Duration",
      note: hasLatency
        ? `max ${formatMs(maxDuration)} · dark = time to first event`
        : "not measured",
      top: CAPTION_H + ITEMS_H,
      show: true,
    },
    {
      key: "tokens",
      title: "Output tokens",
      note: hasTokens
        ? `max ${compactNumber(maxOutput)} · dark = reasoning`
        : "none reported",
      top: CAPTION_H + ITEMS_H + (CAPTION_H + LANE_H),
      show: true,
    },
  ];

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <OutcomeLegend counts={timeline.counts} />
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <span
            aria-hidden="true"
            className="inline-flex h-3 w-2 flex-col overflow-hidden rounded-[2px]"
          >
            <span className="h-1/2 bg-foreground/70" />
            <span className="h-1/2 bg-foreground/20" />
          </span>
          Solid top = items added that turn
        </p>
      </div>
      <div className="overflow-x-auto pb-1 [scrollbar-width:thin]">
        <div className="relative" style={{ minWidth: n * (COL_MIN + 2) }}>
          {lanes.map((lane) => (
            <p
              key={lane.key}
              aria-hidden="true"
              className="pointer-events-none absolute inset-x-0 z-10 flex items-baseline gap-2 truncate text-[11px] leading-none"
              style={{ top: lane.top + 5 }}
            >
              <span className="font-medium text-foreground">{lane.title}</span>
              <span className="truncate text-muted-foreground">
                {lane.note}
              </span>
            </p>
          ))}
          <div
            role="group"
            aria-label={`Conversation timeline, ${n} requests. Use arrow keys to move between turns and Enter to open one.`}
            className="grid gap-x-[2px]"
            style={{
              gridTemplateColumns: `repeat(${n}, minmax(${COL_MIN}px, 1fr))`,
            }}
          >
            {turns.map((t, i) => {
              const carried = t.items - t.added;
              const selected = i === active;
              return (
                <button
                  key={t.turn.history_id}
                  ref={(el) => {
                    refs.current[i] = el;
                  }}
                  type="button"
                  tabIndex={selected ? 0 : -1}
                  aria-label={`${describeTurn(t)}. Open request.`}
                  aria-current={selected || undefined}
                  data-turn={t.n}
                  onMouseEnter={() => onActive(i)}
                  onFocus={() => onActive(i)}
                  onKeyDown={(e) => move(e, i)}
                  onClick={() => onOpen(i)}
                  className={cn(
                    "group relative flex flex-col rounded-[4px] outline-none transition-colors",
                    "focus-visible:ring-2 focus-visible:ring-ring",
                    selected
                      ? "bg-foreground/[0.06]"
                      : "hover:bg-foreground/[0.04]",
                  )}
                >
                  {/* Items lane */}
                  <span style={{ height: CAPTION_H }} />
                  <span
                    className="flex flex-col justify-end border-b border-foreground/15 px-[15%]"
                    style={{ height: ITEMS_H }}
                  >
                    <span
                      className="flex w-full flex-col overflow-hidden rounded-t-[4px]"
                      style={{ height: pct(t.items, maxItems), minHeight: 2 }}
                    >
                      <span
                        className="w-full shrink-0"
                        style={{
                          height: pct(t.added, Math.max(1, t.items)),
                          minHeight: t.added ? 3 : 0,
                          ...outcomeFill(t.kind),
                        }}
                      />
                      {carried > 0 && (
                        <span
                          className="w-full flex-1"
                          style={outcomeFill(t.kind, true)}
                        />
                      )}
                    </span>
                  </span>
                  {/* Latency lane */}
                  <span style={{ height: CAPTION_H }} />
                  <span
                    className="flex flex-col justify-end border-b border-foreground/15 px-[15%]"
                    style={{ height: LANE_H }}
                  >
                    {t.duration !== null ? (
                      <span
                        className="flex w-full flex-col justify-end overflow-hidden rounded-t-[3px] bg-foreground/20"
                        style={{
                          height: pct(t.duration, maxDuration),
                          minHeight: 2,
                        }}
                      >
                        {t.firstEvent !== null && t.duration > 0 && (
                          <span
                            className="w-full bg-foreground/60"
                            style={{ height: pct(t.firstEvent, t.duration) }}
                          />
                        )}
                      </span>
                    ) : (
                      <span className="mx-auto mb-0.5 h-px w-1/2 bg-foreground/25" />
                    )}
                  </span>
                  {/* Tokens lane */}
                  <span style={{ height: CAPTION_H }} />
                  <span
                    className="flex flex-col justify-end border-b border-foreground/15 px-[15%]"
                    style={{ height: LANE_H }}
                  >
                    {t.output !== null && t.output > 0 && (
                      <span
                        className="flex w-full flex-col justify-end overflow-hidden rounded-t-[3px] bg-foreground/20"
                        style={{
                          height: pct(t.output, maxOutput),
                          minHeight: 2,
                        }}
                      >
                        {t.reasoning !== null && t.reasoning > 0 && (
                          <span
                            className="w-full bg-foreground/60"
                            style={{ height: pct(t.reasoning, t.output) }}
                          />
                        )}
                      </span>
                    )}
                  </span>
                  {/* Axis */}
                  <span
                    aria-hidden="true"
                    className={cn(
                      "h-5 pt-1 text-center text-[10px] tabular-nums leading-none",
                      selected
                        ? "font-semibold text-foreground"
                        : "text-muted-foreground",
                    )}
                  >
                    {selected ||
                    ((t.n === 1 || t.n % step === 0) &&
                      Math.abs(i - active) > 1)
                      ? t.n
                      : ""}
                  </span>
                </button>
              );
            })}
          </div>
        </div>
      </div>
      <p className="text-[11px] text-muted-foreground">
        Turn number · hover, focus or use the arrow keys to inspect a turn;
        click or press Enter to open its request.
      </p>
    </div>
  );
}
