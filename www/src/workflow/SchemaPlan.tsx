import { Panel } from "@flanksource/clicky-ui/components";
import { Badge, Callout, CodeBlock } from "@flanksource/clicky-ui/data";
import type { Change, Name, RebuildChange } from "../types";
import type { PlanStep, PlanUnit, ReviewedPlan, Scope } from "./types";

const qualified = (name: Name) => (name.schema ? `${name.schema}.${name.name}` : name.name);

/** "modify column amount on dbo.orders": what a change does, to which member of which object. */
export function describeChange(change: Change | RebuildChange): string {
  return `${change.action} ${change.kind} ${change.member ? `${change.member} on ` : ""}${qualified(change.object)}`;
}

function quantity(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

function scopeText(scope: Scope): string {
  const schemas = scope.schemas?.length ? `: ${scope.schemas.join(", ")}` : "";
  const managed = scope.mode === "declared" ? `, ${quantity(scope.objects?.length ?? 0, "managed object")}` : "";
  return `${scope.mode === "declared" ? "Declared" : "Complete"} scope${schemas}${managed}`;
}

type Lookup = { changes: ReadonlyMap<string, Change | RebuildChange>; steps: ReadonlyMap<string, PlanStep> };

function lookup(plan: ReviewedPlan): Lookup {
  const changes = new Map<string, Change | RebuildChange>();
  for (const change of plan.changes) {
    changes.set(change.id, change);
    for (const rebuild of change.rebuilds ?? []) changes.set(rebuild.id, rebuild);
  }
  const steps = new Map(plan.steps.map((step) => [step.id, step]));
  const listed = new Set((plan.units ?? []).flatMap((unit) => unit.steps));
  for (const step of plan.steps) {
    if (!listed.has(step.id)) throw new Error(`statement ${step.id} names unit ${step.unit ?? "(none)"}, which the plan does not list`);
  }
  return { changes, steps };
}

/** What a reviewer reads before approving a schema plan: the changes with the rebuilds they force, then the statements by unit. */
export function SchemaPlan({ plan }: { plan: ReviewedPlan }) {
  const known = lookup(plan);
  const units = plan.units ?? [];
  return (
    <section aria-label="Schema changes" className="space-y-4">
      <h2 className="text-sm font-semibold">Schema changes</h2>
      {plan.changes.length === 0 && <p className="text-sm text-muted-foreground">No schema changes</p>}
      {plan.scope && <p className="text-sm text-muted-foreground">{scopeText(plan.scope)}</p>}
      {plan.changes.length > 0 && <PlannedChanges changes={plan.changes} />}
      {!!plan.preserved?.length && (
        <section aria-label="Drops the plan leaves out" className="space-y-1">
          <h3 className="text-sm font-medium">Drops the plan leaves out</h3>
          <p className="text-xs text-muted-foreground">The target has these objects and members and the plan does not drop them: either their table is declared partial, or no declaration covers them and the scope is declared. Hover an entry for the rule that applied.</p>
          <ul className="list-inside list-disc text-sm">
            {plan.preserved.map((change) => <li key={change.id} title={change.reason}>{describeChange(change)}</li>)}
          </ul>
        </section>
      )}
      {!!plan.refusals?.length && (
        <section aria-label="Objects the planner will not touch">
          <Callout variant="warning" title="Objects the planner will not touch">
            <ul className="list-inside list-disc text-sm">
              {plan.refusals.map((refusal) => (
                <li key={`${qualified(refusal.object)}.${refusal.member ?? ""}`}>
                  {qualified(refusal.object)}{refusal.member ? `.${refusal.member}` : ""}: {refusal.reason}
                </li>
              ))}
            </ul>
          </Callout>
        </section>
      )}
      {units.map((unit, index) => <Unit key={unit.id} unit={unit} number={index + 1} known={known} />)}
    </section>
  );
}

function PlannedChanges({ changes }: { changes: ReviewedPlan["changes"] }) {
  return (
    <section aria-label="Planned changes" className="space-y-2">
      <h3 className="text-sm font-medium">Planned changes</h3>
      <ul className="space-y-2">
        {changes.map((change) => (
          <li key={change.id} className="rounded-lg border border-border p-3">
            <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
              <span>{describeChange(change)}</span>
              {change.destructive && <Badge tone="danger">Destructive</Badge>}
            </p>
            {change.reason && <p className="text-xs text-muted-foreground">{change.reason}</p>}
            {!!change.rebuilds?.length && (
              <div className="mt-2 space-y-1">
                <p className="text-xs text-muted-foreground">Dropped and re-created around this change</p>
                <ul aria-label={`Rebuilds forced by ${describeChange(change)}`} className="list-inside list-disc text-sm">
                  {change.rebuilds.map((rebuild) => <li key={rebuild.id}>{describeChange(rebuild)}</li>)}
                </ul>
              </div>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}

function Unit({ unit, number, known }: { unit: PlanUnit; number: number; known: Lookup }) {
  return (
    <section aria-label={`Unit ${number}`}>
      <Panel
        title={
          <span className="flex flex-wrap items-center gap-2">
            Unit {number}
            <Badge tone={unit.transactional ? "neutral" : "warning"}>{unit.transactional ? "Transactional" : "Non-transactional"}</Badge>
          </span>
        }
      >
        <p className="mb-3 text-xs text-muted-foreground">
          <code className="break-all">{unit.id}</code> · {unit.objects.map(qualified).join(", ")}
        </p>
        {!!unit.preconditions?.length && (
          <div className="mb-3 space-y-2">
            <h4 className="text-sm font-medium">Checks that run before any change</h4>
            <ul aria-label="Checks that run before any change" className="space-y-2">
              {unit.preconditions.map((check) => (
                <li key={check.id} className="space-y-1">
                  <p className="text-sm">{check.message}</p>
                  <p className="text-xs text-muted-foreground">Passes when this query returns no rows.</p>
                  <CodeBlock language="sql" source={check.sql} copyable />
                </li>
              ))}
            </ul>
          </div>
        )}
        <ol aria-label="Statements" className="list-decimal space-y-3 pl-5">
          {unit.steps.map((id) => <Statement key={id} step={step(known, id)} known={known} />)}
        </ol>
      </Panel>
    </section>
  );
}

function step(known: Lookup, id: string): PlanStep {
  const found = known.steps.get(id);
  if (!found) throw new Error(`unit lists statement ${id}, which the plan does not carry`);
  return found;
}

function Statement({ step, known }: { step: PlanStep; known: Lookup }) {
  if (!step.change) throw new Error(`statement ${step.id} names no change`);
  if (!step.phase) throw new Error(`statement ${step.id} has no phase`);
  const change = known.changes.get(step.change);
  if (!change) throw new Error(`statement ${step.id} implements change ${step.change}, which the plan does not list`);
  return (
    <li className="space-y-1">
      <p className="flex flex-wrap items-center gap-2 text-sm">
        <Badge>{step.phase}</Badge>{" "}
        <span>{describeChange(change)}</span>
        {step.destructive && <Badge tone="danger">Destructive</Badge>}
      </p>
      {step.nonTransactional && <p className="text-xs text-muted-foreground">Runs outside a transaction</p>}
      <CodeBlock language="sql" source={step.sql} copyable />
    </li>
  );
}
