// Pure selection logic for the review screen.
//
// A change's `requires` lists the change ids that must be selected for it to be
// valid. Ticking a change therefore pulls in its requirements (transitively) and
// unticking one removes everything that (transitively) requires it. Requirements
// may be mutual (a SQLite rebuild group): the members then toggle together.
//
// The selection that reaches the URL is `encodeSelection`: change ids are JSON
// strings (`["add","column","dbo","orders","amount"]`) that contain commas and
// quotes, so each id is base64url-encoded (UTF-8) and tokens are joined with ",".
// Ids are encoded rather than indexed so a URL keeps its meaning when the compare
// result is recomputed; an id that no longer exists is rejected, never remapped.

import type { Change } from "../types";

export type ChangeGraph = {
  ids: string[];
  destructive: ReadonlySet<string>;
  requires: ReadonlyMap<string, readonly string[]>;
  dependents: ReadonlyMap<string, readonly string[]>;
};

export function buildGraph(changes: readonly Change[]): ChangeGraph {
  const requires = new Map<string, readonly string[]>();
  for (const change of changes) {
    if (requires.has(change.id)) throw new Error(`duplicate change id ${change.id}`);
    requires.set(change.id, change.requires ?? []);
  }
  const dependents = new Map<string, string[]>([...requires.keys()].map((id) => [id, []]));
  for (const [id, needs] of requires) {
    for (const need of needs) {
      const list = dependents.get(need);
      if (!list) throw new Error(`change ${id} requires unknown change ${need}`);
      list.push(id);
    }
  }
  return {
    ids: [...requires.keys()],
    destructive: new Set(changes.filter((change) => change.destructive).map((change) => change.id)),
    requires,
    dependents,
  };
}

function reach(edges: ReadonlyMap<string, readonly string[]>, start: string): Set<string> {
  if (!edges.has(start)) throw new Error(`unknown change ${start}`);
  const seen = new Set([start]);
  const stack = [start];
  while (stack.length) {
    for (const next of edges.get(stack.pop()!)!) {
      if (seen.has(next)) continue;
      seen.add(next);
      stack.push(next);
    }
  }
  return seen;
}

/** Ticks `id` and everything it transitively requires. */
export function select(graph: ChangeGraph, selected: ReadonlySet<string>, id: string): Set<string> {
  return new Set([...selected, ...reach(graph.requires, id)]);
}

/** Unticks `id` and everything that transitively requires it. */
export function deselect(graph: ChangeGraph, selected: ReadonlySet<string>, id: string): Set<string> {
  const removed = reach(graph.dependents, id);
  return new Set([...selected].filter((item) => !removed.has(item)));
}

/** Ticks or unticks several changes at once, e.g. every change of one object. */
export function setMany(graph: ChangeGraph, selected: ReadonlySet<string>, ids: readonly string[], checked: boolean): Set<string> {
  return ids.reduce((current, id) => (checked ? select(graph, current, id) : deselect(graph, current, id)), new Set(selected));
}

/** Requirements of selected changes that are not themselves selected. */
export function missingRequirements(graph: ChangeGraph, selected: ReadonlySet<string>): string[] {
  const missing = new Set<string>();
  for (const id of selected) {
    for (const need of graph.requires.get(id) ?? []) if (!selected.has(need)) missing.add(need);
  }
  return [...missing].sort();
}

/**
 * Every non-destructive change, minus any whose requirements are not all in the
 * set (a requirement that is destructive, or part of a group with a destructive
 * member), so the default is always a valid selection.
 */
export function defaultSelection(graph: ChangeGraph): Set<string> {
  const selected = new Set(graph.ids.filter((id) => !graph.destructive.has(id)));
  for (let changed = true; changed; ) {
    changed = false;
    for (const id of [...selected]) {
      if (graph.requires.get(id)!.every((need) => selected.has(need))) continue;
      selected.delete(id);
      changed = true;
    }
  }
  return selected;
}

const TOKEN = /^[A-Za-z0-9_-]+$/;

function toToken(id: string): string {
  const bytes = new TextEncoder().encode(id);
  return btoa(String.fromCharCode(...bytes)).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

function fromToken(token: string): string {
  if (!TOKEN.test(token)) throw new Error(`invalid selection token ${JSON.stringify(token)}`);
  const base64 = token.replaceAll("-", "+").replaceAll("_", "/");
  try {
    const binary = atob(base64.padEnd(Math.ceil(base64.length / 4) * 4, "="));
    return new TextDecoder("utf-8", { fatal: true }).decode(Uint8Array.from(binary, (char) => char.charCodeAt(0)));
  } catch {
    throw new Error(`invalid selection token ${JSON.stringify(token)}`);
  }
}

export type SelectionState = { selected: Set<string>; problem: string | null };

/**
 * The selection a route stands for: the default when the URL carries none,
 * otherwise the decoded ids. A URL naming ids that are not in this comparison
 * (or a malformed token) is reported as a `problem`, never silently remapped.
 */
export function readSelection(graph: ChangeGraph, encoded: string | null): SelectionState {
  if (encoded === null) return { selected: defaultSelection(graph), problem: null };
  let decoded: Set<string>;
  try {
    decoded = decodeSelection(encoded);
  } catch (error) {
    return { selected: new Set(), problem: error instanceof Error ? error.message : String(error) };
  }
  const known = new Set([...decoded].filter((id) => graph.requires.has(id)));
  const unknown = decoded.size - known.size;
  return { selected: known, problem: unknown ? `${unknown} selected change${unknown === 1 ? " is" : "s are"} not part of this comparison` : null };
}

export function encodeSelection(selected: ReadonlySet<string>): string {
  return [...selected].map(toToken).sort().join(",");
}

export function decodeSelection(encoded: string): Set<string> {
  return new Set(encoded.split(",").filter(Boolean).map(fromToken));
}
