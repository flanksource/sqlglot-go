import type { ReactNode } from "react";
import { Button, CopyButton, Panel } from "@flanksource/clicky-ui/components";
import { Badge, Callout, CodeBlock } from "@flanksource/clicky-ui/data";
import { UiArrowLeft } from "@flanksource/clicky-ui/icons";
import { isNotFound } from "../api";
import { useHistoryRecord, useRunKnown } from "../compare/queries";
import { RunProgress } from "../compare/RunProgress";
import type { Navigate, Route } from "../compare/route";
import type { HistoryRecord } from "../types";
import { connectionName, recordedAt } from "./format";
import { routeForRecord, summaryText, taskRunOf } from "./navigation";
import { asMigrationPayload, inspectionView } from "./payload";
import { ResultExport } from "../workflow/ResultExport";

const json = (value: unknown) => JSON.stringify(value, null, 2);

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return <><dt className="text-muted-foreground">{label}</dt><dd className="min-w-0">{children}</dd></>;
}

function Metadata({ record, navigate }: { record: HistoryRecord; navigate: Navigate }) {
  const summary = summaryText(record.summary);
  const parent = record.parent;
  return (
    <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-[auto_1fr]" aria-label="Record">
      <Fact label="Id"><span className="flex items-center gap-1"><code className="truncate font-mono text-xs">{record.id}</code><CopyButton value={record.id} label="Copy id" /></span></Fact>
      <Fact label="Kind">{record.kind}</Fact>
      <Fact label="Status"><Badge tone={record.status === "failed" ? "danger" : "success"} size="xs">{record.status}</Badge></Fact>
      <Fact label="Recorded">{recordedAt(record.createdAt)}</Fact>
      {record.dialect && <Fact label="Dialect">{record.dialect}</Fact>}
      {record.reference && <Fact label="Reference">{connectionName(record.reference)}</Fact>}
      {record.target && <Fact label="Target">{connectionName(record.target)}</Fact>}
      {record.schemas.length > 0 && <Fact label="Schemas">{record.schemas.join(", ")}</Fact>}
      {parent && <Fact label="Parent run"><Button size="sm" variant="ghost" onClick={() => navigate({ historyRecord: parent })}>{parent}</Button></Fact>}
      {summary && <Fact label="Summary"><span className="tabular-nums">{summary}</span></Fact>}
    </dl>
  );
}

function MigrationPayloadView({ payload }: { payload: unknown }) {
  const migration = asMigrationPayload(payload);
  return (
    <div className="space-y-3">
      <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-[auto_1fr]" aria-label="Migration">
        {migration.mode && <Fact label="Mode">{migration.mode}</Fact>}
        {migration.hash && <Fact label="Hash"><code className="break-all font-mono text-xs">{migration.hash}</code></Fact>}
        {migration.acks && migration.acks.length > 0 && (
          <Fact label="Acknowledged"><ul>{migration.acks.map((ack) => <li key={ack}><code className="break-all font-mono text-xs">{ack}</code></li>)}</ul></Fact>
        )}
      </dl>
      {migration.artifact !== undefined && <Panel title="Artifact" padded={false}><CodeBlock language="json" source={json(migration.artifact)} bare className="max-h-96 overflow-auto p-3" /></Panel>}
      {migration.result !== undefined && <Panel title="Result" padded={false}><CodeBlock language="json" source={json(migration.result)} bare className="max-h-96 overflow-auto p-3" /></Panel>}
    </div>
  );
}

function Payload({ record }: { record: HistoryRecord }) {
  if (record.payload === undefined) {
    return record.status === "failed" ? null : <Callout variant="warning" title="No payload was recorded">This run succeeded but kept no payload to show.</Callout>;
  }
  if (record.kind === "migration") return <MigrationPayloadView payload={record.payload} />;
  const view = record.kind === "inspection" ? inspectionView(record.payload) : { language: "json", source: json(record.payload) };
  return (
    <Panel title={record.kind === "inspection" ? "Inspected schema" : "Payload"} actions={<CopyButton value={view.source} label="Copy payload" />} padded={false}>
      <CodeBlock language={view.language} source={view.source} bare className="max-h-[32rem] overflow-auto p-3" />
    </Panel>
  );
}

function RecordLog({ record }: { record: HistoryRecord }) {
  const run = taskRunOf(record);
  const known = useRunKnown(run);
  if (known.isError) return <Callout variant="caution" title="The run log could not be loaded">{known.error.message}</Callout>;
  return known.data === true ? <RunProgress run={run} title="Run log" /> : null;
}

export function RecordView({ route, navigate }: { route: Route; navigate: Navigate }) {
  const query = useHistoryRecord(route.historyRecord);
  const back = <Button size="sm" variant="ghost" onClick={() => navigate({ historyRecord: "" })}><UiArrowLeft className="size-4" />History</Button>;
  if (query.isPending) return <p role="status" className="py-10 text-center text-sm text-muted-foreground">Loading the record…</p>;
  if (query.isError) {
    return (
      <div className="mx-auto max-w-4xl space-y-3 py-4">
        <Callout variant={isNotFound(query.error) ? "warning" : "caution"} title={isNotFound(query.error) ? "This record was not found" : "The record could not be loaded"}>{query.error.message}</Callout>
        {back}
      </div>
    );
  }
  const record = query.data;
  const open = routeForRecord(record);
  return (
    <div className="mx-auto max-w-4xl space-y-4 py-4" data-history-record-view={record.id}>
      <header className="flex items-center gap-2">
        {back}
        {open.page !== "history" && <Button size="sm" className="ml-auto" onClick={() => navigate(open)}>Open {record.kind}</Button>}
      </header>
      <Metadata record={record} navigate={navigate} />
      {record.error && <Callout variant="caution" title={`The ${record.kind} failed`}>{record.error}</Callout>}
      <Payload record={record} />
      {record.payload != null && <ResultExport value={record.payload} route={route} navigate={navigate} />}
      <RecordLog record={record} />
    </div>
  );
}
