"use client";
import { createContext, useContext, useState } from "react";
import * as T from "@radix-ui/react-tabs";
import { cn } from "@/lib/utils";
// Tabs visited so far, when panels should stay mounted (and keep their
// state) after the first visit instead of unmounting when hidden.
const Visited = createContext<ReadonlySet<string> | null>(null);
export function Tabs({
  keepMounted = false,
  value,
  defaultValue,
  onValueChange,
  ...p
}: React.ComponentProps<typeof T.Root> & { keepMounted?: boolean }) {
  const [uncontrolled, setUncontrolled] = useState(defaultValue);
  const active = value ?? uncontrolled;
  const [visited, setVisited] = useState<ReadonlySet<string>>(
    () => new Set(active ? [active] : []),
  );
  if (keepMounted && active && !visited.has(active))
    setVisited(new Set(visited).add(active));
  return (
    <Visited.Provider value={keepMounted ? visited : null}>
      <T.Root
        value={active}
        onValueChange={(next) => {
          setUncontrolled(next);
          onValueChange?.(next);
        }}
        {...p}
      />
    </Visited.Provider>
  );
}
/**
 * Tabs read as a row of labels on a rule, with the active one underlined in
 * the primary color; pills would compete with the segmented mode switch.
 */
export function TabsList({
  className,
  ...p
}: React.ComponentProps<typeof T.List>) {
  return (
    <T.List
      className={cn(
        "flex h-9 max-w-full items-end gap-1 overflow-x-auto border-b border-border",
        className,
      )}
      {...p}
    />
  );
}
export function TabsTrigger({
  className,
  ...p
}: React.ComponentProps<typeof T.Trigger>) {
  return (
    <T.Trigger
      className={cn(
        "-mb-px inline-flex h-9 items-center justify-center gap-1.5 whitespace-nowrap border-b-2 border-transparent px-2.5 text-sm font-medium text-muted-foreground outline-none transition-colors hover:text-foreground focus-visible:rounded-sm focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50 data-[state=active]:border-primary data-[state=active]:text-foreground",
        className,
      )}
      {...p}
    />
  );
}
export function TabsContent({
  className,
  value,
  ...p
}: React.ComponentProps<typeof T.Content>) {
  const visited = useContext(Visited);
  return (
    <T.Content
      value={value}
      forceMount={visited?.has(value) || undefined}
      className={cn(
        "mt-4 outline-none data-[state=inactive]:hidden",
        className,
      )}
      {...p}
    />
  );
}
