import { Button, CopyButton, Panel } from "@flanksource/clicky-ui/components";
import { Badge, Callout, CodeBlock } from "@flanksource/clicky-ui/data";
import { UiDownload, UiRefresh } from "@flanksource/clicky-ui/icons";
import { asPatchResult } from "../api";
import { downloadText } from "../download";
import type { CompareResult, PatchResult } from "../types";
import { applyCommand, changeLabel, isDriftError } from "./model";
import { comparisonOf, useStartComparison } from "./queries";
import { ComparisonHeader } from "./Review";
import { ResultGate } from "./ResultGate";
import type { Navigate, Route } from "./route";
import { RunGate } from "./RunGate";

export function Patch({ route, navigate }: { route: Route; navigate: Navigate }) {
  return (
    <ResultGate route={route} navigate={navigate}>
      {(result) => <PatchBody route={route} navigate={navigate} result={result} />}
    </ResultGate>
  );
}

function PatchBody({ route, navigate, result }: { route: Route; navigate: Navigate; result: CompareResult }) {
  const backToReview = () => navigate({ stage: "review", patch: "" });
  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-4" data-comparison-stage="patch">
      <ComparisonHeader result={result} back={{ label: "Review", onClick: backToReview }} actions={null} />
      {route.patch ? (
        <PatchRun route={route} navigate={navigate} result={result} />
      ) : (
        <Callout variant="warning" title="No patch has been created">
          Select the changes to patch on the review screen and create the patch there.{" "}
          <Button size="sm" variant="outline" onClick={backToReview}>Back to review</Button>
        </Callout>
      )}
    </div>
  );
}

function PatchRun({ route, navigate, result }: { route: Route; navigate: Navigate; result: CompareResult }) {
  const comparison = useStartComparison(navigate);
  const backToReview = <Button size="sm" variant="outline" onClick={() => navigate({ stage: "review", patch: "" })}>Back to review</Button>;
  return (
    <RunGate
      run={route.patch}
      kind="patch"
      parse={asPatchResult}
      progressTitle="Building the patch…"
      failureActions={(message) => (
        <>
          {isDriftError(message) && (
            <Button size="sm" disabled={comparison.running} onClick={() => result.reference.startsWith("connection://") && result.target.startsWith("connection://") ? comparison.start(comparisonOf(result)) : navigate({ stage: "setup" })}>
              <UiRefresh className="size-3.5" />Re-compare
            </Button>
          )}
          {backToReview}
        </>
      )}
      missingActions={backToReview}
    >
      {(patch) => <><Button onClick={() => navigate({ page: "migrations", migrationRun: route.patch, workflowConnection: result.target.startsWith("connection://") ? result.target : "", workflowDriver: result.dialect, workflowSchema: result.schemas.length === 1 ? result.schemas[0] ?? "" : "" })}>Review application</Button><PatchView patch={patch} result={result} /></>}
    </RunGate>
  );
}

function PatchView({ patch, result }: { patch: PatchResult; result: CompareResult }) {
  const labels = new Map(result.changes.map((change) => [change.id, change]));
  const command = applyCommand({ target: result.target, hash: patch.hash, acks: patch.acks });
  return (
    <>
      <Panel title="Patch" actions={
        <>
          <Button size="sm" variant="outline" onClick={() => downloadText({ filename: "patch.sql", content: patch.sql, type: "application/sql" })}><UiDownload className="size-3.5" />patch.sql</Button>
          <Button size="sm" variant="outline" onClick={() => downloadText({ filename: "artifact.json", content: JSON.stringify(patch.artifact, null, 2), type: "application/json" })}><UiDownload className="size-3.5" />artifact.json</Button>
        </>
      }>
        <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
          <dt className="text-muted-foreground">Changes</dt><dd>{patch.selection.length} selected</dd>
          <dt className="text-muted-foreground">Hash</dt>
          <dd className="flex min-w-0 items-center gap-1"><code className="truncate font-mono text-xs" data-patch-hash="">{patch.hash}</code><CopyButton value={patch.hash} label="Copy hash" /></dd>
        </dl>
      </Panel>
      {patch.acks.length > 0 && (
        <Callout variant="caution" title={`${patch.acks.length} destructive change${patch.acks.length === 1 ? "" : "s"} must be acknowledged on apply`}>
          <ul className="space-y-1" aria-label="Destructive acknowledgements">
            {patch.acks.map((ack) => {
              const change = labels.get(ack);
              return (
                <li key={ack} className="flex flex-wrap items-center gap-2 text-sm">
                  <Badge tone="danger" size="xs">{change?.action ?? "ack"}</Badge>
                  <span className="font-medium">{change ? `${changeLabel(change)} on ${change.object.name}` : ack}</span>
                  {change?.reason && <span className="text-muted-foreground">{change.reason}</span>}
                </li>
              );
            })}
          </ul>
        </Callout>
      )}
      <Panel title="SQL script" actions={<CopyButton value={patch.sql} label="Copy SQL" />} padded={false}>
        <CodeBlock language="sql" source={patch.sql} bare className="max-h-[28rem] overflow-auto p-3" />
      </Panel>
      <Panel title="Apply to the target" actions={<CopyButton value={command} label="Copy command" />}>
        <pre className="overflow-x-auto whitespace-pre-wrap break-all font-mono text-xs" data-apply-command="">{command}</pre>
      </Panel>
    </>
  );
}
