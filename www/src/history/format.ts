import { formatRelativeTime, parseTimestamp } from "@flanksource/clicky-ui/data";

/** "5m ago (26/10/2026, 14:03:00)": the relative age with the absolute time behind it. */
export function recordedAt(createdAt: string, now: Date = new Date()): string {
  const date = parseTimestamp(createdAt);
  if (!date) throw new Error(`record has an invalid createdAt: ${createdAt}`);
  return `${formatRelativeTime(date, now)} (${date.toLocaleString()})`;
}

export const connectionName = (reference: string) => reference.replace(/^connection:\/\//, "");
