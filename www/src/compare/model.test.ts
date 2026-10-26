import { describe, expect, it } from "vitest";
import compareFixture from "../fixtures/compare.json";
import type { CompareResult } from "../types";
import { applyCommand, changeLabel, connectionRef, countChanges, groupByObject, isDriftError, objectKey, parseSchemas, stepsFor } from "./model";

const result = compareFixture as CompareResult;
const id = (action: string, kind: string, object: string, member = "") => JSON.stringify([action, kind, "main", object, member]);

describe("countChanges", () => {
  it("counts added, dropped, modified and destructive changes from the real change list", () => {
    expect(countChanges(result.changes)).toEqual({ added: 4, dropped: 2, modified: 2, destructive: 2 });
  });
});

describe("groupByObject", () => {
  const groups = groupByObject(result);

  it("returns one group per object in result order, keyed by kind and qualified name", () => {
    expect(groups.map((group) => group.key)).toEqual(["table:main.customers", "table:main.orders", "table:main.audit_log", "table:main.products"]);
  });

  it("lists each object's own changes in the order the object names them", () => {
    expect(groups[0]!.changes.map((change) => change.id)).toEqual([
      id("add", "column", "customers", "email"),
      id("add", "index", "customers", "idx_customers_email"),
      id("drop", "column", "customers", "legacy_code"),
    ]);
  });

  it("rejects an object that names a change the result does not contain", () => {
    const broken = { ...result, objects: [{ ...result.objects[0]!, changes: ["ghost"] }] };
    expect(() => groupByObject(broken)).toThrow(/object table:main.customers lists unknown change ghost/);
  });

  it("rejects a change that no object covers", () => {
    const broken = { ...result, objects: result.objects.slice(1) };
    expect(() => groupByObject(broken)).toThrow(/is not covered by any object/);
  });
});

describe("objectKey", () => {
  it("omits the schema when the object has none", () => {
    expect(objectKey("procedure", { schema: "", name: "refresh" })).toBe("procedure:refresh");
  });
});

describe("stepsFor", () => {
  const customers = groupByObject(result)[0]!;

  it("returns the steps of the object's selected changes only", () => {
    const selected = new Set([id("add", "column", "customers", "email"), id("drop", "table", "audit_log")]);
    expect(stepsFor(result.steps, customers, selected).map((step) => step.sql)).toEqual(['ALTER TABLE "customers" ADD COLUMN "email" TEXT;']);
  });

  it("is empty when none of the object's changes are selected", () => {
    expect(stepsFor(result.steps, customers, new Set())).toEqual([]);
  });

  it("returns a multi-change step once", () => {
    const orders = groupByObject(result)[1]!;
    const both = new Set(orders.changes.map((change) => change.id));
    expect(stepsFor(result.steps, orders, both)).toHaveLength(1);
  });
});

describe("connectionRef", () => {
  it("qualifies the name with its namespace", () => {
    expect(connectionRef({ name: "dev", namespace: "default" })).toBe("connection://default/dev");
  });

  it("is just the name for a connection without a namespace", () => {
    expect(connectionRef({ name: "dev", namespace: "" })).toBe("connection://dev");
  });
});

describe("parseSchemas", () => {
  it("splits, trims and drops empty entries", () => {
    expect(parseSchemas(" main, aux ,,")).toEqual(["main", "aux"]);
  });

  it("is undefined when no schema is given so the field is omitted from the request", () => {
    expect(parseSchemas("  ")).toBeUndefined();
  });
});

describe("applyCommand", () => {
  it("builds the apply command with the target, hash and one quoted --ack per destructive change", () => {
    const ack = id("drop", "column", "customers", "legacy_code");
    expect(applyCommand({ target: "connection://dev-0a", hash: "sha256:abc", acks: [ack] })).toBe(
      `sqlglot migrate apply --artifact artifact.json --dsn 'connection://dev-0a' --hash 'sha256:abc' --ack '${ack}'`,
    );
  });

  it("omits --ack when nothing is destructive", () => {
    expect(applyCommand({ target: "t", hash: "h", acks: [] })).toBe("sqlglot migrate apply --artifact artifact.json --dsn 't' --hash 'h'");
  });

  it("escapes single quotes inside a value", () => {
    expect(applyCommand({ target: "it's", hash: "h", acks: [] })).toContain(`--dsn 'it'\\''s'`);
  });
});

describe("isDriftError", () => {
  it("matches a drift message regardless of case", () => {
    expect(isDriftError("Schema DRIFT detected: diff hash differs")).toBe(true);
  });

  it("does not match other errors", () => {
    expect(isDriftError("dialect mismatch")).toBe(false);
  });
});

describe("changeLabel", () => {
  it("names the action, kind and member of a column change", () => {
    expect(changeLabel(result.changes[0]!)).toBe("add column email");
  });

  it("names just the action and kind for an object-level change", () => {
    expect(changeLabel(result.changes[3]!)).toBe("add table");
  });
});
