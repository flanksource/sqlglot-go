import type { MigrationPayload } from "../types";

export type PayloadView = { language: "hcl" | "json"; source: string };

/** An inspection's payload: Postgres and SQLite carry the HCL they were rendered to, SQL Server its catalogue. */
export function inspectionView(payload: unknown): PayloadView {
  if (typeof payload === "object" && payload !== null && "hcl" in payload && typeof payload.hcl === "string") return { language: "hcl", source: payload.hcl };
  return { language: "json", source: JSON.stringify(payload, null, 2) };
}

// A failed migration may keep only the partial result it reached, so every field is optional.
export function asMigrationPayload(payload: unknown): MigrationPayload {
  if (typeof payload !== "object" || payload === null || Array.isArray(payload)) {
    throw new Error(`migration record has an unexpected payload: ${JSON.stringify(payload)?.slice(0, 200)}`);
  }
  return payload as MigrationPayload;
}
