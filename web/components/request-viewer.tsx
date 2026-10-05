"use client";

import { useMemo, useState } from "react";
import { Braces } from "lucide-react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Chip,
  Disclosure,
  ExpandControls,
  ExpansionProvider,
  FieldList,
  JsonTree,
  RawPanel,
  Transcript,
  type Expansion,
} from "@/components/transcript";
import { formatBytes, requestView } from "@/lib/request-viewer";

const plural = (n: number, one: string, many = `${one}s`) =>
  `${n.toLocaleString()} ${n === 1 ? one : many}`;

export function RequestViewer({ route, raw }: { route: string; raw: string }) {
  const view = useMemo(() => requestView(route, raw), [route, raw]);
  const [expansion, setExpansion] = useState<Expansion>({
    open: null,
    generation: 0,
  });
  const { toolCalls, images, attachments } = view.counts;
  return (
    <section aria-label="Request browser" className="min-w-0 space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="mr-1 text-sm font-semibold">Request</h3>
        <Chip>{view.protocol} API</Chip>
        {view.model && <Chip className="font-medium">{view.model}</Chip>}
        <Chip>{plural(view.turns.length, "turn")}</Chip>
        {toolCalls > 0 && <Chip>{plural(toolCalls, "tool call")}</Chip>}
        {images > 0 && <Chip>{plural(images, "image")}</Chip>}
        {attachments > 0 && <Chip>{plural(attachments, "file")}</Chip>}
        <Chip>{formatBytes(view.bytes)}</Chip>
      </div>
      <ExpansionProvider value={expansion}>
        <Tabs keepMounted defaultValue="conversation">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <TabsList aria-label="Request view" className="h-auto flex-wrap">
              <TabsTrigger value="conversation">Conversation</TabsTrigger>
              <TabsTrigger value="tools">
                Tools
                <span className="tabular-nums text-muted-foreground">
                  {view.tools.length}
                </span>
              </TabsTrigger>
              <TabsTrigger value="parameters">Parameters</TabsTrigger>
              <TabsTrigger value="raw">
                <Braces className="size-3.5" aria-hidden="true" />
                Raw
              </TabsTrigger>
            </TabsList>
            <ExpandControls
              onChange={(open) =>
                setExpansion((previous) => ({
                  open,
                  generation: previous.generation + 1,
                }))
              }
            />
          </div>
          <TabsContent value="conversation" className="space-y-4">
            {view.warnings.map((warning) => (
              <p
                key={warning}
                role="status"
                className="rounded-md border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900"
              >
                {warning}
              </p>
            ))}
            {view.turns.length > 0 ? (
              <Transcript turns={view.turns} label="Request conversation" />
            ) : (
              !view.warnings.length && (
                <p className="text-sm text-muted-foreground">
                  This request has no messages. Its fields are under Parameters.
                </p>
              )
            )}
          </TabsContent>
          <TabsContent value="tools">
            {view.tools.length ? (
              <ul className="divide-y rounded-md border bg-card">
                {view.tools.map((tool, i) => (
                  <li key={i} className="min-w-0 px-3 py-2.5">
                    <Disclosure
                      summary={
                        <code className="min-w-0 break-all font-mono text-[13px] font-medium">
                          {tool.name}
                        </code>
                      }
                      meta={tool.type === "function" ? undefined : tool.type}
                    >
                      <div className="space-y-2 pl-5">
                        {tool.schema !== undefined ? (
                          <JsonTree value={tool.schema} label="Input schema" />
                        ) : (
                          <JsonTree value={tool.raw} label="Definition" />
                        )}
                      </div>
                    </Disclosure>
                    {tool.description && (
                      <p className="mt-0.5 line-clamp-2 max-w-[72ch] pl-5 text-sm text-muted-foreground">
                        {tool.description}
                      </p>
                    )}
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-sm text-muted-foreground">
                No tools were offered to the model in this request.
              </p>
            )}
          </TabsContent>
          <TabsContent value="parameters">
            <FieldList fields={view.parameters} />
          </TabsContent>
          <TabsContent value="raw">
            <RawPanel
              text={raw}
              label="Raw request"
              description="The request body exactly as the proxy received it."
            />
          </TabsContent>
        </Tabs>
      </ExpansionProvider>
    </section>
  );
}
