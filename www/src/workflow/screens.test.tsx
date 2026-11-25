import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { parseRoute } from "../compare/route";
import { destructivePlan, scriptPlanFile } from "../fixtures/plan";
import { json, renderWithClient, stubFetch } from "../test-utils";
import { SQLTools } from "./SQLTools";
import { MigrationReview } from "./MigrationReview";
import { MigrationSetup } from "./MigrationSetup";
import type { ReviewedPlan } from "./types";
import { DraftProvider } from "./drafts";

vi.mock("@flanksource/clicky-ui/monaco", () => ({
  MonacoEditor: ({
    value,
    onChange,
  }: {
    value: string;
    onChange: (value: string) => void;
  }) => (
    <textarea
      aria-label="SQL"
      value={value}
      onChange={(event) => onChange(event.target.value)}
    />
  ),
}));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("SQL Tools", () => {
  it("shows formatted output without replacing the input until requested", async () => {
    stubFetch({
      "/api/v1/dialects": json([{ name: "neutral" }]),
      "/api/v1/format": json("SELECT 1"),
      "/api/v1/render/formats": json({ json: "application/json" }),
    });
    renderWithClient(
      <DraftProvider>
        <SQLTools route={parseRoute("/", "")} navigate={vi.fn()} />
      </DraftProvider>,
    );
    fireEvent.change(screen.getByRole("textbox", { name: "SQL" }), {
      target: { value: "select 1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Format" }));
    expect(
      await screen.findByRole("button", { name: "Replace editor content" }),
    ).toBeTruthy();
    expect(
      (screen.getByRole("textbox", { name: "SQL" }) as HTMLTextAreaElement)
        .value,
    ).toBe("select 1");
    fireEvent.click(
      screen.getByRole("button", { name: "Replace editor content" }),
    );
    expect(
      (screen.getByRole("textbox", { name: "SQL" }) as HTMLTextAreaElement)
        .value,
    ).toBe("SELECT 1");
  });
});

describe("reviewed migrations", () => {
  it("defaults to the discovered project and submits its source without an upload", () => {
    const onPlan = vi.fn();
    renderWithClient(<DraftProvider><MigrationSetup disabled={false} targetReady={true}
      project={{ config: "/project/.sqlglot-go.yaml", preview: { mapped: 2, ignored: 1 } }} onPlan={onPlan} /></DraftProvider>);
    expect(screen.getByText("2 mapped · 1 ignored")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Generate reviewed plan" }));
    expect(onPlan).toHaveBeenCalledWith({ source: { kind: "project" }, var: [], routines: "", "allow-drops": false });
  });

  it("shows script actions and treats country as a generic label", async () => {
    const script = { id: "seed-guid", name: "Seed catalog", path: "scripts/seed/data.xml", phase: "post" as const, sql: "SELECT 2;" };
    const scriptPlan = scriptPlanFile([{ script, selected: true, reasons: ["new"] }]);
    const plan: ReviewedPlan = {
      dialect: "sqlserver", schema: "dbo", artifact: scriptPlan, hash: "reviewed",
      acks: [], steps: [], changes: [], warnings: [], applicable: true, sql: "",
      scriptPlan,
      sources: [{ id: script.id, path: script.path, kind: "sql", labels: { country: ["ZW"], component: ["billing"] } }],
    };
    const apply = vi.fn();
    renderWithClient(<MigrationReview plan={plan} target="connection://local" onApply={apply} busy={false}
      route={parseRoute("/", "")} navigate={vi.fn()} />);
    expect(screen.getByText(/1 script to run/)).toBeTruthy();
    expect(screen.getAllByText("Labels").length).toBeGreaterThan(0);
    expect(screen.getByText("country: ZW")).toBeTruthy();
    expect(screen.getByText("component: billing")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Seed catalog" }));
    expect(screen.getByText("seed-guid")).toBeTruthy();
    expect(screen.getByText("scripts/seed/data.xml")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Review application" }));
    expect(screen.getByText(/1 post-deployment script/)).toBeTruthy();
    expect(apply).not.toHaveBeenCalled();
  });

  it("shows an empty script as skipped while keeping other scripts runnable", () => {
    const path = "scripts/disabled/data.xml";
    const scriptPlan = scriptPlanFile([
      { script: { id: "empty", name: "Disabled", path, phase: "post", sql: "/* disabled */", noStatements: true }, selected: false },
      { script: { id: "seed", name: "Seed", path: "seed.sql", phase: "post", sql: "SELECT 1;" }, selected: true },
    ]);
    const plan: ReviewedPlan = {
      dialect: "sqlserver", schema: "dbo", artifact: scriptPlan, hash: "reviewed",
      acks: [], steps: [], changes: [], warnings: [`SQL script ${path} has no statements; skipped`], applicable: true, sql: "",
      scriptPlan,
    };
    renderWithClient(<MigrationReview plan={plan} target="connection://local" onApply={vi.fn()} busy={false}
      route={parseRoute("/", "")} navigate={vi.fn()} />);
    expect(screen.getByText(`SQL script ${path} has no statements; skipped`)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Disabled" })).toBeTruthy();
    expect(screen.getByText("Skipped")).toBeTruthy();
    expect(screen.getByText(/1 script to run/)).toBeTruthy();
  });

  it("requires individual acknowledgements and target confirmation, then applies without a reviewed hash", async () => {
    const plan = destructivePlan;
    const apply = vi.fn();
    renderWithClient(
      <MigrationReview
        plan={plan}
        target="connection://local"
        onApply={apply}
        busy={false}
        route={parseRoute("/", "")}
        navigate={vi.fn()}
      />,
    );
    expect(
      (
        screen.getByRole("button", {
          name: "Review application",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    fireEvent.click(screen.getByRole("checkbox", { name: /drop-items/ }));
    fireEvent.click(screen.getByRole("button", { name: "Review application" }));
    expect(apply).not.toHaveBeenCalled();
    fireEvent.click(
      screen.getByRole("button", { name: "Apply reviewed migration" }),
    );
    await waitFor(() =>
      expect(apply).toHaveBeenCalledWith({
        ack: ["drop-items"],
        "lock-timeout": "5s",
      }),
    );
    expect(apply.mock.calls[0]![0]).not.toHaveProperty("hash");
  });
});
