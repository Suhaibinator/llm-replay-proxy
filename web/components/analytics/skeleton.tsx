import { cn } from "@/lib/utils";

const Block = ({ className }: { className?: string }) => (
  <div
    className={cn(
      "animate-pulse rounded-md bg-muted motion-reduce:animate-none",
      className,
    )}
  />
);

function PanelSkeleton({
  className,
  height = "h-56",
}: {
  className?: string;
  height?: string;
}) {
  return (
    <div
      className={cn(
        "rounded-xl border bg-card p-4 shadow-sm sm:p-5",
        className,
      )}
    >
      <Block className="h-4 w-28" />
      <Block className="mt-2 h-3 w-48 max-w-full" />
      <Block className={cn("mt-5 w-full", height)} />
    </div>
  );
}

/** First-load placeholder with the same geometry as the loaded page. */
export function OverviewSkeleton() {
  return (
    <div aria-hidden="true" className="space-y-4">
      <div className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border sm:grid-cols-3 xl:grid-cols-6">
        {Array.from({ length: 6 }, (_, i) => (
          <div
            key={i}
            className={cn(
              "bg-card p-4 sm:p-5",
              i === 0 && "col-span-2 sm:col-span-1",
              i === 5 && "max-sm:col-span-2",
            )}
          >
            <Block className="h-3 w-20" />
            <Block className={cn("mt-3 w-24", i === 0 ? "h-12" : "h-7")} />
            <Block className="mt-3 h-3 w-32 max-w-full" />
          </div>
        ))}
      </div>
      <PanelSkeleton height="h-60" />
      <PanelSkeleton height="h-64" />
      <PanelSkeleton height="h-40" />
    </div>
  );
}
