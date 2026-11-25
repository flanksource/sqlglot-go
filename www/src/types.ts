// Wire types of the sqlglot ui API (POST /api/v1/schema/compare and /schema/patch).
import type { EngineArtifact } from "./workflow/types";

export type Name = { schema: string; name: string };

export type RuntimeDependency = { server?: string; database?: string; schema?: string; name: string };

export type ChangeAction = "add" | "drop" | "modify";

type ChangeFields = {
  id: string;
  kind: string;
  object: Name;
  member?: string;
  destructive: boolean;
  reason?: string;
  /** Flags a module whose references bind only at runtime. */
  runtimeDependencies?: RuntimeDependency[];
  /** The change IDs that must be selected with this one. */
  requires?: string[];
};

/** A change the diff itself names; a forced rebuild is a `RebuildChange`, never one of these. */
export type Change = ChangeFields & { action: ChangeAction };

/** A dependent (index, key, check, default, statistic, view) dropped and re-created around the change `cause`. */
export type RebuildChange = ChangeFields & { action: "rebuild"; cause: string };

export type CompareStep = { changes: string[]; sql: string };

export type ObjectStatus = "added" | "deleted" | "changed";

export type ObjectLanguage = "sql" | "json" | "hcl";

export type ObjectDiff = {
  kind: string;
  name: Name;
  status: ObjectStatus;
  before?: string;
  after?: string;
  language: ObjectLanguage;
  changes: string[];
};

export type CompareResult = {
  dialect: string;
  reference: string;
  target: string;
  schemas: string[];
  /** The differences a reviewer selects from; forced rebuilds are not among them. */
  changes: Change[];
  /** The dependents the changes force to be dropped and re-created; each names its change in `cause` and is never selected alone. */
  rebuilds?: RebuildChange[];
  steps: CompareStep[];
  objects: ObjectDiff[];
  diffHash: string;
};

export type PatchResult = {
  dialect: string;
  artifact: EngineArtifact;
  sql: string;
  hash: string;
  acks: string[];
  selection: string[];
};

/** `run` is a client-chosen id (crypto.randomUUID); the server refuses a reused one. */
export type CompareRequest = { reference: string; target: string; schemas?: string[]; run: string };

/** `comparison` is the comparison run the selection was reviewed in; `run` is the new patch run. */
export type PatchRequest = CompareRequest & { comparison: string; select: string[]; diffHash: string };

export const HISTORY_KINDS = ["inspection", "comparison", "patch", "migration"] as const;
export type HistoryKind = (typeof HISTORY_KINDS)[number];

export type HistoryStatus = "succeeded" | "failed";

/** One recorded run. The list endpoint omits `payload`; GET /history/{id} carries it. */
export type HistoryRecord = {
  id: string;
  kind: HistoryKind;
  parent?: string;
  dialect?: string;
  reference?: string;
  target?: string;
  schemas: string[];
  status: HistoryStatus;
  error?: string;
  /** RFC3339 */
  createdAt: string;
  summary: Record<string, number>;
  payload?: unknown;
};

export type HistoryFilter = { kind?: HistoryKind; connection?: string; limit?: number };

export type IndexColumn = { name: string; descending?: boolean };
export type Column = {
  name: string;
  type: string;
  nullable: boolean;
  identity?: { seed: number; increment: number };
  default?: { name: string; expression: string; systemNamed?: boolean };
  collation?: string;
  rowGuid?: boolean;
};
export type Index = {
  name: string;
  columns: IndexColumn[];
  included?: string[];
  unique?: boolean;
  primary?: boolean;
  clustered?: boolean;
  constraint?: boolean;
  filter?: string;
  fillFactor?: number;
  padIndex?: boolean;
  ignoreDupKey?: boolean;
  noRowLocks?: boolean;
  noPageLocks?: boolean;
  noRecompute?: boolean;
  compression?: string;
  filegroup?: string;
};
export type ForeignKey = {
  name: string;
  columns: string[];
  references: Name;
  referencedColumns: string[];
  onDelete?: string;
  onUpdate?: string;
  notForReplication?: boolean;
};
export type Check = { name: string; expression: string; notForReplication?: boolean };
export type Table = { name: Name; columns: Column[]; indexes?: Index[]; foreignKeys?: ForeignKey[]; checks?: Check[] };
export type Definition = {
  name: Name;
  kind: string;
  sql: string;
  parent?: Name;
  disabled?: boolean;
  quotedIdentifierOff?: boolean;
  dependencies?: Name[];
  runtimeDependencies?: RuntimeDependency[];
};
export type Sequence = {
  name: Name;
  type: string;
  start: string;
  increment: string;
  minimum: string;
  maximum: string;
  cycle?: boolean;
  noCache?: boolean;
  cacheSize?: number;
};
export type TableType = { name: Name; columns: Column[]; keys?: Index[]; checks?: string[] };
/** A dialect's schema; `native` is its own lossless description (Atlas HCL) when the other fields cannot hold it. */
export type Schema = {
  tables: Table[];
  definitions: Definition[];
  sequences?: Sequence[];
  tableTypes?: TableType[];
  native?: string;
};

/** An object or member the inspector could not read faithfully. */
export type Refusal = { object: Name; member?: string; reason: string };

/** What `inspect --snapshot` returns, and what a "snapshot" migration source holds, for every dialect. */
export type InspectSnapshot = { dialect: string; schemas: string[]; schema: Schema; refusals?: Refusal[] };
