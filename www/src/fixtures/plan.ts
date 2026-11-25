// A reviewed plan as `POST /migrate/plan` returns it for a declared-scope SQL Server schema: one retype that forces
// three rebuilds, a second change in a unit of its own that cannot run in a transaction, a data check, a preserved
// drop and a refusal. Shared by the review tests and the dev mock API.
import type { Change, RebuildChange } from "../types";
import type { ApplyResult, EngineArtifact, PlanStep, PlanUnit, ReviewedPlan, ScriptAction, ScriptPlanFile } from "../workflow/types";

const orders = { schema: "dbo", name: "orders" };
const events = { schema: "dbo", name: "events" };

export const RETYPE = "modify-column-orders-amount";
export const ADD_INDEX = "add-index-events-at";

const retype: Change = {
  id: RETYPE,
  action: "modify",
  kind: "column",
  object: orders,
  member: "amount",
  destructive: false,
  reason: "retypes orders.amount from money to decimal(19,4)",
};
const addIndex: Change = { id: ADD_INDEX, action: "add", kind: "index", object: events, member: "IX_events_at", destructive: false };

export const rebuilds: RebuildChange[] = [
  { id: "rebuild-index-orders-customer-amount", action: "rebuild", kind: "index", object: orders, member: "IX_orders_customer_amount", destructive: false, cause: RETYPE },
  { id: "rebuild-foreign-key-orders-limits", action: "rebuild", kind: "foreign-key", object: orders, member: "FK_orders_amount_limits", destructive: false, cause: RETYPE },
  { id: "rebuild-check-orders-amount-positive", action: "rebuild", kind: "check", object: orders, member: "CK_orders_amount_positive", destructive: false, cause: RETYPE },
];
const [rebuildIndex, rebuildKey, rebuildCheck] = rebuilds as [RebuildChange, RebuildChange, RebuildChange];

export const preservedDrop: Change = {
  id: "drop-table-legacy-audit",
  action: "drop",
  kind: "table",
  object: { schema: "dbo", name: "legacy_audit" },
  destructive: true,
  reason: "drops table dbo.legacy_audit and its data",
};

export const refusal = { object: { schema: "dbo", name: "fn_orders_total" }, reason: "a CLR function cannot be read faithfully" };

export const precondition = {
  id: "pre-amount-fits",
  change: RETYPE,
  sql: "SELECT TOP (1) 1 FROM dbo.orders WHERE amount > 922337203685477",
  message: "dbo.orders.amount holds values that do not fit decimal(19,4)",
};

const unitOne = {
  id: "unit-retype-orders",
  changes: [RETYPE, rebuildIndex.id, rebuildKey.id, rebuildCheck.id],
  objects: [orders],
  transactional: true,
  preconditions: [precondition],
  ops: [
    { id: `${rebuildKey.id}/drop`, change: rebuildKey.id, phase: "drop" as const, object: orders, statements: [{ sql: "ALTER TABLE dbo.orders DROP CONSTRAINT FK_orders_amount_limits" }] },
    { id: `${rebuildCheck.id}/drop`, change: rebuildCheck.id, phase: "drop" as const, object: orders, statements: [{ sql: "ALTER TABLE dbo.orders DROP CONSTRAINT CK_orders_amount_positive" }] },
    { id: `${rebuildIndex.id}/drop`, change: rebuildIndex.id, phase: "drop" as const, object: orders, statements: [{ sql: "DROP INDEX IX_orders_customer_amount ON dbo.orders" }] },
    { id: `${RETYPE}/alter`, change: RETYPE, phase: "alter" as const, object: orders, statements: [{ sql: "ALTER TABLE dbo.orders ALTER COLUMN amount decimal(19,4) NOT NULL" }] },
    { id: `${rebuildIndex.id}/create`, change: rebuildIndex.id, phase: "create" as const, object: orders, statements: [{ sql: "CREATE INDEX IX_orders_customer_amount ON dbo.orders (customer_id, amount)" }] },
    { id: `${rebuildCheck.id}/create`, change: rebuildCheck.id, phase: "create" as const, object: orders, statements: [{ sql: "ALTER TABLE dbo.orders ADD CONSTRAINT CK_orders_amount_positive CHECK (amount > 0)" }] },
    { id: `${rebuildKey.id}/create`, change: rebuildKey.id, phase: "create" as const, object: orders, statements: [{ sql: "ALTER TABLE dbo.orders ADD CONSTRAINT FK_orders_amount_limits FOREIGN KEY (amount) REFERENCES dbo.limits (amount)" }] },
  ],
};
const unitTwo = {
  id: "unit-index-events",
  changes: [ADD_INDEX],
  objects: [events],
  transactional: false,
  ops: [
    {
      id: `${ADD_INDEX}/create`,
      change: ADD_INDEX,
      phase: "create" as const,
      object: events,
      statements: [{ sql: "CREATE INDEX IX_events_at ON dbo.events (at) WITH (ONLINE = ON)", nonTransactional: true }],
    },
  ],
};

