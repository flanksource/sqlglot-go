// Every call to the sqlglot ui server. The clicky typed-command request/response
// envelope is confined to `post`, so a change to it touches only this file.
import type { CompareRequest, CompareResult, HistoryFilter, HistoryRecord, PatchRequest, PatchResult } from "./types";
import type { SQLDiagnostic } from "./workflow/types";

export const API_BASE = "/api/v1";

export class ApiError extends Error {
  readonly status: number;
  readonly diagnostics: SQLDiagnostic[];

  constructor(status: number, message: string, diagnostics: SQLDiagnostic[] = []) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.diagnostics = diagnostics;
  }
}

export function isNotFound(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404;
}

// Structured errors are `{code, message, trace}`; `error` is the older single-field form.
async function errorMessage(response: Response, label: string): Promise<string> {
  const text = (await response.text()).trim();
  if (!text) return `${label} failed: ${response.status}`;
  try {
    const body: unknown = JSON.parse(text);
    if (typeof body === "object" && body !== null) {
      if ("message" in body && typeof body.message === "string" && body.message) return body.message;
      if ("error" in body && typeof body.error === "string" && body.error) return body.error;
    }
  } catch {
    // not JSON: the text is the message
  }
  return text;
}

async function request(method: "GET" | "POST", path: string, body?: unknown, accept = "application/json"): Promise<Response> {
  const url = `${API_BASE}${path}`;
  const response = await fetch(url, {
    method,
    headers: method === "POST" ? { "Content-Type": "application/json", Accept: accept } : { Accept: accept },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  if (!response.ok) {
    let diagnostics: SQLDiagnostic[] = [];
    const body: unknown = await response.clone().json().catch(() => null);
    if (body && typeof body === "object" && "context" in body && body.context && typeof body.context === "object" && "diagnostics" in body.context && Array.isArray(body.context.diagnostics)) diagnostics = body.context.diagnostics;
    throw new ApiError(response.status, await errorMessage(response, `${method} ${url}`), diagnostics);
  }
  return response;
}

export async function post(path: string, body: unknown): Promise<unknown> {
  return (await request("POST", path, body)).json();
}

export async function postText(path: string, body: unknown): Promise<string> {
  const response = await request("POST", path, body, "text/plain");
  if (!response.headers.get("content-type")?.includes("application/json")) return response.text();
  const value: unknown = await response.json();
  if (typeof value !== "string") throw new Error(`${path} did not return text`);
  return value;
}

function requireFields<T>(value: unknown, label: string, check: (record: Record<string, unknown>) => boolean): T {
  if (typeof value !== "object" || value === null || !check(value as Record<string, unknown>)) {
    throw new Error(`${label} response has an unexpected shape: ${JSON.stringify(value)?.slice(0, 200)}`);
  }
  return value as T;
}

export function asCompareResult(value: unknown): CompareResult {
  return requireFields<CompareResult>(value, "compare", (r) =>
    [r.changes, r.steps, r.objects].every(Array.isArray) && typeof r.diffHash === "string" && typeof r.dialect === "string",
  );
}

export function asPatchResult(value: unknown): PatchResult {
  return requireFields<PatchResult>(value, "patch", (r) =>
    typeof r.sql === "string" && typeof r.hash === "string" && Array.isArray(r.acks) && Array.isArray(r.selection),
  );
}

/** Blocks until the comparison run finishes; the run is recorded under `request.run`. */
export async function compare(request: CompareRequest): Promise<CompareResult> {
  return asCompareResult(await post("/schema/compare", request));
}

// Typed commands take their cobra flag names as body keys.
export async function patch({ diffHash, ...rest }: PatchRequest): Promise<PatchResult> {
  return asPatchResult(await post("/schema/patch", { ...rest, "diff-hash": diffHash }));
}

function asHistoryRecord(value: unknown): HistoryRecord {
  return requireFields<HistoryRecord>(value, "history", (r) =>
    typeof r.id === "string" && typeof r.kind === "string" && typeof r.status === "string" && typeof r.createdAt === "string" && Array.isArray(r.schemas),
  );
}

/** Newest first, without payloads. */
export async function historyList({ kind, connection, limit }: HistoryFilter = {}): Promise<HistoryRecord[]> {
  const params = new URLSearchParams();
  if (kind) params.set("kind", kind);
  if (connection) params.set("connection", connection);
  if (limit) params.set("limit", String(limit));
  const query = params.toString();
  const body: unknown = await (await request("GET", `/history${query ? `?${query}` : ""}`)).json();
  if (!Array.isArray(body)) throw new Error(`history list response is not an array: ${JSON.stringify(body)?.slice(0, 200)}`);
  return body.map(asHistoryRecord);
}

/** The record with its payload; an ApiError with status 404 when the run is not recorded (yet). */
export async function historyGet(id: string): Promise<HistoryRecord> {
  return asHistoryRecord(await (await request("GET", `/history/${encodeURIComponent(id)}`)).json());
}

/** Whether the task API knows the run, live or from history; its 404 is the answer, not a failure. */
export async function taskRunKnown(run: string): Promise<boolean> {
  try {
    await request("GET", `/tasks/${encodeURIComponent(run)}`);
    return true;
  } catch (error) {
    if (isNotFound(error)) return false;
    throw error;
  }
}
