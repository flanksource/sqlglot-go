// Pure derivations over a CompareResult shared by the review and patch screens.
import type { Change, CompareResult, CompareStep, Name, ObjectDiff } from "../types";

export type ChangeCounts = { added: number; dropped: number; modified: number; destructive: number };

export function countChanges(changes: readonly Change[]): ChangeCounts {
  const count = (action: Change["action"]) => changes.filter((change) => change.action === action).length;
  return { added: count("add"), dropped: count("drop"), modified: count("modify"), destructive: changes.filter((change) => change.destructive).length };
}

export function objectKey(kind: string, name: Name): string {
  return `${kind}:${name.schema ? `${name.schema}.` : ""}${name.name}`;
}

export type ObjectGroup = { key: string; object: ObjectDiff; changes: Change[] };

/** Groups changes under the object that names them; every change must belong to an object. */
export function groupByObject(result: CompareResult): ObjectGroup[] {
  const byId = new Map(result.changes.map((change) => [change.id, change]));
  const covered = new Set<string>();
  const groups = result.objects.map((object) => {
    const key = objectKey(object.kind, object.name);
    const changes = object.changes.map((changeId) => {
      const change = byId.get(changeId);
      if (!change) throw new Error(`object ${key} lists unknown change ${changeId}`);
      covered.add(changeId);
      return change;
    });
    return { key, object, changes };
  });
  const orphan = result.changes.find((change) => !covered.has(change.id));
  if (orphan) throw new Error(`change ${orphan.id} is not covered by any object`);
  return groups;
}

/** The steps that carry the group's selected changes. */
export function stepsFor(steps: readonly CompareStep[], group: ObjectGroup, selected: ReadonlySet<string>): CompareStep[] {
  const own = new Set(group.changes.map((change) => change.id).filter((changeId) => selected.has(changeId)));
  return steps.filter((step) => step.changes.some((changeId) => own.has(changeId)));
}

export function changeLabel(change: Change): string {
  return [change.action, change.kind, change.member].filter(Boolean).join(" ");
}

/** The reference a picker stores for a dashboard connection: `connection://[namespace/]name`. */
export function connectionRef(connection: { name: string; namespace: string }): string {
  return `connection://${connection.namespace ? `${connection.namespace}/` : ""}${connection.name}`;
}

export function parseSchemas(value: string): string[] | undefined {
  const schemas = value.split(",").map((schema) => schema.trim()).filter(Boolean);
  return schemas.length ? schemas : undefined;
}

const shellQuote = (value: string) => `'${value.replaceAll("'", `'\\''`)}'`;

export function applyCommand({ target, hash, acks }: { target: string; hash: string; acks: readonly string[] }): string {
  return [
    "sqlglot migrate apply --artifact artifact.json",
    `--dsn ${shellQuote(target)}`,
    `--hash ${shellQuote(hash)}`,
    ...acks.map((ack) => `--ack ${shellQuote(ack)}`),
  ].join(" ");
}

export function isDriftError(message: string): boolean {
  return /drift/i.test(message);
}
