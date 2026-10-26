// Dev-only API stand-in (`pnpm dev:mock`): serves src/fixtures/*.json under /api/v1
// so the UI can be exercised without the Go server. Never imported by src/.
import { readFileSync } from "node:fs";
import type { IncomingMessage, ServerResponse } from "node:http";
import { resolve } from "node:path";
import type { Plugin } from "vite";
import type { CompareResult, HistoryRecord, PatchRequest, PatchResult } from "../src/types.ts";

const fixture = <T>(name: string): T => JSON.parse(readFileSync(resolve(import.meta.dirname, "../src/fixtures", name), "utf8")) as T;

function send(res: ServerResponse, status: number, body: unknown) {
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json");
  res.end(JSON.stringify(body));
}

async function readBody<T>(req: IncomingMessage): Promise<T> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(chunk as Buffer);
  return JSON.parse(Buffer.concat(chunks).toString("utf8")) as T;
}

function mockPatch(request: Omit<PatchRequest, "diffHash"> & { "diff-hash": string }): PatchResult {
  const compare = fixture<CompareResult>("compare.json");
  if (request["diff-hash"] !== compare.diffHash) throw new Error("schema drift: the reference or target changed since the comparison");
  const selected = new Set(request.select);
  const acks = compare.changes.filter((change) => selected.has(change.id) && change.destructive).map((change) => change.id);
  const sql = compare.steps.filter((step) => step.changes.some((id) => selected.has(id))).map((step) => step.sql).join("\n");
  const hash = `sha256:mock-${selected.size}-${sql.length}`;
  return { dialect: compare.dialect, artifact: { version: 1, dialect: compare.dialect, hash, selection: request.select }, sql, hash, acks, selection: request.select };
}

// Every run the mock served, newest first: the stand-in for the server's run history.
const history: HistoryRecord[] = [];

function record(run: HistoryRecord): HistoryRecord {
  if (history.some((existing) => existing.id === run.id)) throw new Error(`run ${run.id} was already used`);
  history.unshift(run);
  return run;
}

export function mockApiPlugin(): Plugin {
  return {
    name: "sqlglot-mock-api",
    configureServer(server) {
      server.middlewares.use("/api/v1", (req, res) => {
        void (async () => {
          try {
            const url = new URL(req.url ?? "", "http://mock");
            const path = url.pathname;
            if (req.method === "GET" && path === "/connections/dashboard") return send(res, 200, fixture("connections-dashboard.json"));
            if (req.method === "GET" && path === "/history") {
              const kind = url.searchParams.get("kind");
              const connection = url.searchParams.get("connection");
              const matching = history.filter((run) => (!kind || run.kind === kind) && (!connection || run.reference === connection || run.target === connection));
              return send(res, 200, matching.map(({ payload: _payload, ...listed }) => listed));
            }
            if (req.method === "GET" && path.startsWith("/history/")) {
              const found = history.find((run) => run.id === decodeURIComponent(path.slice("/history/".length)));
              return found ? send(res, 200, found) : send(res, 404, { code: "not_found", message: "run not found", trace: "" });
            }
            if (req.method === "POST" && path === "/schema/compare") {
              const { reference, target, run } = await readBody<{ reference: string; target: string; run: string }>(req);
              const result = { ...fixture<CompareResult>("compare.json"), reference, target };
              record({ id: run, kind: "comparison", dialect: result.dialect, reference, target, schemas: result.schemas, status: "succeeded", createdAt: new Date().toISOString(), summary: { changes: result.changes.length }, payload: result });
              return send(res, 200, result);
            }
            if (req.method === "POST" && path === "/schema/patch") {
              const request = await readBody<Parameters<typeof mockPatch>[0] & { comparison: string }>(req);
              const result = mockPatch(request);
              record({ id: request.run, kind: "patch", parent: request.comparison, dialect: result.dialect, reference: request.reference, target: request.target, schemas: [], status: "succeeded", createdAt: new Date().toISOString(), summary: { selected: result.selection.length, acks: result.acks.length }, payload: result });
              return send(res, 200, result);
            }
            return send(res, 404, { code: "not_found", message: `mock API does not serve ${req.method} /api/v1${path}`, trace: "" });
          } catch (error) {
            return send(res, 400, { code: "bad_request", message: error instanceof Error ? error.message : String(error), trace: "" });
          }
        })();
      });
    },
  };
}
