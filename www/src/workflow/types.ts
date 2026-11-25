import type { Change, Name, RebuildChange, Refusal, Schema, Check, ForeignKey, Index } from "../types";

export type SQLMode = "parse" | "validate" | "format" | "tokens";
export type SQLDiagnostic = {
  label: string;
  kind: string;
  line: number;
  column: number;
  start: number;
  end: number;
  excerpt?: string;
};
export type Source = {
  kind: "hcl" | "snapshot" | "bundle" | "artifact" | "project";
  content?: string;
  files?: { path: string; content: string }[];
};
export type Target = { dsn: string; driver?: string; schema?: string };
export type ProjectSource = {
  id: string;
  path: string;
  kind: "hcl" | "sql";
  name?: string;
  phase?: string;
  labels?: Record<string, string[]>;
};

/** The fields of a script plan action the UI reads; the rest of `scripts.Pending` (state, reads, effects) is not shown. */
export type ScriptAction = {
  script: {
    id: string;
    name: string;
    path: string;
    phase: "pre" | "post";
    sql: string;
    noStatements?: boolean;
    nonTransactional?: boolean;
  };
  selected: boolean;
  reasons?: string[];
  blocked?: string[];
};

// The reviewed engine artifact (migrate/engine): ordered, grouped SQL bound to the snapshots it was planned from.

export type ScopeMode = "declared" | "complete";
/** What a plan manages: `objects` are the managed objects in declared mode. */
export type Scope = { mode: ScopeMode; schemas?: string[]; objects?: Name[] };
export type Phase = "drop" | "alter" | "create";
export type Statement = { sql: string; nonTransactional?: boolean; sizeOfData?: boolean };
/** The statements one change contributes in one phase. */
export type Op = { id: string; change: string; phase: Phase; object: Name; statements: Statement[]; requires?: string[] };
/** A data check that passes when its SQL returns no rows. */
export type Precondition = { id: string; change: string; sql: string; message: string };
export type EngineUnit = {
  id: string;
  changes: string[];
  objects: Name[];
  ops: Op[];
  preconditions?: Precondition[];
  transactional: boolean;
};
export type Capabilities = { fingerprint: string; transactionalDdl: boolean; features?: Record<string, boolean> };
export type ColumnRef = { table: Name; column: string };
export type DependentKind =
  | "index"
  | "primary-key"
  | "unique-constraint"
  | "foreign-key"
  | "check"
  | "default"
  | "statistic"
  | "view"
  | "table";
export type Dependent = {
  kind: DependentKind;
  table: Name;
  name: string;
  on?: ColumnRef[];
  index?: Index;
  foreignKey?: ForeignKey;
  check?: Check;
  definition?: string;
  refusal?: string;
};
export type EngineArtifact = {
  version: 2;
  dialect: string;
  capabilities: Capabilities;
  scope: Scope;
  baseline: Schema;
  desired: Schema;
  context?: Name[];
  selection?: string[];
  dependents?: Dependent[];
  refusals?: Refusal[];
  warnings?: string[];
  /** The planned changes, forced rebuilds included. */
  changes: (Change | RebuildChange)[];
  /** The drops left unplanned because the scope is declared. */
  preserved?: Change[];
  units: EngineUnit[];
  atomic?: boolean;
  hash: string;
};
/** A bundle preview's plan file: the artifact with the sources and warnings it was planned from. */
export type BundlePlanFile = { artifact: EngineArtifact; warnings?: string[]; advisory?: string[]; sources?: ProjectSource[] };

// The SQL Server script plan file (version 3).

export type ReadAnswer = { digest?: string; rows: number; value?: string | null; error?: string };
export type ScriptState = {
  version: number;
  sourceHash: string;
  policyHash: string;
  effectsHash?: string;
  hash: string;
  inputs: Record<string, string>;
  values?: Record<string, string | null>;
  reads?: Record<string, ReadAnswer>;
  outcome?: string;
  lastSuccess?: ScriptState;
};
export type ScriptPlanFile = {
  kind: "sqlglot-go/script-plan";
  version: 3;
  target: { server: string; database: string; scope: string };
  bundle: {
    scripts: ScriptAction["script"][];
    functions?: { name: string; type: string; nullable: boolean; prerequisites?: string[] }[];
    /** The declared-schema step, an engine artifact; absent when the bundle declares no objects. */
    schema?: EngineArtifact;
  };
  ledger: Record<string, ScriptState>;
  absorbed?: string[];
  observations: Record<string, string>;
  actions: ScriptAction[];
  warnings?: string[];
  hash: string;
};

// The result of reviewing a plan (workflow.PlanResult).

/** One statement of the plan, in execution order. `change`, `unit` and `phase` say what implements it and where it runs. */
export type PlanStep = {
  id: string;
  sql: string;
  destructive: boolean;
  change?: string;
  unit?: string;
  phase?: Phase;
  nonTransactional?: boolean;
};
/** A planned change with the rebuilds it forces, each named by its own change with `cause` set to this one. */
export type PlanChange = Change & { rebuilds?: RebuildChange[] };
/** One transaction of the plan, with the data checks it runs first; `steps` are the IDs of its statements, in order. */
export type PlanUnit = {
  id: string;
  changes: string[];
  objects: Name[];
  transactional: boolean;
  preconditions?: Precondition[];
  steps: string[];
};
export type ReviewedPlan = {
  dialect: string;
  schema: string;
  /** The engine artifact; for a SQL Server script bundle, the script plan file. */
  artifact: EngineArtifact | ScriptPlanFile;
  hash: string;
  /** The IDs of the destructive changes apply must acknowledge. */
  acks: string[];
  steps: PlanStep[];
  changes: PlanChange[];
  warnings: string[];
  /** False for an advisory plan, which can be reviewed but not applied. */
  applicable: boolean;
  /** Why the plan is not applicable; a saved plan file must carry it. */
  advisory?: string[];
  sql: string;
  sources?: ProjectSource[];
  scope?: Scope;
  units?: PlanUnit[];
  preserved?: Change[];
  refusals?: Refusal[];
  scriptPlan?: ScriptPlanFile;
};
export type ApplyInput = {
  ack: string[];
  "lock-timeout": string;
};

// The result of applying a plan.

export type UnitOutcome = "applied" | "already-applied" | "failed" | "commit-unknown";
export type UnitResult = { id: string; outcome: UnitOutcome; statements: number };
/** A schema artifact's apply result; a partial one comes back together with an error. */
export type ApplyResult = { hash: string; outcome: UnitOutcome; steps: number; units?: UnitResult[] };
export type ScriptFailure = { script: string; outcome: string; error: string };
export type ScriptApplyResult = {
  outcome: "applied" | "already-applied" | "applied-with-warnings" | "stale" | "failed" | "partial" | "commit-unknown";
  applied: string[];
  /** applied when the schema step committed units, partial when it committed some and then failed. */
  schema?: "applied" | "partial";
  blocked?: string[];
  failed?: ScriptFailure[];
  skipped?: { script: string; dependency: string }[];
  warnings?: ScriptFailure[];
};
export type MigrationPayload = {
  mode: "plan" | "apply";
  plan?: ReviewedPlan;
  hash?: string;
  acks?: string[];
  artifact?: EngineArtifact | ScriptPlanFile;
  result?: ApplyResult | ScriptApplyResult;
};

export function isScriptApplyResult(result: ApplyResult | ScriptApplyResult): result is ScriptApplyResult {
  return "applied" in result;
}
