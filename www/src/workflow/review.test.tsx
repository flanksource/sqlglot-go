import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen, within } from "@testing-library/react";
import { parseRoute } from "../compare/route";
import { failedAfterFirstUnit, precondition, refusal, reviewedPlan } from "../fixtures/plan";
import { renderWithClient } from "../test-utils";
import { MigrationOutcome } from "./MigrationOutcome";
import { downloadablePlan, MigrationReview } from "./MigrationReview";
import { MigrationSetup } from "./MigrationSetup";
import { previewText } from "./projectUpload";
import type { ReviewedPlan } from "./types";

vi.mock("@flanksource/clicky-ui/monaco", () => ({ MonacoEditor: () => null }));

afterEach(cleanup);

function review(plan: ReviewedPlan) {
  return renderWithClient(
    <MigrationReview plan={plan} target="connection://local" onApply={vi.fn()} busy={false} route={parseRoute("/", "")} navigate={vi.fn()} />,
  );
}

const texts = (list: HTMLElement) => within(list).getAllByRole("listitem").map((item) => item.textContent);

describe("reviewing a schema plan", () => {
  it("lists each rebuild a retype forces under that change, and none under a change that forces nothing", () => {
    review(reviewedPlan);
    const changes = screen.getByRole("region", { name: "Planned changes" });
    expect(within(changes).getByText("modify column amount on dbo.orders")).toBeTruthy();
    expect(within(changes).getByText("add index IX_events_at on dbo.events")).toBeTruthy();
    expect(texts(within(changes).getByRole("list", { name: "Rebuilds forced by modify column amount on dbo.orders" }))).toEqual([
      "rebuild index IX_orders_customer_amount on dbo.orders",
      "rebuild foreign-key FK_orders_amount_limits on dbo.orders",
      "rebuild check CK_orders_amount_positive on dbo.orders",
    ]);
    expect(within(changes).queryByRole("list", { name: /^Rebuilds forced by add index/ })).toBeNull();
  });

  it("groups the statements by unit in execution order with the phase and change of each", () => {
    review(reviewedPlan);
    const [first, second] = screen.getAllByRole("region", { name: /^Unit \d+$/ }) as [HTMLElement, HTMLElement];
    const heads = (unit: HTMLElement) => within(within(unit).getByRole("list", { name: "Statements" })).getAllByRole("listitem").map((item) => item.querySelector("p")?.textContent);
    expect(heads(first)).toEqual([
      "drop rebuild foreign-key FK_orders_amount_limits on dbo.orders",
      "drop rebuild check CK_orders_amount_positive on dbo.orders",
      "drop rebuild index IX_orders_customer_amount on dbo.orders",
      "alter modify column amount on dbo.orders",
      "create rebuild index IX_orders_customer_amount on dbo.orders",
      "create rebuild check CK_orders_amount_positive on dbo.orders",
      "create rebuild foreign-key FK_orders_amount_limits on dbo.orders",
    ]);
    expect(heads(second)).toEqual(["create add index IX_events_at on dbo.events"]);
    expect(first.textContent).toContain("ALTER TABLE dbo.orders ALTER COLUMN amount decimal(19,4) NOT NULL");
    expect(second.textContent).toContain("CREATE INDEX IX_events_at ON dbo.events (at) WITH (ONLINE = ON)");
  });

  it("marks only the non-transactional unit", () => {
    review(reviewedPlan);
    const [first, second] = screen.getAllByRole("region", { name: /^Unit \d+$/ }) as [HTMLElement, HTMLElement];
    expect(within(first).queryByText("Non-transactional")).toBeNull();
    expect(within(second).getByText("Non-transactional")).toBeTruthy();
  });

  it("shows each unit's preconditions with their message and SQL as checks that run before any change", () => {
    review(reviewedPlan);
    const [first, second] = screen.getAllByRole("region", { name: /^Unit \d+$/ }) as [HTMLElement, HTMLElement];
    const checks = within(first).getByRole("list", { name: "Checks that run before any change" });
    expect(within(checks).getByText(precondition.message)).toBeTruthy();
    expect(checks.textContent).toContain(precondition.sql);
    expect(within(second).queryByRole("list", { name: "Checks that run before any change" })).toBeNull();
  });

  it("shows the drops the plan leaves out, each with its rule as a hover title, and the objects the planner refuses to touch, with the reason", () => {
    review(reviewedPlan);
    const scope = screen.getByRole("region", { name: "Drops the plan leaves out" });
    expect(texts(within(scope).getByRole("list"))).toEqual(["drop table dbo.legacy_audit"]);
    const refusals = screen.getByRole("region", { name: "Objects the planner will not touch" });
    expect(texts(within(refusals).getByRole("list"))).toEqual([`dbo.fn_orders_total: ${refusal.reason}`]);
  });

  it("states the scope the plan manages", () => {
    review(reviewedPlan);
    expect(screen.getByText("Declared scope: dbo, 2 managed objects")).toBeTruthy();
  });

  it("omits the preserved and refusal sections when the plan has none", () => {
    const { preserved: _preserved, refusals: _refusals, ...rest } = reviewedPlan;
    review(rest);
    expect(screen.queryByRole("region", { name: "Drops the plan leaves out" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Objects the planner will not touch" })).toBeNull();
  });

  it("shows warnings and says an advisory plan cannot be applied", () => {
    review({ ...reviewedPlan, applicable: false, warnings: ["security changes require phased reviewed artifacts"] });
    expect(screen.getByText("security changes require phased reviewed artifacts")).toBeTruthy();
    expect(screen.getByText("Preview only")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Review application" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("keeps the advisory list in the plan file it saves, so re-importing it stays a preview", () => {
    const advisory = ["security changes require phased reviewed artifacts"];
    expect(downloadablePlan({ ...reviewedPlan, applicable: false, advisory })).toEqual({ artifact: reviewedPlan.artifact, advisory, sources: undefined });
    expect(downloadablePlan(reviewedPlan)).toBe(reviewedPlan.artifact);
  });

  it("refuses a plan whose statements name a unit it does not list", () => {
    const silence = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const broken: ReviewedPlan = { ...reviewedPlan, units: [] };
    expect(() => review(broken)).toThrow(/statement .* names unit unit-retype-orders/);
    silence.mockRestore();
  });
});

describe("the result of applying a plan", () => {
  it("shows each unit's outcome and says how many units were applied when a later unit failed", () => {
    renderWithClient(<MigrationOutcome value={{ mode: "apply", result: failedAfterFirstUnit }} />);
    expect(screen.getByText("Migration failed")).toBeTruthy();
    expect(screen.getByText(/1 of 2 units applied/)).toBeTruthy();
    expect(texts(screen.getByRole("list", { name: "Unit outcomes" }))).toEqual([
      "unit-retype-orders applied 7 statements",
      "unit-index-events failed 0 statements",
    ]);
  });

  it("shows a script bundle's partial schema step beside the scripts it applied and the ones that failed", () => {
    renderWithClient(
      <MigrationOutcome
        value={{
          mode: "apply",
          result: {
            outcome: "partial",
            applied: ["seed"],
            schema: "partial",
            failed: [{ script: "backfill", outcome: "partial", error: "timeout" }],
            skipped: [{ script: "report", dependency: "backfill" }],
          },
        }}
      />,
    );
    expect(screen.getByText("Schema step partially applied")).toBeTruthy();
    expect(screen.getByText("1 script applied")).toBeTruthy();
    expect(screen.getByText("backfill partial: timeout")).toBeTruthy();
    expect(screen.getByText("report skipped: backfill did not run")).toBeTruthy();
  });

  it("warns when a run kept no result", () => {
    renderWithClient(<MigrationOutcome value={{ mode: "apply" }} />);
    expect(screen.getByText("No execution result")).toBeTruthy();
  });
});

describe("the project preview", () => {
  it("shows how many mapped sources the project's default label select left out, only when there are some", () => {
    expect(previewText({ mapped: 2, ignored: 1, deselected: 3 })).toBe("2 mapped · 1 ignored · 3 deselected");
    expect(previewText({ mapped: 2, ignored: 1 })).toBe("2 mapped · 1 ignored");
    expect(previewText({ mapped: 2, ignored: 1, deselected: 0 })).toBe("2 mapped · 1 ignored");
  });

  it("puts the deselected count in the local project's summary", () => {
    renderWithClient(
      <MigrationSetup disabled={false} targetReady={true} project={{ config: "/project/.sqlglot-go.yaml", preview: { mapped: 2, ignored: 1, deselected: 3 } }} onPlan={vi.fn()} />,
    );
    expect(screen.getByText("2 mapped · 1 ignored · 3 deselected")).toBeTruthy();
  });
});
