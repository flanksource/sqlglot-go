// Recorded runs and task snapshots shared by the screen tests and the dev mock API.
import type { HistoryRecord, PatchResult, CompareResult } from "../types";
import compareFixture from "./compare.json";
import patchFixture from "./patch.json";

export const COMPARISON_RUN = "0d2c6f1e-8b1a-4c53-9d1e-6f0a7b3c2e11";
export const PATCH_RUN = "7a4e9b02-5c3d-4f68-8a21-1d9e0c6b4f35";

/** An override of `undefined` leaves the field out, as the server does for an unset optional field. */
type Overrides = { [K in Exclude<keyof HistoryRecord, "id" | "kind">]?: HistoryRecord[K] | undefined } & Partial<Pick<HistoryRecord, "id" | "kind">>;

export function record(overrides: Overrides & Pick<HistoryRecord, "id" | "kind">): HistoryRecord {
  const merged = {
    dialect: "sqlite",
    reference: compareFixture.reference,
    target: compareFixture.target,
    schemas: compareFixture.schemas,
    status: "succeeded",
    createdAt: "2026-01-15T09:15:00Z",
    summary: {},
    ...overrides,
  };
  return Object.fromEntries(Object.entries(merged).filter(([, value]) => value !== undefined)) as HistoryRecord;
}

export const comparisonRecord = (overrides: Overrides = {}): HistoryRecord =>
  record({
    id: COMPARISON_RUN,
    kind: "comparison",
    summary: { changes: compareFixture.changes.length, destructive: 2 },
    payload: compareFixture as CompareResult,
    ...overrides,
  });

export const patchRecord = (overrides: Overrides = {}): HistoryRecord =>
  record({
    id: PATCH_RUN,
    kind: "patch",
    parent: COMPARISON_RUN,
    summary: { selected: patchFixture.selection.length, acks: patchFixture.acks.length },
    payload: patchFixture as PatchResult,
    ...overrides,
  });

/** A comparison group with its inspect/plan tasks, as the clicky task API serves it. */
export function runSnapshots(run: string, status: "running" | "success" | "failed") {
  return [
    { id: run, name: "Compare dev with dev-0a", type: "group", status, groupId: run, total: 3, completed: status === "running" ? 1 : 3 },
    { id: `${run}/reference`, name: "Inspect reference dev", type: "task", status: "success", groupId: run },
    { id: `${run}/plan`, name: "Plan all differences", type: "task", status: status === "running" ? "running" : status, groupId: run },
  ];
}
