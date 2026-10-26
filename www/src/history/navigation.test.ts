import { describe, expect, it } from "vitest";
import { COMPARISON_RUN, PATCH_RUN, comparisonRecord, patchRecord, record } from "../fixtures/records";
import { routeForRecord, summaryText, taskRunOf } from "./navigation";

const SIDES = { reference: "connection://dev", target: "connection://dev-0a", schemas: "main" };
const COMPARE_SCREEN = { page: "compare", selected: null, object: "", historyRecord: "" };

describe("routeForRecord", () => {
  it("reopens a comparison in the review screen under its run", () => {
    expect(routeForRecord(comparisonRecord())).toEqual({ ...COMPARE_SCREEN, ...SIDES, stage: "review", run: COMPARISON_RUN, patch: "" });
  });

  it("reopens a patch in the patch screen under its comparison run", () => {
    expect(routeForRecord(patchRecord())).toEqual({ ...COMPARE_SCREEN, ...SIDES, stage: "patch", run: COMPARISON_RUN, patch: PATCH_RUN });
  });

  it("joins every schema of the record for the comparison route", () => {
    expect(routeForRecord(comparisonRecord({ schemas: ["main", "aux"] }))).toMatchObject({ schemas: "main,aux" });
  });

  it("opens an orphan patch in history and reopens inspections and migrations in their dedicated screens", () => {
    expect(routeForRecord(patchRecord({ id: "p", parent: undefined }))).toEqual({ page: "history", historyRecord: "p" });
    expect(routeForRecord(record({ id: "i", kind: "inspection" }))).toMatchObject({ page: "inspect", inspectionRun: "i" });
    expect(routeForRecord(record({ id: "m", kind: "migration", target: "postgres://user:REDACTED@localhost/db" }))).toMatchObject({ page: "migrations", migrationRun: "m", workflowConnection: "" });
  });
});

describe("summaryText", () => {
  it("lists the non-zero counters in order", () => {
    expect(summaryText({ changes: 8, added: 4, dropped: 0, destructive: 2 })).toBe("8 changes · 4 added · 2 destructive");
  });

  it("is empty when every counter is zero", () => {
    expect(summaryText({ changes: 0 })).toBe("");
  });
});

describe("taskRunOf", () => {
  it("logs an inspection of a comparison under that comparison and every other record under itself", () => {
    expect(taskRunOf(record({ id: `${COMPARISON_RUN}/reference`, kind: "inspection", parent: COMPARISON_RUN }))).toBe(COMPARISON_RUN);
    expect(taskRunOf(record({ id: "standalone", kind: "inspection" }))).toBe("standalone");
    expect(taskRunOf(patchRecord())).toBe(PATCH_RUN);
  });
});
