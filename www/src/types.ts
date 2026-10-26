// Wire types of the sqlglot ui API (POST /api/v1/schema/compare and /schema/patch).

export type Name = { schema: string; name: string };

export type ChangeAction = "add" | "drop" | "modify";

export type Change = {
  id: string;
  action: ChangeAction;
  kind: string;
  object: Name;
  member?: string;
  destructive: boolean;
  reason?: string;
  requires?: string[];
};

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
  changes: Change[];
  steps: CompareStep[];
  objects: ObjectDiff[];
  diffHash: string;
};

export type PatchResult = {
  dialect: string;
  artifact: unknown;
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

/** A migration record's payload; a failed run may keep only the partial `result` it reached. */
export type MigrationPayload = { mode?: "plan" | "apply"; hash?: string; acks?: string[]; artifact?: unknown; result?: unknown };
