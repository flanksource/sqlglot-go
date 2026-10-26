import { useMemo, type ReactNode } from "react";
import { Button, Workspace, type WorkspacePaneSpec } from "@flanksource/clicky-ui/components";
import { Badge, Callout } from "@flanksource/clicky-ui/data";
import { UiArrowLeft, UiDatabase, UiRefresh } from "@flanksource/clicky-ui/icons";
import { connectionName, recordedAt } from "../history/format";
import type { CompareResult } from "../types";
import { ChangeTree } from "./ChangeTree";
import { countChanges, groupByObject, stepsFor, type ObjectGroup } from "./model";
import { MigrationSteps, ObjectDiff } from "./ObjectDiff";
import { useStartPatch } from "./queries";
import type { Navigate, Route } from "./route";
import { ResultGate, type Recompare } from "./ResultGate";
import type { RunMeta } from "./RunGate";
import { RunProgress } from "./RunProgress";
import { buildGraph, encodeSelection, missingRequirements, readSelection, setMany } from "./selection";
import { ConnectionPicker } from "../workflow/ConnectionPicker";
import { useDraft } from "../workflow/drafts";

function Side({ reference }: { reference: string }) {
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5" title={reference}>
      <UiDatabase className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <span className="truncate font-semibold">{connectionName(reference)}</span>
    </span>
  );
}

function Stat({ children, tone }: { children: ReactNode; tone?: "added" | "removed" | "changed" }) {
  const color = tone === "added" ? "text-green-700 dark:text-green-400" : tone === "removed" ? "text-red-700 dark:text-red-400" : tone === "changed" ? "text-amber-700 dark:text-amber-400" : "text-muted-foreground";
  return <span className={`shrink-0 text-xs tabular-nums ${color}`}>{children}</span>;
}

// ComparisonHeader is the chrome above the review and patch screens: back, the
// compared connections, the real change counts, then the screen's actions.
export function ComparisonHeader({ result, back, actions }: { result: CompareResult; back: { label: string; onClick: () => void }; actions: ReactNode }) {
  const counts = countChanges(result.changes);
  return (
    <header className="flex min-h-11 min-w-0 flex-wrap items-center gap-x-3 gap-y-2 border-b border-border px-1 pb-2 text-sm">
      <Button size="sm" variant="ghost" onClick={back.onClick}><UiArrowLeft className="size-4" />{back.label}</Button>
      <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[11px] font-medium">{result.dialect}</span>
      <span className="flex min-w-0 items-center gap-2"><Side reference={result.reference} /><span aria-hidden className="text-muted-foreground">→</span><Side reference={result.target} /></span>
      <span className="flex items-center gap-2" aria-label="Change counts">
        <Stat tone="added">+{counts.added} added</Stat>
        <Stat tone="removed">−{counts.dropped} dropped</Stat>
        <Stat tone="changed">Δ{counts.modified} modified</Stat>
        {counts.destructive > 0 && <Badge tone="danger" size="xs">{counts.destructive} destructive</Badge>}
      </span>
      <span className="ml-auto flex shrink-0 items-center gap-2">{actions}</span>
    </header>
  );
}

export function Review({ route, navigate }: { route: Route; navigate: Navigate }) {
  return (
    <ResultGate route={route} navigate={navigate}>
      {(result, recompare, meta) => <ReviewBody route={route} navigate={navigate} result={result} recompare={recompare} meta={meta} />}
    </ResultGate>
  );
}

