import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export type Column<Row> = {
  key: string;
  header: string;
  cell: (row: Row) => ReactNode;
  /** Numeric columns align right with tabular figures. */
  numeric?: boolean;
};

/** The table twin of a chart: every plotted value, readable without hover. */
export function DataTable<Row>({
  caption,
  columns,
  rows,
  rowKey,
  className,
}: {
  caption: string;
  columns: Column<Row>[];
  rows: Row[];
  rowKey: (row: Row, index: number) => string;
  className?: string;
}) {
  return (
    <div className={cn("max-h-80 overflow-auto rounded-lg border", className)}>
      <table className="w-full border-collapse text-left text-xs">
        <caption className="sr-only">{caption}</caption>
        <thead className="sticky top-0 bg-muted">
          <tr>
            {columns.map((c) => (
              <th
                key={c.key}
                scope="col"
                className={cn(
                  "whitespace-nowrap px-3 py-2 font-medium text-muted-foreground",
                  c.numeric && "text-right",
                )}
              >
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={rowKey(r, i)} className="border-t">
              {columns.map((c, j) =>
                j === 0 ? (
                  <th
                    key={c.key}
                    scope="row"
                    className="whitespace-nowrap px-3 py-1.5 font-normal"
                  >
                    {c.cell(r)}
                  </th>
                ) : (
                  <td
                    key={c.key}
                    className={cn(
                      "whitespace-nowrap px-3 py-1.5",
                      c.numeric && "text-right tabular-nums",
                    )}
                  >
                    {c.cell(r)}
                  </td>
                ),
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
