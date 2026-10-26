import { afterEach, describe, expect, it } from "vitest";
import { buildRoute, parseRoute, type Route } from "./route";

const DEFAULT: Route = {
  page: "compare",
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

function at(search: string) {
  window.history.replaceState(null, "", `/${search}`);
}

afterEach(() => at(""));

describe("parseRoute", () => {
  it("defaults to the compare setup screen", () => {
    expect(parseRoute("/", "")).toEqual(DEFAULT);
  });

  it("reads the connections page and its selected connection", () => {
    expect(parseRoute("/", "?page=connections&connection=default%2Fdev")).toEqual({ ...DEFAULT, page: "connections", connection: "default/dev" });
  });

  it("reads the cmp.* params", () => {
    const search = "?cmp.stage=review&cmp.reference=connection%3A%2F%2Fdev&cmp.target=connection%3A%2F%2Fdev-0a&cmp.schemas=main%2Caux&cmp.object=table%3Amain.orders&cmp.selected=YQ";
    expect(parseRoute("/", search)).toEqual({
      ...DEFAULT,
      stage: "review",
      reference: "connection://dev",
      target: "connection://dev-0a",
      schemas: "main,aux",
      object: "table:main.orders",
      selected: "YQ",
    });
  });

  it("distinguishes an explicitly empty selection from an absent one", () => {
    expect(parseRoute("/", "?cmp.selected=").selected).toBe("");
    expect(parseRoute("/", "").selected).toBeNull();
  });

  it("reads the comparison and patch run ids", () => {
    expect(parseRoute("/", "?cmp.stage=patch&cmp.run=run-1&cmp.patch=run-2")).toEqual({ ...DEFAULT, stage: "patch", run: "run-1", patch: "run-2" });
  });

  it("reads the history page, its filters and its open record", () => {
    const search = "?page=history&hist.kind=comparison&hist.connection=connection%3A%2F%2Fdev&hist.record=run-1%2Freference";
    expect(parseRoute("/", search)).toEqual({
      ...DEFAULT,
      page: "history",
      historyKind: "comparison",
      historyConnection: "connection://dev",
      historyRecord: "run-1/reference",
    });
  });

  it("rejects an unknown history kind", () => {
    expect(() => parseRoute("/", "?page=history&hist.kind=export")).toThrow(/Unknown history kind: export/);
  });

  it("rejects an unknown stage", () => {
    expect(() => parseRoute("/", "?cmp.stage=apply")).toThrow(/Unknown comparison stage: apply/);
  });

  it("rejects an unknown page", () => {
    expect(() => parseRoute("/", "?page=settings")).toThrow(/Unknown page: settings/);
  });
});

describe("buildRoute", () => {
  it("never writes unsaved connection strings to navigation URLs", () => {
    const unsaved = "postgres://operator:example@localhost/db";
    const url = buildRoute({ ...DEFAULT, page: "migrations", reference: unsaved, target: unsaved, workflowConnection: unsaved, migrationRun: "recorded" });
    expect(url).toBe("/?mig.run=recorded&page=migrations");
  });
  it("omits default values", () => {
    at("");
    expect(buildRoute(DEFAULT)).toBe("/");
  });

  it("round-trips a full route through parseRoute", () => {
    const route: Route = {
      page: "compare",
      connection: "",
      stage: "patch",
      reference: "connection://ns/dev",
      target: "connection://dev-0a",
      schemas: "main,aux",
      selected: "YQ,Yg",
      object: "table:main.orders",
      run: "0b7d0c1e-run",
      patch: "5c1e8a2f-patch",
      historyKind: "patch",
      historyConnection: "connection://ns/dev",
      historyRecord: "0b7d0c1e-run/target",
    };
    at("");
    const url = buildRoute(route);
    expect(parseRoute("/", url.slice(1))).toEqual(route);
  });

  it("round-trips the history page route", () => {
    const route: Route = { ...DEFAULT, page: "history", historyKind: "inspection", historyConnection: "connection://dev", historyRecord: "abc" };
    at("");
    expect(buildRoute(route)).toBe("/?page=history&hist.kind=inspection&hist.connection=connection%3A%2F%2Fdev&hist.record=abc");
    expect(parseRoute("/", buildRoute(route).slice(1))).toEqual(route);
  });

  it("keeps an empty selection in the URL", () => {
    at("");
    expect(buildRoute({ ...DEFAULT, selected: "" })).toBe("/?cmp.selected=");
  });

  it("drops params that went back to their default and keeps unrelated ones", () => {
    at("?cmp.stage=review&other=1");
    expect(buildRoute(DEFAULT)).toBe("/?other=1");
  });
});