// ReviewLayout is the review's workspace: the changes on the left, the focused
// object's diff in the middle, its migration SQL below and the run's log aside.
function ReviewLayout({ route, navigate, result, groups, focus, selected, setSelection, graph }: {
  route: Route;
  navigate: Navigate;
  result: CompareResult;
  groups: ObjectGroup[];
  focus: ObjectGroup;
  selected: ReadonlySet<string>;
  setSelection: (next: ReadonlySet<string>) => void;
  graph: ReturnType<typeof buildGraph>;
}) {
  const panes: WorkspacePaneSpec[] = [
    {
      id: "changes",
      label: "Changes",
      location: "left",
      width: 384,
      minWidth: 256,
      contentClassName: "flex flex-col",
      content: (
        <ChangeTree
          groups={groups}
          selected={selected}
          focusKey={focus.key}
          onFocus={(object) => navigate({ object })}
          onToggle={(ids, checked) => setSelection(setMany(graph, selected, ids, checked))}
          onSelectAll={() => setSelection(new Set(graph.ids))}
          onSelectNone={() => setSelection(new Set())}
          onSelectDefault={() => navigate({ selected: null })}
        />
      ),
    },
    { id: "diff", label: "Diff", location: "center", showHeader: false, collapsible: false, content: <ObjectDiff group={focus} /> },
    { id: "sql", label: "Migration SQL", location: "bottom", content: <MigrationSteps group={focus} steps={stepsFor(result.steps, focus, selected)} selected={selected} /> },
    // Stacked under Changes: a collapsed right-side pane's expand control sits beneath Workspace's top-right toggles.
    { id: "run", label: "Run", location: "left", width: 384, minWidth: 256, height: 260, defaultCollapsed: true, content: <div className="p-3"><RunProgress run={route.run} /></div> },
  ];
  return <Workspace storageKey="sqlglot-review" panes={panes} className="min-h-0 flex-1 rounded-lg border border-border" />;
}

function ReviewBody({ route, navigate, result, recompare, meta }: { route: Route; navigate: Navigate; result: CompareResult; recompare: Recompare; meta: RunMeta }) {
  const [reference, setReference] = useDraft("compare-reference");
  const [target, setTarget] = useDraft("compare-target");
  const needsReference = !result.reference.startsWith("connection://");
  const needsTarget = !result.target.startsWith("connection://");
  const credentialsReady = (!needsReference || reference !== "") && (!needsTarget || target !== "");
  const groups = useMemo(() => groupByObject(result), [result]);
  const graph = useMemo(() => buildGraph(result.changes), [result]);
  const patching = useStartPatch(navigate);
  const { selected, problem } = readSelection(graph, route.selected);
  const missing = missingRequirements(graph, selected);
  const setSelection = (next: ReadonlySet<string>) => navigate({ selected: encodeSelection(next) });
  const focus = groups.find((group) => group.key === route.object) ?? groups[0];
  const select = result.changes.map((change) => change.id).filter((id) => selected.has(id));
  return (
    <div className="flex h-full min-h-[640px] flex-col gap-3" data-comparison-stage="review">
      <ComparisonHeader
        result={result}
        back={{ label: "Comparisons", onClick: () => navigate({ stage: "setup" }) }}
        actions={<>
          <Button size="sm" variant="ghost" disabled={recompare.running} onClick={recompare.run}><UiRefresh className="size-3.5" />Re-compare</Button>
          <Button size="sm" disabled={!credentialsReady || selected.size === 0 || missing.length > 0 || problem !== null || patching.running} onClick={() => patching.start({ comparison: route.run, result, select })}>
            Create patch ({selected.size})
          </Button>
        </>}
      />
      {(needsReference || needsTarget) && <section className="space-y-2"><p className="text-sm text-muted-foreground">Unsaved connections must be entered again after a reload. Creating a patch checks the reviewed differences against these databases.</p><div className="grid gap-4 md:grid-cols-2">{needsReference && <ConnectionPicker label="Reference connection" value={reference} onChange={setReference} />}{needsTarget && <ConnectionPicker label="Target connection" value={target} onChange={setTarget} />}</div></section>}
      {meta.recorded && (
        <Callout variant="note">
          Recorded {recordedAt(meta.createdAt)} — the databases were not contacted. Creating a patch contacts both and checks them for drift.
        </Callout>
      )}
      {problem && (
        <Callout variant="warning" title="The selection in the URL does not match this comparison">
          {problem}.{" "}
          <Button size="sm" variant="outline" onClick={() => navigate({ selected: null })}>Reset selection</Button>
        </Callout>
      )}
      {missing.length > 0 && (
        <Callout variant="warning" title="The selection is missing required changes">
          {missing.length} change{missing.length === 1 ? "" : "s"} required by the selection {missing.length === 1 ? "is" : "are"} not ticked.{" "}
          <Button size="sm" variant="outline" onClick={() => setSelection(setMany(graph, selected, [...selected], true))}>Add required changes</Button>
        </Callout>
      )}
      {groups.length === 0 || !focus ? (
        <p className="py-10 text-center text-sm text-muted-foreground">The two connections have identical schemas. There is nothing to patch.</p>
      ) : (
        <ReviewLayout route={route} navigate={navigate} result={result} groups={groups} focus={focus} selected={selected} setSelection={setSelection} graph={graph} />
      )}
    </div>
  );
}
