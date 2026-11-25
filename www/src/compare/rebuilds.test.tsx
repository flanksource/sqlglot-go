import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import compareFixture from "../fixtures/compare.json";
import type { CompareResult, RebuildChange } from "../types";
import { ChangeTree } from "./ChangeTree";
import { groupByObject } from "./model";
import { ObjectDiff } from "./ObjectDiff";

vi.mock("@flanksource/clicky-ui/data", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@flanksource/clicky-ui/data")>()),
  CodeDiff: () => null,
}));

afterEach(cleanup);

const fixture = compareFixture as CompareResult;
const cause = JSON.stringify(["add", "column", "main", "customers", "email"]);
const rebuild: RebuildChange = {
  id: JSON.stringify(["rebuild", "index", "main", "customers", "idx_customers_name"]),
  action: "rebuild",
  kind: "index",
  object: { schema: "main", name: "customers" },
  member: "idx_customers_name",
  destructive: false,
  cause,
};
const groups = groupByObject({ ...fixture, rebuilds: [rebuild] });

describe("a comparison whose change forces a rebuild", () => {
  it("says in the change tree how many dependents the change rebuilds, on that change only", () => {
    render(
      <ChangeTree groups={groups} selected={new Set()} focusKey={groups[0]!.key} onFocus={vi.fn()} onToggle={vi.fn()} onSelectAll={vi.fn()} onSelectNone={vi.fn()} onSelectDefault={vi.fn()} />,
    );
    expect(screen.getAllByText("+1 rebuild")).toHaveLength(1);
    expect(screen.getByText("+1 rebuild").parentElement?.textContent).toBe("column email +1 rebuild");
  });

  it("lists the forced rebuild with the change that forces it under the focused object", () => {
    render(<ObjectDiff group={groups[0]!} />);
    const list = screen.getByRole("list", { name: "Forced rebuilds" });
    expect(within(list).getAllByRole("listitem").map((item) => item.textContent)).toEqual(["rebuild index idx_customers_name (forced by add column email)"]);
  });

  it("lists no rebuilds under an object whose changes force none", () => {
    render(<ObjectDiff group={groups[1]!} />);
    expect(screen.queryByRole("list", { name: "Forced rebuilds" })).toBeNull();
  });
});
