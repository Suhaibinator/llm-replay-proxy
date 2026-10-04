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
export function TabsList({
  className,
  ...p
}: React.ComponentProps<typeof T.List>) {
  return (
    <T.List
      className={cn(
        "inline-flex h-9 items-center rounded-lg bg-muted p-1",
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
        "inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-md px-3 py-1 text-sm font-medium text-muted-foreground outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50 data-[state=active]:bg-background data-[state=active]:text-foreground data-[state=active]:shadow-sm",
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
