"use client";
import type { Database } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export function Outcome({ value }: { value: string }) {
  const hit = /hit|recorded|success/i.test(value),
    miss = /miss/i.test(value);
  return (
    <Badge
      className={cn(
        hit && "border-emerald-200 bg-emerald-50 text-emerald-700",
        miss && "border-amber-200 bg-amber-50 text-amber-700",
        !hit && !miss && "border-red-200 bg-red-50 text-red-700",
      )}
    >
      {value || "unknown"}
    </Badge>
  );
}
export function Empty({
  icon: Icon,
  title,
  body,
}: {
  icon: typeof Database;
  title: string;
  body: string;
}) {
  return (
    <div className="flex min-h-56 flex-col items-center justify-center px-5 text-center">
      <div className="mb-3 rounded-xl bg-muted p-3">
        <Icon className="size-5 text-muted-foreground" />
      </div>
      <p className="font-medium">{title}</p>
      <p className="mt-1 max-w-sm text-sm text-muted-foreground">{body}</p>
    </div>
  );
}
