import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import compareFixture from "../fixtures/compare.json";
import patchFixture from "../fixtures/patch.json";
import { COMPARISON_RUN, PATCH_RUN, comparisonRecord, patchRecord } from "../fixtures/records";
import { bodyOf, failure, json, renderWithClient, stubFetch } from "../test-utils";
import { Patch } from "./Patch";
import type { Route } from "./route";
import { Review } from "./Review";
import { decodeSelection, encodeSelection } from "./selection";

const id = (action: string, kind: string, object: string, member = "") => JSON.stringify([action, kind, "main", object, member]);
const EMAIL = id("add", "column", "customers", "email");
const INDEX = id("add", "index", "customers", "idx_customers_email");
const LEGACY = id("drop", "column", "customers", "legacy_code");

const route: Route = {
  page: "compare",
  connection: "",
  stage: "review",
  reference: "connection://dev",
  target: "connection://dev-0a",
  schemas: "main",
  selected: null,
  object: "",
  run: COMPARISON_RUN,
  patch: "",
  historyKind: "",
  historyConnection: "",
  historyRecord: "",
};

const COMPARISON = `/api/v1/history/${COMPARISON_RUN}`;
const PATCH = `/api/v1/history/${PATCH_RUN}`;
const notFound = failure("run not found", 404);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function selectedFrom(navigate: ReturnType<typeof vi.fn>): string[] {
  const call = navigate.mock.calls.at(-1)?.[0] as { selected: string };
  return [...decodeSelection(call.selected)].sort();
}

