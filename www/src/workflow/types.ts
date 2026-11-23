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
  labels?: Record<string, string[]>;
};
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
export type ReviewedPlan = {
  dialect: string;
  schema: string;
  artifact: unknown;
  hash: string;
  acks: string[];
  steps: { id: string; sql: string; destructive: boolean }[];
  changes: unknown[];
  warnings: string[];
  applicable: boolean;
  sql: string;
  sources?: ProjectSource[];
  scriptPlan?: { actions: ScriptAction[] };
};
export type ApplyInput = {
  ack: string[];
  "lock-timeout": string;
};
export type MigrationPayload = {
  mode?: "plan" | "apply";
  plan?: ReviewedPlan;
  artifact?: unknown;
  result?: {
    hash: string;
    outcome: "applied" | "already-applied" | "failed" | "commit-unknown";
    steps: number;
  };
};
