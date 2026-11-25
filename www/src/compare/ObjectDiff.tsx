import { Badge, CodeBlock, CodeDiff } from "@flanksource/clicky-ui/data";
import type { CompareStep } from "../types";
import { qualifiedName } from "./ChangeTree";
import { changeLabel, type ObjectGroup } from "./model";

export function ObjectDiff({ group }: { group: ObjectGroup }) {
  const { object } = group;
  return (
    <section aria-label={`Diff of ${qualifiedName(group)}`} className="flex min-w-0 flex-col gap-3 p-3" data-comparison-diff="">
      <header className="flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-semibold"><span className="text-muted-foreground">{object.kind} </span>{qualifiedName(group)}</h2>
        <Badge tone={object.status === "added" ? "success" : object.status === "deleted" ? "danger" : "warning"} size="xs">{object.status}</Badge>
        <span className="text-xs text-muted-foreground">{object.language}</span>
      </header>
      {group.rebuilds.length > 0 && (
        <div className="space-y-1">
          <p className="text-xs text-muted-foreground">Dropped and re-created around these changes</p>
          <ul aria-label="Forced rebuilds" className="list-inside list-disc text-xs">
            {group.rebuilds.map((rebuild) => {
              const cause = group.changes.find((change) => change.id === rebuild.cause);
              if (!cause) throw new Error(`rebuild ${rebuild.id} names cause ${rebuild.cause}, which is not a change of ${qualifiedName(group)}`);
              return <li key={rebuild.id}>{changeLabel(rebuild)} (forced by {changeLabel(cause)})</li>;
            })}
          </ul>
        </div>
      )}
      <CodeDiff
        view="split"
        language={object.language}
        original={object.before ?? ""}
        modified={object.after ?? ""}
        originalLabel="Before (target)"
        modifiedLabel="After (reference)"
      />
    </section>
  );
}

// The SQL the focused object's selected changes run, one block per step.
export function MigrationSteps({ group, steps, selected }: { group: ObjectGroup; steps: CompareStep[]; selected: ReadonlySet<string> }) {
  const labels = new Map(group.changes.map((change) => [change.id, changeLabel(change)]));
  return (
    <section aria-label={`Migration SQL of ${qualifiedName(group)}`} className="space-y-2 p-3" data-comparison-steps="">
      {steps.length === 0 ? (
        <p className="text-sm text-muted-foreground">No selected change of {qualifiedName(group)}. Tick a change to see the SQL it runs.</p>
      ) : (
        steps.map((step) => (
          <div key={step.changes.join("|")} className="space-y-1">
            <p className="text-xs text-muted-foreground">
              {step.changes.filter((id) => selected.has(id) && labels.has(id)).map((id) => labels.get(id)).join(" · ")}
            </p>
            <CodeBlock language="sql" source={step.sql} copyable />
          </div>
        ))
      )}
    </section>
  );
}
