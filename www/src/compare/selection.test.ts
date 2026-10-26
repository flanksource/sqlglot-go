import { describe, expect, it } from "vitest";
import type { Change } from "../types";
import {
  buildGraph,
  decodeSelection,
  defaultSelection,
  deselect,
  encodeSelection,
  missingRequirements,
  readSelection,
  select,
  setMany,
} from "./selection";

function change(id: string, requires: string[] = [], destructive = false): Change {
  return {
    id,
    action: destructive ? "drop" : "add",
    kind: "column",
    object: { schema: "dbo", name: "orders" },
    destructive,
    ...(requires.length ? { requires } : {}),
  };
}

const sorted = (set: ReadonlySet<string>) => [...set].sort();

// Chain: index -> column -> table. FK -> table (other). Unrelated: lone.
const CHAIN = buildGraph([
  change("table"),
  change("column", ["table"]),
  change("index", ["column"]),
  change("other"),
  change("fk", ["table", "other"]),
  change("lone"),
]);

describe("buildGraph", () => {
  it("rejects a requirement that is not in the change list", () => {
    expect(() => buildGraph([change("a", ["ghost"])])).toThrow(/a requires unknown change ghost/);
  });

  it("rejects duplicate change ids", () => {
    expect(() => buildGraph([change("a"), change("a")])).toThrow(/duplicate change id a/);
  });
});

describe("select (ticking)", () => {
  it("adds the change on its own when it has no requirements", () => {
    expect(sorted(select(CHAIN, new Set(), "lone"))).toEqual(["lone"]);
  });

  it("adds requirements transitively", () => {
    expect(sorted(select(CHAIN, new Set(), "index"))).toEqual(["column", "index", "table"]);
  });

  it("adds every requirement of a change with several", () => {
    expect(sorted(select(CHAIN, new Set(), "fk"))).toEqual(["fk", "other", "table"]);
  });

  it("keeps what was already selected", () => {
    expect(sorted(select(CHAIN, new Set(["lone"]), "column"))).toEqual(["column", "lone", "table"]);
  });

  it("does not mutate its input", () => {
    const before = new Set(["lone"]);
    select(CHAIN, before, "index");
    expect(sorted(before)).toEqual(["lone"]);
  });

  it("rejects an unknown id", () => {
    expect(() => select(CHAIN, new Set(), "ghost")).toThrow(/unknown change ghost/);
  });
});

describe("deselect (unticking)", () => {
  const all = new Set(CHAIN.ids);

  it("removes only the change when nothing requires it", () => {
    expect(sorted(deselect(CHAIN, all, "lone"))).toEqual(["column", "fk", "index", "other", "table"]);
  });

  it("removes every transitive dependent", () => {
    expect(sorted(deselect(CHAIN, all, "table"))).toEqual(["lone", "other"]);
  });

  it("keeps the requirements of the unticked change", () => {
    expect(sorted(deselect(CHAIN, all, "index"))).toEqual(["column", "fk", "lone", "other", "table"]);
  });

  it("removes dependents through a diamond", () => {
    const diamond = buildGraph([change("base"), change("left", ["base"]), change("right", ["base"]), change("top", ["left", "right"])]);
    expect(sorted(deselect(diamond, new Set(diamond.ids), "left"))).toEqual(["base", "right"]);
  });

  it("is a no-op for a change that is not selected and has no selected dependents", () => {
    expect(sorted(deselect(CHAIN, new Set(["lone"]), "index"))).toEqual(["lone"]);
  });
});

describe("mutual requirements (SQLite rebuild groups)", () => {
  const group = buildGraph([change("a", ["b"]), change("b", ["a"]), change("c", ["a"]), change("solo")]);

  it("ticking one member ticks the whole group", () => {
    expect(sorted(select(group, new Set(), "a"))).toEqual(["a", "b"]);
    expect(sorted(select(group, new Set(), "b"))).toEqual(["a", "b"]);
  });

  it("unticking one member unticks the whole group and its dependents", () => {
    expect(sorted(deselect(group, new Set(group.ids), "b"))).toEqual(["solo"]);
  });

  it("terminates on a self requirement", () => {
    const self = buildGraph([change("a", ["a"])]);
    expect(sorted(select(self, new Set(), "a"))).toEqual(["a"]);
    expect(sorted(deselect(self, new Set(["a"]), "a"))).toEqual([]);
  });
});

