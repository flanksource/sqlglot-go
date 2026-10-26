import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { parseRoute } from "../compare/route";
import { json, renderWithClient, stubFetch } from "../test-utils";
import { SQLTools } from "./SQLTools";
import { MigrationReview } from "./MigrationReview";
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
  it("requires individual acknowledgements and target/hash confirmation before apply", async () => {
    const plan: ReviewedPlan = {
      dialect: "sqlite",
      schema: "main",
      artifact: { hash: "reviewed" },
      hash: "reviewed",
      acks: ["drop-items"],
      steps: [{ id: "drop-items", sql: "DROP TABLE items", destructive: true }],
      changes: [],
      warnings: [],
      applicable: true,
      sql: "DROP TABLE items",
    };
    const apply = vi.fn();
    renderWithClient(
      <MigrationReview
        plan={plan}
        target="connection://local"
        onApply={apply}
        busy={false}
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
        hash: "reviewed",
        ack: ["drop-items"],
        "lock-timeout": "5s",
      }),
    );
  });
});
