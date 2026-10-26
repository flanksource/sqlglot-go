import type { Route } from "../compare/route";
import type { HistoryRecord } from "../types";

/**
 * The route that opens a record: a comparison or patch reopens in the compare
 * screens it was made in, anything else in the history page's detail view.
 */
export function routeForRecord(record: HistoryRecord): Partial<Route> {
  const sides = { reference: record.reference ?? "", target: record.target ?? "", schemas: record.schemas.join(",") };
  const compare = { page: "compare", selected: null, object: "", historyRecord: "", ...sides } as const;
  if (record.kind === "comparison") return { ...compare, stage: "review", run: record.id, patch: "" };
  if (record.kind === "patch" && record.parent) return { ...compare, stage: "patch", run: record.parent, patch: record.id };
  const workflow = { workflowConnection: record.target?.startsWith("connection://") ? record.target : "", workflowSchema: record.schemas.length === 1 ? record.schemas[0] ?? "" : "", workflowDriver: record.dialect ?? "" };
  if (record.kind === "inspection") return { ...workflow, page: "inspect", inspectionRun: record.id, inspectionMode: "catalog" };
  if (record.kind === "migration") return { ...workflow, page: "migrations", migrationRun: record.id };
  return { page: "history", historyRecord: record.id };
}

/** `3 changes · 1 destructive`: the record's counters, zero counters left out. */
export function summaryText(summary: Record<string, number>): string {
  return Object.entries(summary).filter(([, count]) => count > 0).map(([name, count]) => `${count} ${name}`).join(" · ");
}

/** The task group that carries the record's log: an inspection of a comparison logs under that comparison. */
export function taskRunOf(record: HistoryRecord): string {
  return record.kind === "inspection" && record.parent ? record.parent : record.id;
}
