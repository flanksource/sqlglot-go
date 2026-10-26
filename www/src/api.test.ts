import { afterEach, describe, expect, it, vi } from "vitest";
import compareFixture from "./fixtures/compare.json";
import patchFixture from "./fixtures/patch.json";
import { comparisonRecord } from "./fixtures/records";
import { ApiError, compare, historyGet, historyList, isNotFound, patch, postText, taskRunKnown } from "./api";

function respond(status: number, body: string, contentType = "application/json") {
  const fetchMock = vi.fn().mockResolvedValue(new Response(body, { status, headers: { "Content-Type": contentType } }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => vi.unstubAllGlobals());

const RUN = "9c1d5e7a-3b2f-4a60-8d14-0e6f2a9b7c33";

describe("text operations", () => {
  it("negotiates plain text and preserves formatted SQL verbatim", async () => {
    const sql = "SELECT id FROM items";
    const fetchMock = respond(200, sql, "text/plain; charset=utf-8");
    expect(await postText("/format", { sql })).toBe(sql);
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/format", expect.objectContaining({ headers: { "Content-Type": "application/json", Accept: "text/plain" } }));
  });
});

describe("compare", () => {
  it("posts the request and its run id to /api/v1/schema/compare and returns the result", async () => {
    const fetchMock = respond(200, JSON.stringify(compareFixture));
    const result = await compare({ reference: "connection://dev", target: "connection://dev-0a", schemas: ["main"], run: RUN });
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/schema/compare", {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify({ reference: "connection://dev", target: "connection://dev-0a", schemas: ["main"], run: RUN }),
    });
    expect(result.diffHash).toBe(compareFixture.diffHash);
    expect(result.changes).toHaveLength(compareFixture.changes.length);
  });

  it("surfaces the message of a structured error body", async () => {
    respond(400, JSON.stringify({ code: "dialect_mismatch", message: "dialect mismatch: sqlite vs postgres", trace: "at compare" }));
    await expect(compare({ reference: "a", target: "b", run: RUN })).rejects.toThrow(new ApiError(400, "dialect mismatch: sqlite vs postgres"));
  });

  it("surfaces the error of a body that only has the older error field", async () => {
    respond(400, JSON.stringify({ error: "dialect mismatch: sqlite vs postgres" }));
    await expect(compare({ reference: "a", target: "b", run: RUN })).rejects.toThrow(new ApiError(400, "dialect mismatch: sqlite vs postgres"));
  });

  it("surfaces a plain-text error body", async () => {
    respond(500, "boom\n", "text/plain");
    await expect(compare({ reference: "a", target: "b", run: RUN })).rejects.toMatchObject({ status: 500, message: "boom" });
  });

  it("names the status when the error body is empty", async () => {
    respond(502, "", "text/plain");
    await expect(compare({ reference: "a", target: "b", run: RUN })).rejects.toThrow("POST /api/v1/schema/compare failed: 502");
  });

  it("rejects a 2xx response that is not a compare result", async () => {
    respond(200, JSON.stringify({ changes: "nope" }));
    await expect(compare({ reference: "a", target: "b", run: RUN })).rejects.toThrow(/compare response/);
  });
});

describe("patch", () => {
  it("posts the run, comparison, selection and diff hash to /api/v1/schema/patch under their flag names", async () => {
    const fetchMock = respond(200, JSON.stringify(patchFixture));
    const result = await patch({ reference: "r", target: "t", run: RUN, comparison: "c-1", select: patchFixture.selection, diffHash: "sha256:abc" });
    const body = { reference: "r", target: "t", run: RUN, comparison: "c-1", select: patchFixture.selection, "diff-hash": "sha256:abc" };
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/schema/patch", expect.objectContaining({ body: JSON.stringify(body) }));
    expect(result.acks).toEqual(patchFixture.acks);
  });

  it("rejects a 2xx response without sql and hash", async () => {
    respond(200, JSON.stringify({ artifact: {} }));
    await expect(patch({ reference: "r", target: "t", run: RUN, comparison: "c-1", select: [], diffHash: "h" })).rejects.toThrow(/patch response/);
  });
});

describe("historyList", () => {
  it("lists newest first with the kind and connection filters as query params", async () => {
    const records = [comparisonRecord({ id: "a" }), comparisonRecord({ id: "b" })];
    const fetchMock = respond(200, JSON.stringify(records));
    const result = await historyList({ kind: "comparison", connection: "connection://dev", limit: 20 });
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/history?kind=comparison&connection=connection%3A%2F%2Fdev&limit=20", expect.objectContaining({ method: "GET" }));
    expect(result.map((record) => record.id)).toEqual(["a", "b"]);
  });

  it("sends no query string without filters", async () => {
    const fetchMock = respond(200, "[]");
    await historyList();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/history", expect.anything());
  });

  it("rejects a response that is not a list of records", async () => {
    respond(200, JSON.stringify({ records: [] }));
    await expect(historyList()).rejects.toThrow(/history list response is not an array/);
  });
});

describe("historyGet", () => {
  it("returns the record with its payload", async () => {
    const fetchMock = respond(200, JSON.stringify(comparisonRecord({ id: RUN })));
    const record = await historyGet(RUN);
    expect(fetchMock).toHaveBeenCalledWith(`/api/v1/history/${RUN}`, expect.anything());
    expect(record.payload).toMatchObject({ diffHash: compareFixture.diffHash });
  });

  it("rejects with a 404 ApiError a gate can branch on when the run is not recorded", async () => {
    respond(404, JSON.stringify({ code: "not_found", message: "run not found", trace: "" }));
    const error = await historyGet(RUN).catch((caught: unknown) => caught);
    expect(error).toMatchObject({ status: 404, message: "run not found" });
    expect(isNotFound(error)).toBe(true);
  });

  it("does not treat a server failure as not found", async () => {
    respond(500, JSON.stringify({ code: "internal", message: "database is locked" }));
    expect(isNotFound(await historyGet(RUN).catch((caught: unknown) => caught))).toBe(false);
  });
});

describe("taskRunKnown", () => {
  it("is true when the task API serves the run", async () => {
    respond(200, "[]");
    expect(await taskRunKnown(RUN)).toBe(true);
  });

  it("is false on 404 and throws on any other failure", async () => {
    respond(404, "run not found", "text/plain");
    expect(await taskRunKnown(RUN)).toBe(false);
    respond(500, "boom", "text/plain");
    await expect(taskRunKnown(RUN)).rejects.toMatchObject({ status: 500 });
  });
});