describe("setMany (object-level checkbox)", () => {
  it("ticks every listed change with their requirements", () => {
    expect(sorted(setMany(CHAIN, new Set(), ["index", "lone"], true))).toEqual(["column", "index", "lone", "table"]);
  });

  it("unticks every listed change with their dependents", () => {
    expect(sorted(setMany(CHAIN, new Set(CHAIN.ids), ["column", "other"], false))).toEqual(["lone", "table"]);
  });
});

describe("defaultSelection", () => {
  it("selects every non-destructive change", () => {
    const graph = buildGraph([change("a"), change("b", ["a"]), change("d", [], true)]);
    expect(sorted(defaultSelection(graph))).toEqual(["a", "b"]);
  });

  it("drops a non-destructive change whose requirement is destructive, so the result stays valid", () => {
    const graph = buildGraph([change("drop", [], true), change("dep", ["drop"]), change("ok")]);
    expect(sorted(defaultSelection(graph))).toEqual(["ok"]);
  });

  it("drops a whole mutual group when one member is destructive", () => {
    const graph = buildGraph([change("a", ["b"]), change("b", ["a"], true), change("ok")]);
    expect(sorted(defaultSelection(graph))).toEqual(["ok"]);
  });

  it("is empty when every change is destructive", () => {
    expect(defaultSelection(buildGraph([change("d", [], true)])).size).toBe(0);
  });
});

describe("missingRequirements", () => {
  it("is empty for a closed selection", () => {
    expect(missingRequirements(CHAIN, new Set(["table", "column"]))).toEqual([]);
  });

  it("lists each unmet requirement once", () => {
    expect(missingRequirements(CHAIN, new Set(["index", "fk"]))).toEqual(["column", "other", "table"]);
  });
});

describe("readSelection", () => {
  it("uses the default selection when the URL carries none", () => {
    const graph = buildGraph([change("a"), change("d", [], true)]);
    expect(readSelection(graph, null)).toEqual({ selected: new Set(["a"]), problem: null });
  });

  it("decodes the selection from the URL, including an explicitly empty one", () => {
    expect(readSelection(CHAIN, encodeSelection(new Set(["lone", "table"])))).toEqual({ selected: new Set(["lone", "table"]), problem: null });
    expect(readSelection(CHAIN, "")).toEqual({ selected: new Set(), problem: null });
  });

  it("reports ids that are not in the comparison and keeps the rest", () => {
    const state = readSelection(CHAIN, encodeSelection(new Set(["lone", "ghost", "phantom"])));
    expect(state).toEqual({ selected: new Set(["lone"]), problem: "2 selected changes are not part of this comparison" });
  });

  it("reports a malformed token with an empty selection", () => {
    expect(readSelection(CHAIN, "not valid!")).toEqual({ selected: new Set(), problem: 'invalid selection token "not valid!"' });
  });
});

describe("URL encoding", () => {
  const ids = ['["add","table","dbo","orders",""]', '["drop","column","dbo","orders","legacy \\"x\\", é"]'];

  it("round-trips ids that contain commas, quotes and non-ASCII text", () => {
    const encoded = encodeSelection(new Set(ids));
    expect(encoded).not.toMatch(/[^A-Za-z0-9_,-]/);
    expect(sorted(decodeSelection(encoded))).toEqual(sorted(new Set(ids)));
  });

  it("encodes the empty selection as the empty string and decodes it back", () => {
    expect(encodeSelection(new Set())).toBe("");
    expect(decodeSelection("").size).toBe(0);
  });

  it("encodes in sorted order so equal selections produce equal URLs", () => {
    expect(encodeSelection(new Set([ids[1]!, ids[0]!]))).toBe(encodeSelection(new Set(ids)));
  });

  it("rejects a token that is not base64url", () => {
    expect(() => decodeSelection("not valid!")).toThrow(/invalid selection token/);
  });
});
