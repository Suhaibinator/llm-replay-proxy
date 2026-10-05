"use client";
import { useRef, useState, type ReactNode } from "react";
import { Check, Download, Plus, Upload } from "lucide-react";
import {
  type Collection,
  type NumberSetting,
  type Settings,
  APIError,
  count,
  date,
  download,
} from "@/lib/api";
import type { ViewProps } from "./types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Field, Input, Textarea } from "@/components/ui/input";
import { ErrorBanner, ViewHeader } from "@/components/common";
import { ConfirmAction } from "@/components/inspector";
import { MODES } from "@/components/shell/mode-switch";
import { cn } from "@/lib/utils";

/** What the settings view needs from the shell beyond ViewProps. */
export type SettingsShell = {
  settingsLoaded: boolean;
  saving: boolean;
  busy: boolean;
  saveSettings: (patch: Partial<Settings>) => Promise<void>;
  /** Creates the collection and makes it active. */
  createCollection: (name: string, exclusions: string[]) => Promise<void>;
  deleteCollection: (id: number) => Promise<void>;
  importSnapshot: (file: File) => Promise<void>;
  notify: (text: string) => void;
};

const message = (e: unknown) =>
  e instanceof Error ? e.message : "Something went wrong";

const limits: Record<
  NumberSetting,
  { label: string; min: number; max: number; integer: boolean; help: string }
> = {
  first_event_delay_ms: {
    label: "First event delay (ms)",
    min: 0,
    max: 86_400_000,
    integer: true,
    help: "First event delay must be a whole number of milliseconds from 0 to 86400000.",
  },
  delay_multiplier: {
    label: "Delay multiplier",
    min: 0,
    max: 1_000_000,
    integer: false,
    help: "Delay multiplier must be between 0 and 1000000.",
  },
  history_limit: {
    label: "Requests kept per collection",
    min: 0,
    max: 100_000_000,
    integer: true,
    help: "History limit must be a whole number from 0 (keep everything) to 100000000.",
  },
};

function Section({
  title,
  description,
  children,
}: {
  title: string;
  description: ReactNode;
  children: ReactNode;
}) {
  return (
    <section
      aria-labelledby={`settings-${title.toLowerCase().replace(/\W+/g, "-")}`}
      className="grid gap-4 border-t py-6 first:border-t-0 first:pt-0 lg:grid-cols-[260px_minmax(0,1fr)] lg:gap-10"
    >
      <div>
        <h3
          id={`settings-${title.toLowerCase().replace(/\W+/g, "-")}`}
          className="text-[15px] font-semibold tracking-tight"
        >
          {title}
        </h3>
        <p className="mt-1 text-[13px] text-muted-foreground">{description}</p>
      </div>
      <div className="min-w-0 max-w-4xl space-y-3">{children}</div>
    </section>
  );
}

