import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, within } from "@testing-library/react";
import dashboardFixture from "../fixtures/connections-dashboard.json";
import { COMPARISON_RUN, PATCH_RUN, comparisonRecord, patchRecord, record, runSnapshots } from "../fixtures/records";
import { failure, json, renderWithClient, stubFetch } from "../test-utils";
import type { Route } from "../compare/route";
import type { HistoryRecord } from "../types";
import { History } from "./History";

const route: Route = {
  page: "history",
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

const inspection = record({ id: `${COMPARISON_RUN}/reference`, kind: "inspection", parent: COMPARISON_RUN, target: undefined, summary: { table: 12, module: 3 } });
const failedMigration = record({ id: "mig-1", kind: "migration", status: "failed", error: "apply 0007: syntax error near GO", reference: undefined, summary: {} });
// The list endpoint omits payloads.
const listed = (records: HistoryRecord[]): HistoryRecord[] => records.map(({ payload: _payload, ...rest }) => rest);
const ALL = [comparisonRecord(), patchRecord(), inspection, failedMigration];

const DASHBOARD = "/api/v1/connections/dashboard";
const withDashboard = { [DASHBOARD]: json(dashboardFixture) };

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("History list", () => {
  it("shows when, kind, status, reference to target, dialect and summary for each record and the error of a failed one", async () => {
    stubFetch({ ...withDashboard, "/api/v1/history": json(listed(ALL)) });
    renderWithClient(<History route={route} navigate={vi.fn()} />);
    const rows = await screen.findAllByRole("row");
    expect(rows).toHaveLength(ALL.length + 1);
    const cells = (row: HTMLElement) => within(row).getAllByRole("cell").map((cell) => cell.textContent);
    expect(cells(rows[1]!).slice(1)).toEqual(["comparison", "succeeded", "dev→dev-0a", "sqlite", `${comparisonRecord().summary.changes} changes · 2 destructive`]);
    expect(cells(rows[4]!).slice(1)).toEqual(["migration", "failed", "dev-0a", "sqlite", "apply 0007: syntax error near GO"]);
    expect(document.querySelector("[data-history-error]")?.textContent).toBe("apply 0007: syntax error near GO");
  });

  it("filters by the kind and connection of the URL and by nothing else", async () => {
    const fetchMock = stubFetch({ ...withDashboard, "/api/v1/history?kind=patch&connection=connection%3A%2F%2Fdev": json(listed([patchRecord()])) });
    renderWithClient(<History route={{ ...route, historyKind: "patch", historyConnection: "connection://dev" }} navigate={vi.fn()} />);
    expect(await screen.findAllByRole("row")).toHaveLength(2);
    expect(fetchMock.mock.calls.map(([url]) => url)).toContain("/api/v1/history?kind=patch&connection=connection%3A%2F%2Fdev");
  });

  it("changes the kind filter in the URL from the kind control", async () => {
    stubFetch({ ...withDashboard, "/api/v1/history": json([]) });
    const navigate = vi.fn();
    renderWithClient(<History route={route} navigate={navigate} />);
    fireEvent.click(await screen.findByRole("radio", { name: "Comparison" }));
    expect(navigate).toHaveBeenCalledWith({ historyKind: "comparison" });
  });

  it("keeps both filter controls visible when nothing matches", async () => {
    stubFetch({ ...withDashboard, "/api/v1/history?kind=migration": json([]) });
    renderWithClient(<History route={{ ...route, historyKind: "migration" }} navigate={vi.fn()} />);
    expect(await screen.findByText("No recorded run matches these filters.")).toBeTruthy();
    expect(screen.getByRole("radiogroup", { name: "Kind" })).toBeTruthy();
    expect(screen.getByRole("combobox", { name: "Connection" })).toBeTruthy();
  });

  it("reports a history that cannot be read instead of an empty list", async () => {
    stubFetch({ ...withDashboard, "/api/v1/history": failure("history database is locked", 500) });
    renderWithClient(<History route={route} navigate={vi.fn()} />);
    expect(await screen.findByText("history database is locked")).toBeTruthy();
  });

  it("opens a comparison in the review screen, a patch in the patch screen and an inspection in the detail view", async () => {
    stubFetch({ ...withDashboard, "/api/v1/history": json(listed(ALL)) });
    const navigate = vi.fn();
    renderWithClient(<History route={route} navigate={navigate} />);
    await screen.findAllByRole("row");
    const open = (id: string) => fireEvent.click(document.querySelector(`[data-history-record="${id}"] a`)!);
    open(COMPARISON_RUN);
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ page: "compare", stage: "review", run: COMPARISON_RUN, reference: "connection://dev", target: "connection://dev-0a", schemas: "main" }));
    open(PATCH_RUN);
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ page: "compare", stage: "patch", run: COMPARISON_RUN, patch: PATCH_RUN }));
    open(inspection.id);
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ page: "inspect", inspectionRun: inspection.id }));
  });
});

