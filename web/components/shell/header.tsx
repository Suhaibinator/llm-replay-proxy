"use client";
import { useEffect, useState } from "react";
import { Moon, RefreshCw, Sun } from "lucide-react";
import type { Collection, Settings } from "@/lib/api";
import type { ViewName } from "@/components/views/types";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ModeSwitch } from "@/components/shell/mode-switch";
import { VIEWS } from "@/components/shell/routing";
import { useTheme } from "@/components/shell/theme";
import { cn } from "@/lib/utils";

export type HeaderProps = {
  view: ViewName;
  onNavigate: (view: ViewName) => void;
  settings: Settings;
  settingsLoaded: boolean;
  collections: Collection[];
  saving: boolean;
  busy: boolean;
  error: string;
  onMode: (mode: Settings["mode"]) => void;
  onCollection: (id: number) => void;
  onRefresh: () => void;
};

/** The brand mark: a reel with a recording light, drawn so it recolors with the theme. */
function Mark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      aria-hidden
      className={cn("size-6", className)}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
    >
      <circle cx="12" cy="12" r="9.25" />
      <circle cx="12" cy="12" r="2.25" fill="currentColor" stroke="none" />
      <path d="M12 2.75v4.5M12 16.75v4.5M2.75 12h4.5M16.75 12h4.5" />
    </svg>
  );
}

function Nav({
  view,
  onNavigate,
  className,
}: {
  view: ViewName;
  onNavigate: (view: ViewName) => void;
  className?: string;
}) {
  return (
    <nav aria-label="Console sections" className={className}>
      <ul className="flex h-full items-stretch gap-0.5">
        {VIEWS.map((v) => {
          const active = v.name === view;
          return (
            <li key={v.name} className="flex">
              <a
                href={`#/${v.name}`}
                aria-current={active ? "page" : undefined}
                onClick={(event) => {
                  event.preventDefault();
                  onNavigate(v.name);
                }}
                className={cn(
                  "relative inline-flex items-center px-2.5 text-[13px] font-medium outline-none transition-colors focus-visible:rounded-sm focus-visible:ring-2 focus-visible:ring-ring",
                  active
                    ? "text-foreground after:absolute after:inset-x-2.5 after:bottom-0 after:h-0.5 after:rounded-full after:bg-primary"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {v.label}
              </a>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

function CollectionSwitcher({
  settings,
  settingsLoaded,
  collections,
  onCollection,
  className,
}: Pick<
  HeaderProps,
  "settings" | "settingsLoaded" | "collections" | "onCollection"
> & { className?: string }) {
  const value = settings.active_collection_id
    ? String(settings.active_collection_id)
    : "";
  return (
    <Select
      disabled={!collections.length || !settingsLoaded}
      value={value}
      onValueChange={(v) => onCollection(Number(v))}
    >
      <SelectTrigger
        aria-label="Active collection"
        className={cn("h-8 w-40 bg-transparent text-[13px]", className)}
      >
        <SelectValue placeholder="No collection" />
      </SelectTrigger>
      <SelectContent align="end">
        {collections.map((c) => (
          <SelectItem key={c.id} value={String(c.id)}>
            {c.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

/** True once the viewport is at least `px` wide; false during SSR. */
function useMinWidth(px: number) {
  const [wide, setWide] = useState(false);
  useEffect(() => {
    const media = window.matchMedia(`(min-width: ${px}px)`);
    const update = () => setWide(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, [px]);
  return wide;
}

export function Header(props: HeaderProps) {
  const { view, onNavigate, settings, settingsLoaded, saving, busy, error } =
    props;
  const { resolved, setTheme } = useTheme();
  // One collection switcher is rendered at a time so its label stays unique.
  const wide = useMinWidth(768);
  const status = error
    ? "Needs attention"
    : saving
      ? "Saving…"
      : settingsLoaded
        ? "Proxy ready"
        : "Connecting…";
  return (
    <header className="sticky top-0 z-30 border-b bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/80">
      <div className="mx-auto flex h-14 max-w-[1440px] items-center gap-3 px-4 lg:px-6">
        <a
          href="#/overview"
          onClick={(event) => {
            event.preventDefault();
            onNavigate("overview");
          }}
          className="flex items-center gap-2 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Mark className="text-primary" />
          <h1 className="whitespace-nowrap text-[15px] font-semibold tracking-tight max-sm:sr-only">
            Replay Lab
          </h1>
        </a>
        <span
          className="hidden items-center gap-1.5 text-xs text-muted-foreground md:flex"
          aria-live="polite"
        >
          <span
            aria-hidden
            className={cn(
              "size-1.5 rounded-full",
              error ? "bg-error" : settingsLoaded ? "bg-hit" : "bg-miss",
            )}
          />
          {status}
        </span>
        <Nav
          view={view}
          onNavigate={onNavigate}
          className="hidden h-14 lg:ml-4 lg:block"
        />
        <div className="ml-auto flex items-center gap-2">
          <ModeSwitch
            mode={settings.mode}
            disabled={busy || !settingsLoaded}
            onChange={props.onMode}
          />
          {wide && <CollectionSwitcher {...props} />}
          <Button
            variant="ghost"
            size="icon-sm"
            disabled={busy || saving}
            onClick={props.onRefresh}
            aria-label="Refresh"
            title="Refresh"
          >
            <RefreshCw className={cn("size-4", busy && "animate-spin")} />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => setTheme(resolved === "dark" ? "light" : "dark")}
            aria-label={
              resolved === "dark"
                ? "Switch to light theme"
                : "Switch to dark theme"
            }
            title={resolved === "dark" ? "Light theme" : "Dark theme"}
          >
            {resolved === "dark" ? (
              <Sun className="size-4" />
            ) : (
              <Moon className="size-4" />
            )}
          </Button>
        </div>
      </div>
      <div className="mx-auto flex h-10 max-w-[1440px] items-stretch gap-2 px-4 lg:hidden">
        <Nav
          view={view}
          onNavigate={onNavigate}
          className="min-w-0 flex-1 overflow-x-auto [mask-image:linear-gradient(to_right,black_calc(100%-2rem),transparent)] [scrollbar-width:none]"
        />
        {!wide && (
          <CollectionSwitcher {...props} className="my-1 w-32 shrink-0" />
        )}
      </div>
    </header>
  );
}
