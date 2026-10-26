// URL state, mirroring the clicky-ui playground's environment-comparison model:
// `page` and `connection` pick the screen, `cmp.*` carry the comparison and
// `hist.*` the history page's filters and open record.
import { HISTORY_KINDS, type HistoryKind } from "../types";

export type Page = "compare" | "history" | "connections" | "inspect" | "migrations" | "sql";
export type Stage = "setup" | "review" | "patch";

export interface Route {
  inspectionRun?: string;
  inspectionTarget?: string;
  inspectionDatabase?: string;
  inspectionTargetKind?: string;
  sqlObject?: string;
  migrationRun?: string;
  workflowConnection?: string;
  workflowSchema?: string;
  workflowDriver?: string;
  inspectionView?: string;
  workflowObject?: string;
  inspectionMode?: string;
  sqlMode?: string;
  sqlDialect?: string;
  historyLimit?: string;
  outputFormat?: string;
  outputFilter?: string;
  page: Page;
  /** The connection open in the connections page editor. */
  connection: string;
  stage: Stage;
  /** Desired side: `connection://[namespace/]name`. */
  reference: string;
  /** Side to patch: `connection://[namespace/]name`. */
  target: string;
  /** Comma-separated schema filter; empty compares every schema. */
  schemas: string;
  /** Encoded change ids (see selection.ts); null until the user changes the default. */
  selected: string | null;
  /** Key of the object whose diff is shown (see ChangeTree.objectKey). */
  object: string;
  /** Id of the comparison run shown by the review and patch stages. */
  run: string;
  /** Id of the patch run shown by the patch stage. */
  patch: string;
  /** History page filter: record kind; empty lists every kind. */
  historyKind: "" | HistoryKind;
  /** History page filter: a `connection://…` reference matched against either side. */
  historyConnection: string;
  /** History record open in the history page's detail view. */
  historyRecord: string;
}

export type Navigate = (next: Partial<Route>) => void;

const PAGES: readonly Page[] = ["compare", "history", "connections", "inspect", "migrations", "sql"];
const STAGES: readonly Stage[] = ["setup", "review", "patch"];

const PARAMS = {
  inspectionRun: "inspect.run",
  inspectionTarget: "inspect.target",
  inspectionDatabase: "inspect.database",
  inspectionTargetKind: "inspect.target-kind",
  sqlObject: "sql.object",
  migrationRun: "mig.run",
  workflowConnection: "db.connection",
  workflowSchema: "db.schema",
  workflowDriver: "db.driver",
  inspectionView: "inspect.view",
  workflowObject: "db.object",
  inspectionMode: "inspect.mode",
  sqlMode: "sql.mode",
  sqlDialect: "sql.dialect",
  historyLimit: "hist.limit",
  outputFormat: "result.format",
  outputFilter: "result.filter",
  page: "page",
  connection: "connection",
  stage: "cmp.stage",
  reference: "cmp.reference",
  target: "cmp.target",
  schemas: "cmp.schemas",
  selected: "cmp.selected",
  object: "cmp.object",
  run: "cmp.run",
  patch: "cmp.patch",
  historyKind: "hist.kind",
  historyConnection: "hist.connection",
  historyRecord: "hist.record",
} as const satisfies Record<keyof Route, string>;

const DEFAULTS: Route = {
  page: "compare",
  connection: "",
  stage: "setup",
  reference: "",
  target: "",
  schemas: "",
  selected: null,
  object: "",
  run: "",
  patch: "",
  historyKind: "",
  historyConnection: "",
  historyRecord: "",
};

export function parseRoute(_pathname: string, search: string): Route {
  const params = new URLSearchParams(search);
  const route: Route = { ...DEFAULTS };
  for (const key of Object.keys(PARAMS) as (keyof Route)[]) {
    const value = params.get(PARAMS[key]);
    if (value !== null) Object.assign(route, { [key]: value });
  }
  if (!PAGES.includes(route.page)) throw new Error(`Unknown page: ${route.page}`);
  if (!STAGES.includes(route.stage)) throw new Error(`Unknown comparison stage: ${route.stage}`);
  if (route.historyKind !== "" && !HISTORY_KINDS.includes(route.historyKind)) throw new Error(`Unknown history kind: ${route.historyKind}`);
  if (route.historyLimit !== undefined && (!/^[1-9]\d*$/.test(route.historyLimit) || !Number.isSafeInteger(Number(route.historyLimit)))) throw new Error("History limit must be a positive integer");
  return route;
}

export function buildRoute(route: Route): string {
  const url = new URL(window.location.href);
  for (const key of Object.keys(PARAMS) as (keyof Route)[]) {
    const value = route[key];
    if (value === DEFAULTS[key] || ((key === "reference" || key === "target" || key === "workflowConnection") && !value?.startsWith("connection://"))) url.searchParams.delete(PARAMS[key]);
    else url.searchParams.set(PARAMS[key], value as string);
  }
  return `${url.pathname}${url.search}${url.hash}`;
}
