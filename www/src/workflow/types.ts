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
  kind: "hcl" | "snapshot" | "bundle" | "artifact";
  content?: string;
  files?: { path: string; content: string }[];
};
export type Target = { dsn: string; driver?: string; schema?: string };
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
};
export type ApplyInput = {
  hash: string;
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
