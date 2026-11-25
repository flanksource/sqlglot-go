import { Badge, Callout, CodeBlock, type BadgeTone } from "@flanksource/clicky-ui/data";
import { isScriptApplyResult, type ApplyResult, type MigrationPayload, type ScriptApplyResult, type UnitOutcome } from "./types";

const UNIT_TONE: Record<UnitOutcome, BadgeTone> = { applied: "success", "already-applied": "success", failed: "danger", "commit-unknown": "warning" };

function quantity(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

export function MigrationOutcome({ value }: { value: MigrationPayload }) {
  if (!value.result)
    return (
      <Callout variant="warning" title="No execution result">
        The run did not produce an execution outcome. Read the error before starting another run.
      </Callout>
    );
  return <ApplyOutcome result={value.result} />;
}

export function ApplyOutcome({ result }: { result: ApplyResult | ScriptApplyResult }) {
  return (
    <section className="space-y-3">
      {isScriptApplyResult(result) ? <ScriptOutcome result={result} /> : <SchemaOutcome result={result} />}
      <CodeBlock language="json" source={JSON.stringify(result, null, 2)} copyable />
    </section>
  );
}

function SchemaOutcome({ result }: { result: ApplyResult }) {
  const uncertain = result.outcome === "commit-unknown";
  const units = result.units ?? [];
  const applied = units.filter((unit) => unit.outcome === "applied" || unit.outcome === "already-applied").length;
  return (
    <>
      <Callout
        variant={uncertain || result.outcome === "failed" ? "caution" : "note"}
        title={
          uncertain
            ? "Commit outcome unknown"
            : result.outcome === "already-applied"
              ? "Already applied"
              : result.outcome === "applied"
                ? "Migration applied"
                : "Migration failed"
        }
      >
        {uncertain
          ? "Inspect the target before attempting another application. A commit response was lost; this is not a confirmed rollback."
          : `${quantity(result.steps, "statement")} ran${units.length ? `; ${applied} of ${quantity(units.length, "unit")} applied` : ""}. Plan hash: ${result.hash}`}
      </Callout>
      {units.length > 0 && (
        <ul aria-label="Unit outcomes" className="space-y-1 text-sm">
          {units.map((unit) => (
            <li key={unit.id} className="flex items-center gap-2">
              <code className="break-all text-xs">{unit.id}</code>{" "}
              <Badge tone={UNIT_TONE[unit.outcome]} size="xs">{unit.outcome}</Badge>{" "}
              <span className="text-muted-foreground">{quantity(unit.statements, "statement")}</span>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

function ScriptOutcome({ result }: { result: ScriptApplyResult }) {
  const failed = result.outcome === "failed" || result.outcome === "partial" || result.outcome === "commit-unknown" || result.outcome === "stale";
  return (
    <>
      <Callout variant={failed ? "caution" : result.outcome === "applied-with-warnings" ? "warning" : "note"} title={`Scripts ${result.outcome.replaceAll("-", " ")}`}>
        {quantity(result.applied.length, "script")} applied
      </Callout>
      {result.schema === "partial" && (
        <Callout variant="caution" title="Schema step partially applied">
          The schema step committed some units and then failed. Plan again to cover the rest.
        </Callout>
      )}
      {result.schema === "applied" && <p className="text-sm">Schema step applied.</p>}
      {!!result.blocked?.length && <p className="text-sm">Blocked: {result.blocked.join(", ")}</p>}
      {!!(result.failed?.length || result.warnings?.length || result.skipped?.length) && (
        <ul aria-label="Script failures" className="space-y-1 text-sm">
          {result.failed?.map((item) => <li key={`failed:${item.script}`}>{item.script} {item.outcome}: {item.error}</li>)}
          {result.warnings?.map((item) => <li key={`warning:${item.script}`}>{item.script} {item.outcome}: {item.error}</li>)}
          {result.skipped?.map((item) => <li key={`skipped:${item.script}`}>{item.script} skipped: {item.dependency} did not run</li>)}
        </ul>
      )}
    </>
  );
}
