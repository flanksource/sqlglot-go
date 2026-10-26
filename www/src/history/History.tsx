import { Combobox, InputField, SegmentedControl, type SegmentedOption } from "@flanksource/clicky-ui/components";
import { Badge, Callout, Timestamp } from "@flanksource/clicky-ui/data";
import { useConnectionDashboard } from "@flanksource/clicky-ui/connections";
import { API_BASE } from "../api";
import { connectionOption } from "../compare/Setup";
import { useHistoryList } from "../compare/queries";
import { buildRoute, type Navigate, type Route } from "../compare/route";
import { HISTORY_KINDS, type HistoryKind, type HistoryRecord } from "../types";
import { connectionName } from "./format";
import { routeForRecord, summaryText } from "./navigation";
import { RecordView } from "./RecordView";

type KindFilter = "" | HistoryKind;

const KIND_OPTIONS: SegmentedOption<KindFilter>[] = [{ id: "", label: "All" }, ...HISTORY_KINDS.map((kind) => ({ id: kind, label: kind.charAt(0).toUpperCase() + kind.slice(1) }))];

function Sides({ record }: { record: HistoryRecord }) {
  if (!record.reference && !record.target) return <span className="text-muted-foreground">—</span>;
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5" title={[record.reference, record.target].filter(Boolean).join(" → ")}>
      {record.reference && <span className="truncate">{connectionName(record.reference)}</span>}
      {record.reference && record.target && <span aria-hidden className="text-muted-foreground">→</span>}
      {record.target && <span className="truncate">{connectionName(record.target)}</span>}
    </span>
  );
}

function Row({ record, route, navigate }: { record: HistoryRecord; route: Route; navigate: Navigate }) {
  const next = routeForRecord(record);
  const summary = summaryText(record.summary);
  return (
    <tr className="border-b border-border align-top last:border-0 hover:bg-accent/40" data-history-record={record.id}>
      <td className="px-3 py-2 text-xs">
        <a href={buildRoute({ ...route, ...next })} className="text-primary hover:underline" onClick={(event) => { event.preventDefault(); navigate(next); }}>
          <Timestamp value={record.createdAt} format="relative" className="text-inherit" />
        </a>
      </td>
      <td className="px-3 py-2 text-xs">{record.kind}</td>
      <td className="px-3 py-2"><Badge tone={record.status === "failed" ? "danger" : "success"} size="xs">{record.status}</Badge></td>
      <td className="max-w-xs px-3 py-2 text-xs"><Sides record={record} /></td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{record.dialect ?? "—"}</td>
      <td className="px-3 py-2 text-xs">
        {summary && <span className="tabular-nums text-muted-foreground">{summary}</span>}
        {record.error && <p className="text-destructive" data-history-error="">{record.error}</p>}
      </td>
    </tr>
  );
}

function Filters({ route, navigate }: { route: Route; navigate: Navigate }) {
  const dashboard = useConnectionDashboard({ apiBase: API_BASE });
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-end gap-3">
        <SegmentedControl<KindFilter> aria-label="Kind" size="sm" value={route.historyKind} options={KIND_OPTIONS} onChange={(historyKind) => navigate({ historyKind })} />
        <div className="w-72">
          <Combobox ariaLabel="Connection" size="sm" value={route.historyConnection} onChange={(historyConnection) => navigate({ historyConnection })}
            options={(dashboard.data?.connections ?? []).map(connectionOption)} loading={dashboard.isPending} placeholder="Any connection" />
        </div>
        <label className="block w-28 text-xs">Maximum records<InputField aria-label="Maximum records" type="number" min={1} value={route.historyLimit ?? "200"} onChange={(value) => { if (/^[1-9]\d*$/.test(value)) navigate({ historyLimit: value }); }} /></label>
      </div>
      {dashboard.isError && <Callout variant="caution" title="Connections could not be loaded">{dashboard.error.message}. Type a connection reference to filter by it.</Callout>}
    </div>
  );
}

function RecordTable({ route, navigate }: { route: Route; navigate: Navigate }) {
  const list = useHistoryList({
    ...(route.historyLimit ? { limit: Number(route.historyLimit) } : {}),
    ...(route.historyKind ? { kind: route.historyKind } : {}),
    ...(route.historyConnection ? { connection: route.historyConnection } : {}),
  });
  if (list.isPending) return <p role="status" className="py-10 text-center text-sm text-muted-foreground">Loading the history…</p>;
  if (list.isError) return <Callout variant="caution" title="The history could not be loaded">{list.error.message}</Callout>;
  return (
    <div className="overflow-x-auto rounded-lg border border-border bg-card">
      <table className="w-full text-left text-sm" aria-label="History">
        <thead className="border-b border-border text-xs text-muted-foreground">
          <tr>{["When", "Kind", "Status", "Reference → target", "Dialect", "Summary"].map((name) => <th key={name} scope="col" className="px-3 py-2 font-medium">{name}</th>)}</tr>
        </thead>
        <tbody>
          {list.data.length === 0 ? (
            <tr><td colSpan={6} className="px-3 py-8 text-center text-sm text-muted-foreground">No recorded run matches these filters.</td></tr>
          ) : (
            list.data.map((record) => <Row key={record.id} record={record} route={route} navigate={navigate} />)
          )}
        </tbody>
      </table>
    </div>
  );
}

export function History({ route, navigate }: { route: Route; navigate: Navigate }) {
  if (route.historyRecord) return <RecordView route={route} navigate={navigate} />;
  return (
    <div className="mx-auto max-w-6xl space-y-4 py-4" data-history-page="">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold">History</h1>
        <p className="text-sm text-muted-foreground">Every inspection, comparison, patch and migration that was run. Reading it never contacts a database.</p>
      </header>
      <Filters route={route} navigate={navigate} />
      <RecordTable route={route} navigate={navigate} />
    </div>
  );
}