export const artifact: EngineArtifact = {
  version: 2,
  dialect: "sqlserver",
  capabilities: { fingerprint: "sqlserver-2022", transactionalDdl: true },
  scope: { mode: "declared", schemas: ["dbo"], objects: [orders, events] },
  baseline: { tables: [], definitions: [] },
  desired: { tables: [], definitions: [] },
  changes: [retype, ...rebuilds, addIndex],
  preserved: [preservedDrop],
  refusals: [refusal],
  units: [unitOne, unitTwo],
  hash: "sha256:retype-orders",
};

const steps: PlanStep[] = artifact.units.flatMap((unit) =>
  unit.ops.flatMap((op) =>
    op.statements.map((statement, index) => {
      const step: PlanStep = { id: `${op.id}/${index + 1}`, sql: statement.sql, destructive: false, change: op.change, unit: unit.id, phase: op.phase };
      if (statement.nonTransactional) step.nonTransactional = true;
      return step;
    }),
  ),
);

const units: PlanUnit[] = artifact.units.map((unit) => ({
  id: unit.id,
  changes: unit.changes,
  objects: unit.objects,
  transactional: unit.transactional,
  ...(unit.preconditions ? { preconditions: unit.preconditions } : {}),
  steps: steps.filter((step) => step.unit === unit.id).map((step) => step.id),
}));

export const reviewedPlan: ReviewedPlan = {
  dialect: "sqlserver",
  schema: "dbo",
  artifact,
  hash: artifact.hash,
  acks: [],
  steps,
  changes: [{ ...retype, rebuilds }, addIndex],
  warnings: [],
  applicable: true,
  sql: steps.map((step) => step.sql).join("\nGO\n"),
  scope: artifact.scope,
  units,
  preserved: [preservedDrop],
  refusals: [refusal],
};

const dropItems: Change = { id: "drop-items", action: "drop", kind: "table", object: { schema: "main", name: "items" }, destructive: true, reason: "drops table main.items and its data" };

/** A complete-scope SQLite plan that drops one table, which apply must acknowledge. */
export const destructivePlan: ReviewedPlan = {
  dialect: "sqlite",
  schema: "main",
  artifact: {
    version: 2,
    dialect: "sqlite",
    capabilities: { fingerprint: "sqlite-3.45", transactionalDdl: true },
    scope: { mode: "complete", schemas: ["main"] },
    baseline: { tables: [], definitions: [] },
    desired: { tables: [], definitions: [] },
    changes: [dropItems],
    units: [
      {
        id: "unit-items",
        changes: [dropItems.id],
        objects: [dropItems.object],
        transactional: true,
        ops: [{ id: "drop-items/drop", change: dropItems.id, phase: "drop", object: dropItems.object, statements: [{ sql: "DROP TABLE items" }] }],
      },
    ],
    hash: "reviewed",
  },
  hash: "reviewed",
  acks: [dropItems.id],
  steps: [{ id: "drop-items/drop/1", sql: "DROP TABLE items", destructive: true, change: dropItems.id, unit: "unit-items", phase: "drop" }],
  changes: [dropItems],
  warnings: [],
  applicable: true,
  sql: "DROP TABLE items",
  scope: { mode: "complete", schemas: ["main"] },
  units: [{ id: "unit-items", changes: [dropItems.id], objects: [dropItems.object], transactional: true, steps: ["drop-items/drop/1"] }],
};

/** A version 3 script plan file with no declared-schema step. */
export function scriptPlanFile(actions: ScriptAction[]): ScriptPlanFile {
  return {
    kind: "sqlglot-go/script-plan",
    version: 3,
    target: { server: "db.example", database: "app", scope: "default" },
    bundle: { scripts: actions.map((action) => action.script) },
    ledger: {},
    observations: {},
    actions,
    hash: "reviewed",
  };
}

/** A run that applied the first unit and failed in the second; the engine returns it together with an error. */
export const failedAfterFirstUnit: ApplyResult = {
  hash: artifact.hash,
  outcome: "failed",
  steps: 7,
  units: [
    { id: unitOne.id, outcome: "applied", statements: 7 },
    { id: unitTwo.id, outcome: "failed", statements: 0 },
  ],
};
