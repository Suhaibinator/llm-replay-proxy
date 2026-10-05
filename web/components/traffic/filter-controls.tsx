"use client";
import { Search, X } from "lucide-react";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

const ALL = "__all";

/** A labelled facet select; "" means any value. */
export function FacetSelect({
  label,
  value,
  options,
  onChange,
  format = (v) => v,
  className,
}: {
  label: string;
  value: string;
  options: { value: string; count: number }[];
  onChange: (value: string) => void;
  format?: (value: string) => string;
  className?: string;
}) {
  const known = !value || options.some((o) => o.value === value);
  return (
    <Select
      value={value || ALL}
      onValueChange={(v) => onChange(v === ALL ? "" : v)}
    >
      <SelectTrigger
        aria-label={label}
        className={cn(
          "h-8 w-auto min-w-0 max-w-56 gap-1.5 text-xs",
          value && "border-primary/50 bg-primary/5",
          className,
        )}
      >
        <span className="text-muted-foreground">{label}</span>
        <SelectValue>{value ? format(value) : "Any"}</SelectValue>
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL}>Any</SelectItem>
        {!known && <SelectItem value={value}>{format(value)}</SelectItem>}
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {format(o.value)}
            <span className="ml-2 text-xs tabular-nums text-muted-foreground">
              {o.count.toLocaleString()}
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

export function SearchBox({
  value,
  onChange,
  placeholder,
  label,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  label: string;
}) {
  return (
    <div className="relative min-w-0 flex-1 basis-56">
      <Search
        aria-hidden
        className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
      />
      <Input
        type="search"
        aria-label={label}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && value && onChange("")}
        placeholder={placeholder}
        className="h-8 bg-card pl-8 pr-8 text-xs [&::-webkit-search-cancel-button]:hidden"
      />
      {value && (
        <button
          type="button"
          aria-label="Clear search"
          onClick={() => onChange("")}
          className="absolute right-1.5 top-1/2 inline-flex size-5 -translate-y-1/2 items-center justify-center rounded text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
        >
          <X className="size-3.5" />
        </button>
      )}
    </div>
  );
}

/** A pressed/unpressed filter chip. */
export function Chip({
  pressed,
  onClick,
  children,
  className,
  label,
}: {
  pressed: boolean;
  onClick: () => void;
  children: React.ReactNode;
  className?: string;
  label?: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      aria-label={label}
      onClick={onClick}
      className={cn(
        "inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-full border bg-card px-2.5 text-xs outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring",
        pressed &&
          "border-foreground/70 bg-foreground text-background hover:bg-foreground/90",
        className,
      )}
    >
      {children}
    </button>
  );
}