export function SettingsView({
  collections,
  settings,
  shell,
}: ViewProps & { shell: SettingsShell }) {
  const { settingsLoaded, saving, busy, saveSettings } = shell;
  const disabled = busy || !settingsLoaded;
  const active = collections.find(
    (c) => c.id === settings.active_collection_id,
  );
  const [drafts, setDrafts] = useState<Partial<Record<NumberSetting, string>>>(
    {},
  );
  const [fieldErrors, setFieldErrors] = useState<
    Partial<Record<NumberSetting, string>>
  >({});
  function commitNumber(field: NumberSetting) {
    const raw = drafts[field];
    setDrafts(({ [field]: _, ...rest }) => rest);
    if (raw === undefined) return;
    const rule = limits[field];
    const value = Number(raw);
    const valid =
      raw.trim() !== "" &&
      Number.isFinite(value) &&
      value >= rule.min &&
      value <= rule.max &&
      (!rule.integer || Number.isInteger(value));
    if (!valid) {
      setFieldErrors((e) => ({ ...e, [field]: rule.help }));
      return;
    }
    setFieldErrors(({ [field]: _, ...rest }) => rest);
    if (value !== settings[field]) void saveSettings({ [field]: value });
  }
  const numberInput = (field: NumberSetting, step: string) => (
    <Field
      label={limits[field].label}
      htmlFor={`setting-${field}`}
      hint={fieldErrors[field]}
      className={cn(fieldErrors[field] && "[&_p]:text-destructive")}
    >
      <Input
        id={`setting-${field}`}
        type="number"
        inputMode="decimal"
        min={limits[field].min}
        max={limits[field].max}
        step={step}
        disabled={disabled}
        aria-invalid={!!fieldErrors[field]}
        value={drafts[field] ?? settings[field]}
        onChange={(e) => setDrafts((d) => ({ ...d, [field]: e.target.value }))}
        onBlur={() => commitNumber(field)}
        onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
      />
    </Field>
  );

  // New collection form.
  const [creating, setCreating] = useState(false);
  const [formOpen, setFormOpen] = useState(false);
  const [name, setName] = useState("");
  const [pointers, setPointers] = useState("");
  const [createError, setCreateError] = useState("");
  const newButtonRef = useRef<HTMLButtonElement>(null);
  async function create() {
    if (creating || !settingsLoaded) return;
    setCreating(true);
    setCreateError("");
    try {
      const exclusions = pointers
        .split("\n")
        .map((x) => x.trim())
        .filter(Boolean);
      await shell.createCollection(name.trim(), exclusions);
      setFormOpen(false);
      setName("");
      setPointers("");
      newButtonRef.current?.focus();
    } catch (e) {
      setCreateError(message(e));
    } finally {
      setCreating(false);
    }
  }

  // Deletes and imports.
  const [listError, setListError] = useState("");
  const [deleting, setDeleting] = useState(0);
  async function remove(c: Collection) {
    setDeleting(c.id);
    setListError("");
    try {
      await shell.deleteCollection(c.id);
    } catch (e) {
      setListError(
        e instanceof APIError && e.code === "collection_active"
          ? `“${c.name}” is the active collection. Switch to another collection first, then delete it.`
          : message(e),
      );
    } finally {
      setDeleting(0);
    }
  }
  const fileRef = useRef<HTMLInputElement>(null);
  const [importing, setImporting] = useState(false);
  const [importError, setImportError] = useState("");
  async function importFile(file: File) {
    setImporting(true);
    setImportError("");
    try {
      await shell.importSnapshot(file);
    } catch (e) {
      setImportError(message(e));
    } finally {
      setImporting(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  }
  const instant =
    settings.first_event_delay_ms === 0 && settings.delay_multiplier === 0;

  return (
    <div className="space-y-6">
      <ViewHeader
        title="Settings"
        description="Collections, matching, replay timing and retention for this proxy. Changes save as you make them."
      />
      <div>
        <Section
          title="Collections"
          description={
            <>
              A collection is a set of recordings with its own matching rules.
              Requests are matched and recorded in the active one; the header
              switches it too.
            </>
          }
        >
          <ErrorBanner message={listError} onDismiss={() => setListError("")} />
          <ul className="divide-y overflow-hidden rounded-lg border bg-card">
            {collections.length === 0 && (
              <li className="p-4 text-[13px] text-muted-foreground">
                No collections yet. Create one to start recording.
              </li>
            )}
            {collections.map((c) => {
              const isActive = c.id === settings.active_collection_id;
              return (
                <li
                  key={c.id}
                  className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3"
                >
                  <div className="min-w-0 flex-1 basis-48">
                    <p className="flex items-center gap-2 font-medium">
                      <span className="truncate">{c.name}</span>
                      {isActive && (
                        <Badge tone="primary">
                          <Check className="size-3" aria-hidden />
                          Active
                        </Badge>
                      )}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {c.exclusions.length
                        ? count(c.exclusions.length, "exclusion")
                        : "Exact matching"}{" "}
                      · created {date(c.created_at)}
                    </p>
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    {!isActive && (
                      <Button
                        variant="outline"
                        size="xs"
                        disabled={disabled || saving}
                        onClick={() =>
                          void saveSettings({ active_collection_id: c.id })
                        }
                      >
                        Make active
                      </Button>
                    )}
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() =>
                        download(`/api/collections/${c.id}/export`)
                      }
                      aria-label={`Export ${c.name}`}
                    >
                      <Download className="size-3.5" />
                      Export
                    </Button>
                    {isActive ? (
                      <span
                        className="px-2 text-xs text-muted-foreground"
                        title="The active collection cannot be deleted"
                      >
                        In use
                      </span>
                    ) : (
                      <ConfirmAction
                        size="xs"
                        label="Delete"
                        prompt={`Delete “${c.name}” with all of its recordings and history? This cannot be undone.`}
                        disabled={deleting === c.id}
                        onConfirm={() => void remove(c)}
                      />
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
          {formOpen ? (
            <Card className="space-y-4 p-4">
              <div>
                <p className="font-medium">New collection</p>
                <p className="text-[13px] text-muted-foreground">
                  Matching rules are fixed once created; make a new collection
                  to change them.
                </p>
              </div>
              <ErrorBanner
                message={createError}
                onDismiss={() => setCreateError("")}
              />
              <Field label="Name" htmlFor="collection-name">
                <Input
                  id="collection-name"
                  autoFocus
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="Checkout demo"
                  onKeyDown={(e) =>
                    e.key === "Enter" && name.trim() && void create()
                  }
                />
              </Field>
              <Field
                label="JSON Pointer exclusions (one per line)"
                htmlFor="collection-exclusions"
                hint="Fields removed from matching only, never from the forwarded request. For example /metadata/run_id ignores a volatile run id."
              >
                <Textarea
                  id="collection-exclusions"
                  className="code min-h-20 text-xs"
                  spellCheck={false}
                  value={pointers}
                  onChange={(e) => setPointers(e.target.value)}
                  placeholder={"/metadata/request_id\n/user/timestamp"}
                />
              </Field>
              <div className="flex flex-wrap justify-end gap-2">
                <Button
                  variant="ghost"
                  onClick={() => {
                    setFormOpen(false);
                    setCreateError("");
                    newButtonRef.current?.focus();
                  }}
                >
                  Cancel
                </Button>
                <Button
                  disabled={!name.trim() || creating || !settingsLoaded}
                  onClick={() => void create()}
                >
                  {creating ? "Creating…" : "Create and activate"}
                </Button>
              </div>
            </Card>
          ) : (
            <Button
              ref={newButtonRef}
              variant="outline"
              size="sm"
              disabled={!settingsLoaded}
              onClick={() => setFormOpen(true)}
            >
              <Plus className="size-3.5" />
              New collection
            </Button>
          )}
        </Section>

        <Section
          title="Matching rules"
          description="How a request is matched to a recording in the active collection. Use the inspector's Compare tab to see which fields differ on a miss."
        >
          {active ? (
            <Card className="p-4 text-[13px]">
              <p className="font-medium">{active.name}</p>
              <p className="mt-1 text-muted-foreground">
                Requests match when the route, upstream identity and the
                canonical JSON body are identical
                {active.exclusions.length
                  ? ", ignoring these fields:"
                  : ". No fields are excluded."}
              </p>
              {active.exclusions.length > 0 && (
                <ul className="mt-2 flex flex-wrap gap-1.5">
                  {active.exclusions.map((x) => (
                    <li key={x}>
                      <code className="rounded border bg-muted px-1.5 py-0.5 text-xs">
                        {x}
                      </code>
                    </li>
                  ))}
                </ul>
              )}
              <p className="mt-3 text-xs text-muted-foreground">
                Property order is ignored; array order, strings and numeric
                precision are exact. Streaming and non-streaming requests never
                match each other.
              </p>
            </Card>
          ) : (
            <p className="text-[13px] text-muted-foreground">
              Create or activate a collection to see its rules.
            </p>
          )}
        </Section>

        <Section
          title="Traffic mode"
          description="How the proxy answers requests. Replay never calls the upstream provider; Auto and Record do and are billed."
        >
          <Card className="divide-y">
            {MODES.map((m) => {
              const selected = settings.mode === m.value;
              return (
                <label
                  key={m.value}
                  className="flex cursor-pointer items-start gap-3 px-4 py-3 has-[:focus-visible]:bg-muted/60"
                >
                  <input
                    type="radio"
                    name="traffic-mode"
                    className="mt-1 accent-primary"
                    checked={selected}
                    disabled={disabled}
                    onChange={() => void saveSettings({ mode: m.value })}
                  />
                  <span className="min-w-0 flex-1 text-[13px]">
                    <span className="flex items-center gap-2 font-medium">
                      {m.label}
                      {m.spends ? (
                        <Badge tone="warn">Calls upstream</Badge>
                      ) : (
                        <Badge tone="hit">Offline</Badge>
                      )}
                    </span>
                    <span className="mt-0.5 block text-muted-foreground">
                      {m.long}
                    </span>
                  </span>
                </label>
              );
            })}
          </Card>
        </Section>

        <Section
          title="Replay timing"
          description="Recorded streams replay with their original cadence. Delay the first event and scale the gaps between later ones, or play back instantly."
        >
          <Card className="space-y-4 p-4">
            <div className="grid gap-4 sm:grid-cols-2">
              {numberInput("first_event_delay_ms", "1")}
              {numberInput("delay_multiplier", "0.1")}
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <Button
                variant={instant ? "secondary" : "outline"}
                size="sm"
                disabled={disabled || instant}
                onClick={() =>
                  void saveSettings({
                    first_event_delay_ms: 0,
                    delay_multiplier: 0,
                  })
                }
              >
                {instant && <Check className="size-3.5" aria-hidden />}
                Instant playback
              </Button>
              <p className="text-xs text-muted-foreground">
                {instant
                  ? "Events are sent as fast as the client reads them."
                  : `First event after ${settings.first_event_delay_ms.toLocaleString()} ms, later gaps × ${settings.delay_multiplier}.`}
              </p>
            </div>
          </Card>
        </Section>

        <Section
          title="History retention"
          description="Request history powers the Traffic, Conversations and Overview pages. Older rows are removed at startup and every 10 minutes."
        >
          <Card className="p-4">
            <div className="grid gap-4 sm:grid-cols-2">
              {numberInput("history_limit", "1")}
              <p className="self-end pb-2 text-xs text-muted-foreground">
                Per collection. 0 keeps everything. Recordings are never
                affected.
              </p>
            </div>
          </Card>
        </Section>

        <Section
          title="Snapshots"
          description="Move recordings between installations. A snapshot holds one collection with its rules and revisions; request history is not included."
        >
          <Card className="space-y-3 p-4">
            <ErrorBanner
              message={importError}
              onDismiss={() => setImportError("")}
            />
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={!active}
                onClick={() =>
                  active && download(`/api/collections/${active.id}/export`)
                }
              >
                <Download className="size-3.5" />
                Export {active ? `“${active.name}”` : "collection"}
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={importing}
                onClick={() => fileRef.current?.click()}
              >
                <Upload className="size-3.5" />
                {importing ? "Importing…" : "Import snapshot"}
              </Button>
              <input
                ref={fileRef}
                className="hidden"
                type="file"
                accept=".sqlite,.db,application/octet-stream"
                aria-label="Snapshot file"
                onChange={(e) =>
                  e.target.files?.[0] && void importFile(e.target.files[0])
                }
              />
            </div>
            <p className="text-xs text-muted-foreground">
              Import adds the snapshot's collections to this database. Keep the
              same non-secret upstream settings so recorded keys still match.
            </p>
          </Card>
        </Section>
      </div>
    </div>
  );
}