describe("Review", () => {
  const recorded = { [COMPARISON]: json(comparisonRecord()) };

  it("shows the real change counts and selects the non-destructive changes by default", async () => {
    stubFetch(recorded);
    renderWithClient(<Review route={route} navigate={vi.fn()} />);
    const counts = await screen.findByLabelText("Change counts");
    expect(counts.textContent).toBe("+4 added−2 droppedΔ2 modified2 destructive");
    expect(screen.getByRole("button", { name: "Create patch (6)" })).toBeTruthy();
    expect((screen.getByLabelText("Select drop column legacy_code") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByLabelText("Select add column email") as HTMLInputElement).checked).toBe(true);
  });

  it("says a comparison read from the history did not contact the databases", async () => {
    stubFetch(recorded);
    renderWithClient(<Review route={route} navigate={vi.fn()} />);
    expect(await screen.findByText(/Recorded .*the databases were not contacted/)).toBeTruthy();
  });

  it("unticking a change also unticks the changes that require it", async () => {
    stubFetch(recorded);
    const navigate = vi.fn();
    renderWithClient(<Review route={route} navigate={navigate} />);
    fireEvent.click(await screen.findByLabelText("Select add column email"));
    expect(navigate).toHaveBeenCalledTimes(1);
    expect(selectedFrom(navigate)).not.toContain(EMAIL);
    expect(selectedFrom(navigate)).not.toContain(INDEX);
    expect(selectedFrom(navigate)).toHaveLength(4);
  });

  it("ticking a change pulls in the changes it requires", async () => {
    stubFetch(recorded);
    const navigate = vi.fn();
    renderWithClient(<Review route={{ ...route, selected: encodeSelection(new Set()) }} navigate={navigate} />);
    fireEvent.click(await screen.findByLabelText("Select add index idx_customers_email"));
    expect(selectedFrom(navigate)).toEqual([EMAIL, INDEX].sort());
  });

  it("ticks and unticks a mutually-requiring rebuild group together", async () => {
    stubFetch(recorded);
    const navigate = vi.fn();
    renderWithClient(<Review route={{ ...route, selected: encodeSelection(new Set([LEGACY])) }} navigate={navigate} />);
    fireEvent.click(await screen.findByLabelText("Select modify column price"));
    expect(selectedFrom(navigate)).toEqual([LEGACY, id("modify", "column", "products", "price"), id("modify", "column", "products", "sku")].sort());
  });

  it("shows the focused object's migration SQL for its selected changes in the Migration SQL pane", async () => {
    stubFetch(recorded);
    renderWithClient(<Review route={route} navigate={vi.fn()} />);
    await screen.findByLabelText("Change counts");
    const pane = screen.getByTestId("workspace-pane-sql");
    // The highlighter splits the statement into token elements, so read the pane's text.
    await waitFor(() => expect(pane.textContent).toContain('ALTER TABLE "customers" ADD COLUMN "email" TEXT;'));
    expect(pane.textContent).not.toContain("DROP COLUMN");
  });

  it("lays the changes, the diff and the migration SQL out as workspace panes with the run log collapsed", async () => {
    stubFetch(recorded);
    renderWithClient(<Review route={route} navigate={vi.fn()} />);
    await screen.findByLabelText("Change counts");
    const panes = ["changes", "diff", "sql", "run"].map((pane) => screen.queryByTestId(`workspace-pane-${pane}`));
    expect(panes.map((pane) => pane !== null)).toEqual([true, true, true, true]);
    expect(screen.getByTestId("workspace-pane-diff").querySelector("[data-comparison-diff]")).not.toBeNull();
    expect(screen.getByRole("button", { name: "Expand Run" }).getAttribute("aria-expanded")).toBe("false");
  });

  it("reports a failed comparison instead of an empty review", async () => {
    stubFetch({
      [COMPARISON]: json(comparisonRecord({ status: "failed", error: "dialect mismatch: sqlite vs postgres" })),
      [`/api/v1/tasks/${COMPARISON_RUN}`]: notFound,
    });
    renderWithClient(<Review route={route} navigate={vi.fn()} />);
    expect(await screen.findByText("dialect mismatch: sqlite vs postgres")).toBeTruthy();
    expect(screen.queryByLabelText("Change counts")).toBeNull();
  });

  it("flags a selection in the URL that names changes this comparison does not have", async () => {
    stubFetch(recorded);
    renderWithClient(<Review route={{ ...route, selected: encodeSelection(new Set([EMAIL, "ghost"])) }} navigate={vi.fn()} />);
    expect(await screen.findByText(/1 selected change is not part of this comparison/)).toBeTruthy();
    expect((screen.getByRole("button", { name: /Create patch/ }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("creates a patch under a new run id for the selection reviewed in this comparison and shows it", async () => {
    const fetchMock = stubFetch({ ...recorded, "/api/v1/schema/patch": json(patchFixture) });
    const navigate = vi.fn();
    renderWithClient(<Review route={route} navigate={navigate} />);
    fireEvent.click(await screen.findByRole("button", { name: "Create patch (6)" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
    const { patch: run } = navigate.mock.calls[0]![0] as { stage: string; patch: string };
    expect(navigate).toHaveBeenCalledWith({ stage: "patch", patch: run });
    expect(run).not.toBe(COMPARISON_RUN);
    const body = bodyOf(fetchMock, "/api/v1/schema/patch");
    expect(body).toMatchObject({ comparison: COMPARISON_RUN, run, reference: "connection://dev", target: "connection://dev-0a", schemas: ["main"], "diff-hash": compareFixture.diffHash });
    expect((body.select as string[]).sort()).toEqual([...decodeSelection(encodeSelection(new Set(compareFixture.changes.filter((change) => !change.destructive).map((change) => change.id))))].sort());
  });

  it("re-compares under a new run id and leaves the old run in history", async () => {
    const fetchMock = stubFetch({ ...recorded, "/api/v1/schema/compare": json(compareFixture) });
    const navigate = vi.fn();
    renderWithClient(<Review route={route} navigate={navigate} />);
    fireEvent.click(await screen.findByRole("button", { name: /Re-compare/ }));
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
    const { run } = navigate.mock.calls[0]![0] as { run: string };
    expect(navigate).toHaveBeenCalledWith({ stage: "review", run, patch: "", selected: null, object: "" });
    expect(run).not.toBe(COMPARISON_RUN);
    expect(bodyOf(fetchMock, "/api/v1/schema/compare")).toEqual({ reference: "connection://dev", target: "connection://dev-0a", schemas: ["main"], run });
  });
});

describe("Patch", () => {
  const patchRoute: Route = { ...route, stage: "patch", patch: PATCH_RUN };
  const recorded = { [COMPARISON]: json(comparisonRecord()), [PATCH]: json(patchRecord()) };

  it("shows the sql, hash, acknowledgements and the apply command with one --ack per destructive change", async () => {
    stubFetch(recorded);
    renderWithClient(<Patch route={patchRoute} navigate={vi.fn()} />);
    expect(await screen.findByText(patchFixture.hash, { selector: "[data-patch-hash]" })).toBeTruthy();
    expect(screen.getByLabelText("Destructive acknowledgements").textContent).toContain("drops column customers.legacy_code");
    const command = document.querySelector("[data-apply-command]")?.textContent;
    expect(command).toBe(
      `sqlglot migrate apply --artifact artifact.json --dsn 'connection://dev-0a' --hash '${patchFixture.hash}' --ack '${LEGACY}'`,
    );
  });

  it("offers a re-compare under a new run when the patch failed with drift", async () => {
    const fetchMock = stubFetch({
      ...recorded,
      [PATCH]: json(patchRecord({ status: "failed", error: "schema drift: diff hash changed" })),
      [`/api/v1/tasks/${PATCH_RUN}`]: notFound,
      "/api/v1/schema/compare": json(compareFixture),
    });
    const navigate = vi.fn();
    renderWithClient(<Patch route={patchRoute} navigate={navigate} />);
    expect(await screen.findByText("schema drift: diff hash changed")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Re-compare/ }));
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
    const { run } = navigate.mock.calls[0]![0] as { run: string };
    expect(navigate).toHaveBeenCalledWith({ stage: "review", run, patch: "", selected: null, object: "" });
    expect(run).not.toBe(COMPARISON_RUN);
    expect(bodyOf(fetchMock, "/api/v1/schema/compare")).toMatchObject({ reference: "connection://dev", target: "connection://dev-0a", run });
  });

  it("does not offer re-compare for a failure that is not drift", async () => {
    stubFetch({ ...recorded, [PATCH]: json(patchRecord({ status: "failed", error: "unsupported object" })), [`/api/v1/tasks/${PATCH_RUN}`]: notFound });
    renderWithClient(<Patch route={patchRoute} navigate={vi.fn()} />);
    expect(await screen.findByText("unsupported object")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Re-compare/ })).toBeNull();
  });

  it("asks for a patch to be created when the URL names a comparison but no patch run", async () => {
    const fetchMock = stubFetch({ [COMPARISON]: json(comparisonRecord()) });
    renderWithClient(<Patch route={{ ...patchRoute, patch: "" }} navigate={vi.fn()} />);
    expect(await screen.findByText(/No patch has been created/)).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