describe("History record view", () => {
  const view = (id: string): Route => ({ ...route, historyRecord: id });

  it("shows an inspection's metadata, object counts, HCL and the run log of its comparison", async () => {
    const hcl = 'table "customers" {\n  schema = schema.main\n}';
    stubFetch({
      [`/api/v1/history/${encodeURIComponent(inspection.id)}`]: json({ ...inspection, payload: { dialect: "sqlite", schema: "main", hcl } }),
      [`/api/v1/tasks/${COMPARISON_RUN}`]: json(runSnapshots(COMPARISON_RUN, "success")),
    });
    renderWithClient(<History route={view(inspection.id)} navigate={vi.fn()} />);
    expect(await screen.findByLabelText("Record")).toBeTruthy();
    expect(screen.getByLabelText("Record").textContent).toContain("12 table · 3 module");
    expect(screen.getByLabelText("Record").textContent).toContain(COMPARISON_RUN);
    expect(await screen.findByText("Inspected schema")).toBeTruthy();
    expect(document.body.textContent).toContain('table "customers"');
    expect(await screen.findByText("Plan all differences")).toBeTruthy();
  });

  it("shows a SQL Server inspection's catalogue as JSON", async () => {
    const catalogue = { tables: [{ name: "customers" }], definitions: [], sequences: [], tableTypes: [] };
    stubFetch({
      "/api/v1/history/insp-mssql": json(record({ id: "insp-mssql", kind: "inspection", dialect: "tsql", payload: catalogue })),
      "/api/v1/tasks/insp-mssql": failure("run not found", 404),
    });
    renderWithClient(<History route={view("insp-mssql")} navigate={vi.fn()} />);
    expect(await screen.findByText("Inspected schema")).toBeTruthy();
    expect(document.body.textContent).toContain("tableTypes");
  });

  it("shows a failed migration's error and the partial result it kept", async () => {
    stubFetch({
      "/api/v1/history/mig-1": json({ ...failedMigration, payload: { mode: "apply", hash: "sha256:abc", acks: ["drop column"], result: { applied: 6, failedAt: "0007" } } }),
      "/api/v1/tasks/mig-1": failure("run not found", 404),
    });
    renderWithClient(<History route={view("mig-1")} navigate={vi.fn()} />);
    expect(await screen.findByText("apply 0007: syntax error near GO", { selector: "p, div" })).toBeTruthy();
    expect(await screen.findByText("apply")).toBeTruthy();
    expect(screen.getByText("sha256:abc")).toBeTruthy();
    expect(screen.getByText("drop column")).toBeTruthy();
    expect(await screen.findByText("Result")).toBeTruthy();
    expect(document.body.textContent).toContain('failedAt: "0007"');
  });

  it("says a record that is not in the history was not found and goes back to the list", async () => {
    stubFetch({ "/api/v1/history/ghost": failure("run not found", 404) });
    const navigate = vi.fn();
    renderWithClient(<History route={view("ghost")} navigate={navigate} />);
    expect(await screen.findByText("This record was not found")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /History/ }));
    expect(navigate).toHaveBeenCalledWith({ historyRecord: "" });
  });
});
