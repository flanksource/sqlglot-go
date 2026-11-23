import { useState, type ReactNode } from "react";
import { Button, Checkbox, Combobox, InputField, Panel } from "@flanksource/clicky-ui/components";
import { Badge, CodeBlock, DataTable } from "@flanksource/clicky-ui/data";
import type { Navigate, Route } from "../compare/route";
import type { ReviewedPlan, ScriptAction } from "./types";

type ScriptRow = {
  action: ScriptAction;
  name: string;
  status: "Run" | "Re-run" | "Blocked" | "Skipped" | "Unchanged";
  reason: string;
  labels: { key: string; value: string }[];
};

export function MigrationScripts({ plan, route, navigate, schema }: {
  plan: ReviewedPlan;
  route: Route;
  navigate: Navigate;
  schema: ReactNode;
}) {
  const rows = scriptRows(plan);
  const labels = new Map<string, string>();
  for (const row of rows) {
    for (const label of row.labels) labels.set(labelToken(label), `${label.key}: ${label.value}`);
  }
  const labelOptions = [...labels].sort(([left], [right]) => left.localeCompare(right))
    .map(([value, label]) => ({ value, label }));
  if (route.migrationLabel && !labels.has(route.migrationLabel)) {
    labelOptions.push({ value: route.migrationLabel, label: route.migrationLabel });
  }
  const query = (route.migrationSearch ?? "").trim().toLocaleLowerCase();
  const visible = rows.filter(({ action, name, status, reason, labels: rowLabels }) =>
    (route.migrationUnchanged === "1" || status !== "Unchanged") &&
    (!query || [name, action.script.id, action.script.path, reason].some(value => value.toLocaleLowerCase().includes(query))) &&
    (!route.migrationLabel || rowLabels.some(label => labelToken(label) === route.migrationLabel)));

  return <>
    <div className="flex flex-wrap items-end gap-3">
      <label className="min-w-52 space-y-1 text-sm">
        <span className="block font-medium">Search scripts</span>
        <InputField aria-label="Search scripts" value={route.migrationSearch ?? ""}
          onChange={(value) => navigate({ migrationSearch: value || undefined })} />
      </label>
      <div className="min-w-44 space-y-1 text-sm">
        <span className="block font-medium">Label</span>
        <Combobox ariaLabel="Filter by label" allowCustomValue={false} value={route.migrationLabel ?? ""}
          options={[{ value: "", label: "All labels" }, ...labelOptions]}
          onChange={(value) => navigate({ migrationLabel: value || undefined })} />
      </div>
      <label className="flex min-h-9 items-center gap-2 text-sm">
        <Checkbox aria-label="Include unchanged" checked={route.migrationUnchanged === "1"}
          onChange={(checked) => navigate({ migrationUnchanged: checked ? "1" : undefined })} />
        Include unchanged
      </label>
    </div>
    <ScriptPhase title="Pre-deployment" rows={visible.filter(row => row.action.script.phase === "pre")}
      emptyMessage={rows.some(row => row.action.script.phase === "pre") ? "No scripts match the current filters." : "No pre-deployment scripts."} />
    {schema}
    <ScriptPhase title="Post-deployment" rows={visible.filter(row => row.action.script.phase === "post")}
      emptyMessage={rows.some(row => row.action.script.phase === "post") ? "No scripts match the current filters." : "No post-deployment scripts."} />
  </>;
}

function labelToken(label: ScriptRow["labels"][number]): string {
  return `${encodeURIComponent(label.key)}=${encodeURIComponent(label.value)}`;
}

function scriptRows(plan: ReviewedPlan): ScriptRow[] {
  const sources = new Map(plan.sources?.map(source => [source.id, source.labels]));
  return (plan.scriptPlan?.actions ?? []).map(action => ({
    action,
    name: action.script.name || action.script.id,
    status: action.blocked?.length ? "Blocked" : action.script.noStatements ? "Skipped" : !action.selected ? "Unchanged" : action.reasons?.includes("new") ? "Run" : "Re-run",
    reason: action.blocked?.join(", ") || (action.script.noStatements ? "No executable statements" : action.reasons?.map(reason => reason.replaceAll("-", " ")).join(", ") || "—"),
    labels: Object.entries(sources.get(action.script.id) ?? {}).flatMap(([key, values]) => values.map(value => ({ key, value }))),
  }));
}

function ScriptPhase({ title, rows, emptyMessage }: { title: string; rows: ScriptRow[]; emptyMessage: string }) {
  const [open, setOpen] = useState("");
  const selected = rows.find(row => row.action.script.id === open);
  return <section aria-label={`${title} scripts`} className="space-y-2">
    <h2 className="text-sm font-semibold">{title}</h2>
    <DataTable<ScriptRow>
      data={rows}
      getRowId={(row) => row.action.script.id}
      emptyMessage={emptyMessage}
      scrollContainerClassName="max-h-[26rem]"
      virtualize={rows.length > 200}
      columns={[
        { key: "name", label: "Script", grow: true, sortable: true,
          render: (_, row) => <Button size="sm" variant="ghost" className="h-auto min-h-7 whitespace-normal px-0 text-left"
            aria-expanded={open === row.action.script.id}
            onClick={() => setOpen(open === row.action.script.id ? "" : row.action.script.id)}>{row.name}</Button> },
        { key: "status", label: "Status", sortable: true, render: (_, row) => row.status === "Blocked" ? <Badge tone="danger">Blocked</Badge> : row.status },
        { key: "reason", label: "Reason" },
        { key: "labels", label: "Labels", render: (_, row) => <Labels labels={row.labels} /> },
      ]}
    />
    {selected && <ScriptDetails row={selected} />}
  </section>;
}

function Labels({ labels }: { labels: ScriptRow["labels"] }) {
  return labels.length ? <span className="flex flex-wrap gap-1">
    {labels.map(({ key, value }) => <span key={JSON.stringify([key, value])} className="rounded border border-border px-1.5 py-0.5 text-xs">{key}: {value}</span>)}
  </span> : <span className="text-muted-foreground">—</span>;
}

function ScriptDetails({ row }: { row: ScriptRow }) {
  const { script, blocked } = row.action;
  return <Panel title={row.name}>
    <dl className="mb-3 grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
      <dt>Script ID</dt><dd className="break-all font-mono">{script.id}</dd>
      <dt>Source</dt><dd className="break-all font-mono">{script.path}</dd>
      {row.labels.length > 0 && <><dt>Labels</dt><dd><Labels labels={row.labels} /></dd></>}
      {script.noStatements ? <><dt>Execution</dt><dd>No executable statements</dd></> :
        script.nonTransactional && <><dt>Execution</dt><dd>Runs outside a transaction</dd></>}
      {!!blocked?.length && <><dt>Blockers</dt><dd><ul className="list-inside list-disc">{blocked.map(reason => <li key={reason}>{reason}</li>)}</ul></dd></>}
    </dl>
    <CodeBlock language="sql" source={script.sql} copyable />
  </Panel>;
}
