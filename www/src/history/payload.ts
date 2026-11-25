import type { InspectSnapshot, Refusal } from "../types";
import type { MigrationPayload } from "../workflow/types";

export type PayloadView = { language: "hcl" | "json"; source: string };

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** The snapshot `inspect --snapshot` returns, or null for a bounded catalogue. */
export function asSnapshot(payload: unknown): InspectSnapshot | null {
  return isRecord(payload) && typeof payload.dialect === "string" && Array.isArray(payload.schemas) && isRecord(payload.schema) ? (payload as InspectSnapshot) : null;
}

/** The HCL a Postgres or SQLite snapshot carries as `schema.native`; null when the dialect has none. */
export function snapshotNative(payload: unknown): string | null {
  const native = asSnapshot(payload)?.schema.native;
  return native ? native : null;
}

export function snapshotRefusals(payload: unknown): Refusal[] {
  return asSnapshot(payload)?.refusals ?? [];
}

/** An inspection's payload: a snapshot with its dialect's HCL, a snapshot without (SQL Server) or a bounded catalogue, shown as JSON. */
export function inspectionView(payload: unknown): PayloadView {
  const native = snapshotNative(payload);
  if (native !== null) return { language: "hcl", source: native };
  return { language: "json", source: JSON.stringify(payload, null, 2) };
}

// A failed migration may keep only the partial result it reached, so every field but the mode is optional.
export function asMigrationPayload(payload: unknown): MigrationPayload {
  if (!isRecord(payload)) {
    throw new Error(`migration record has an unexpected payload: ${JSON.stringify(payload)?.slice(0, 200)}`);
  }
  return payload as MigrationPayload;
}
