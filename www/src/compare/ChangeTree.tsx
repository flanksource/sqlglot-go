import { useMemo } from "react";
import { Button, Checkbox } from "@flanksource/clicky-ui/components";
import { Badge, Tree, type BadgeTone } from "@flanksource/clicky-ui/data";
import { UiTable } from "@flanksource/clicky-ui/icons";
import type { Change, ChangeAction, ObjectStatus } from "../types";
import { changeLabel, type ObjectGroup } from "./model";

type Node = { key: string; group: ObjectGroup; change?: Change; children?: Node[] };

const ACTION_TONE: Record<ChangeAction, BadgeTone> = { add: "success", drop: "danger", modify: "warning" };
const STATUS_GLYPH: Record<ObjectStatus, string> = { added: "+", deleted: "−", changed: "Δ" };
const STATUS_TONE: Record<ObjectStatus, BadgeTone> = { added: "success", deleted: "danger", changed: "warning" };

export function qualifiedName({ object }: ObjectGroup): string {
  return object.name.schema ? `${object.name.schema}.${object.name.name}` : object.name.name;
}

function toNodes(groups: ObjectGroup[]): Node[] {
  return groups.map((group) => ({
    key: group.key,
    group,
    children: group.changes.map((change) => ({ key: `${group.key}#${change.id}`, group, change })),
  }));
}

export type ChangeTreeProps = {
  groups: ObjectGroup[];
  selected: ReadonlySet<string>;
  focusKey: string;
  onFocus: (key: string) => void;
  onToggle: (ids: string[], checked: boolean) => void;
  onSelectAll: () => void;
  onSelectNone: () => void;
  onSelectDefault: () => void;
};

export function ChangeTree({ groups, selected, focusKey, onFocus, onToggle, onSelectAll, onSelectNone, onSelectDefault }: ChangeTreeProps) {
  const roots = useMemo(() => toNodes(groups), [groups]);
  const total = groups.reduce((sum, group) => sum + group.changes.length, 0);
  return (
    <aside aria-label="Changes" className="flex min-h-0 flex-1 flex-col" data-comparison-changes="">
      <p className="shrink-0 border-b border-border px-3 py-1.5 text-xs tabular-nums text-muted-foreground">{selected.size} / {total} selected</p>
      <div className="min-h-0 flex-1 overflow-y-auto py-2">
        <Tree<Node>
          roots={roots}
          ariaLabel="Changes by object"
          showControls={false}
          getChildren={(node) => node.children}
          getKey={(node) => node.key}
          getAriaLabel={(node) => (node.change ? changeLabel(node.change) : qualifiedName(node.group))}
          defaultOpen={() => true}
          selected={roots.find((node) => node.key === focusKey) ?? null}
          onSelect={(node) => onFocus(node.group.key)}
          rowClass={(_node, active) => `min-h-9 border-l-2 ${active ? "border-primary bg-primary/10" : "border-transparent"}`}
          renderRow={({ node }) => (node.change ? <ChangeRow change={node.change} checked={selected.has(node.change.id)} onToggle={onToggle} /> : <ObjectRow group={node.group} selected={selected} onToggle={onToggle} />)}
        />
      </div>
      <div className="flex shrink-0 items-center justify-between gap-1 border-t border-border px-3 py-2">
        <Button variant="ghost" size="sm" onClick={onSelectAll}>Select all</Button>
        <Button variant="ghost" size="sm" onClick={onSelectDefault} title="Select every non-destructive change">Non-destructive</Button>
        <Button variant="ghost" size="sm" disabled={selected.size === 0} onClick={onSelectNone}>Clear</Button>
      </div>
    </aside>
  );
}

function ObjectRow({ group, selected, onToggle }: { group: ObjectGroup; selected: ReadonlySet<string>; onToggle: ChangeTreeProps["onToggle"] }) {
  const ids = group.changes.map((change) => change.id);
  const count = ids.filter((id) => selected.has(id)).length;
  return (
    <>
      <Checkbox aria-label={`Select all changes of ${qualifiedName(group)}`} checked={count === ids.length} onClick={(event) => event.stopPropagation()} onChange={(checked) => onToggle(ids, checked)} />
      <UiTable className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 flex-1 truncate text-xs font-medium" title={`${group.object.kind} ${qualifiedName(group)}`}>
        <span className="text-muted-foreground">{group.object.kind} </span>{qualifiedName(group)}
      </span>
      <span title={group.object.status}><Badge tone={STATUS_TONE[group.object.status]} size="xs">{STATUS_GLYPH[group.object.status]}</Badge></span>
      <span className="w-9 text-right text-[11px] tabular-nums text-muted-foreground">{count}/{ids.length}</span>
    </>
  );
}

function ChangeRow({ change, checked, onToggle }: { change: Change; checked: boolean; onToggle: ChangeTreeProps["onToggle"] }) {
  const requires = change.requires?.length ? ` · requires ${change.requires.length} other change${change.requires.length === 1 ? "" : "s"}` : "";
  return (
    <>
      <Checkbox aria-label={`Select ${changeLabel(change)}`} checked={checked} onClick={(event) => event.stopPropagation()} onChange={(next) => onToggle([change.id], next)} />
      <Badge tone={ACTION_TONE[change.action]} size="xs">{change.action}</Badge>
      <span className="min-w-0 flex-1 truncate text-xs" title={`${changeLabel(change)}${requires}`}>
        {[change.kind, change.member].filter(Boolean).join(" ")}
      </span>
      {change.destructive && <span title={change.reason ?? "destructive"}><Badge tone="danger" variant="solid" size="xs">destructive</Badge></span>}
    </>
  );
}
