import { useQuery } from "@tanstack/react-query";
import { Button } from "@flanksource/clicky-ui/components";
import { Callout, CodeBlock } from "@flanksource/clicky-ui/data";
import { asPatchResult, post } from "../api";
import { useHistoryRecord } from "../compare/queries";
import type { Navigate, Route } from "../compare/route";
import { RunGate } from "../compare/RunGate";
import type { HistoryRecord } from "../types";
import { useDraft } from "./drafts";
import { MigrationReview } from "./MigrationReview";
import { MigrationSetup } from "./MigrationSetup";
import { ResultExport } from "./ResultExport";
import { TargetFields } from "./TargetFields";
import { useOperation } from "./useOperation";
import type { ApplyInput, MigrationPayload, ReviewedPlan } from "./types";

function payload(value: unknown): MigrationPayload {
  if (!value || typeof value !== "object")
    throw new Error("Invalid migration record");
  return value as MigrationPayload;
}

export function Migrations({
  route,
  navigate,
}: {
  route: Route;
  navigate: Navigate;
}) {
  const [raw, setRaw] = useDraft("migration-target");
  const target = route.workflowConnection || raw;
  const run = route.migrationRun ?? "";
  const record = useHistoryRecord(run);
  const planning = useOperation("/migrate/plan", (migrationRun) =>
    navigate({ migrationRun }),
  );
  const applying = useOperation("/migrate/apply", (migrationRun) =>
    navigate({ migrationRun }),
  );
  const fields = {
    dsn: target,
    schema: route.workflowSchema ?? "",
    driver: route.workflowDriver ?? "",
  };
  const apply = (plan: ReviewedPlan, input: ApplyInput) =>
    applying.start({
      ...fields,
      ...input,
      artifactContent: plan.artifact,
      parent: run,
    });
  const review = (plan: ReviewedPlan) => (
    <>
      <MigrationReview
        key={`${plan.hash}:${target}:${fields.schema}:${fields.driver}`}
        plan={plan}
        target={target}
        busy={applying.busy}
        onApply={(input) => apply(plan, input)}
      />
      <ResultExport value={plan.steps} route={route} navigate={navigate} />
    </>
  );
  const failure = (record: HistoryRecord) => (
    <>
      <Callout variant="caution" title="Migration failed">
        {record.error}
      </Callout>
      {record.payload != null && (
        <MigrationOutcome value={payload(record.payload)} />
      )}
      {record.parent && (
        <Button
          variant="outline"
          onClick={() => navigate({ migrationRun: record.parent ?? "" })}
        >
          Return to reviewed plan
        </Button>
      )}
    </>
  );
  return (
    <div className="mx-auto w-full max-w-5xl space-y-4 py-4">
      <header className="flex items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Migrations</h1>
          <p className="text-sm text-muted-foreground">
            Plan, review, and explicitly apply hash-bound migrations.
          </p>
        </div>
        {run && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigate({ migrationRun: "" })}
          >
            New plan
          </Button>
        )}
      </header>
      <TargetFields
        value={target}
        onChange={(value) => {
          setRaw(value);
          navigate({
            workflowConnection: value.startsWith("connection://") ? value : "",
          });
        }}
        route={route}
        navigate={navigate}
      />
      {!run ? (
        <MigrationSetup
          disabled={planning.busy}
          targetReady={!!target}
          onPlan={(input) => planning.start({ ...fields, ...input })}
        />
      ) : record.data?.kind === "patch" ? (
        <RecordedArtifact record={record.data} render={review} />
      ) : (
        <RunGate
          run={run}
          kind="migration"
          parse={payload}
          progressTitle="Processing migration…"
          failureActions={() => null}
          missingActions={
            <Button
              variant="outline"
              onClick={() => navigate({ migrationRun: "" })}
            >
              Start a new plan
            </Button>
          }
          renderFailure={failure}
        >
          {(value) =>
            value.mode === "plan" ? (
              value.plan ? (
                review(value.plan)
              ) : record.data ? (
                <RecordedArtifact record={record.data} render={review} />
              ) : null
            ) : (
              <>
                <MigrationOutcome value={value} />
                {record.data?.parent && (
                  <Button
                    variant="outline"
                    onClick={() =>
                      navigate({ migrationRun: record.data?.parent ?? "" })
                    }
                  >
                    Open reviewed plan
                  </Button>
                )}
              </>
            )
          }
        </RunGate>
      )}
    </div>
  );
}

function RecordedArtifact({
  record,
  render,
}: {
  record: HistoryRecord;
  render: (plan: ReviewedPlan) => React.ReactNode;
}) {
  const query = useQuery({
    queryKey: ["review-artifact", record.id],
    queryFn: async () => {
      const artifact =
        record.kind === "patch"
          ? asPatchResult(record.payload).artifact
          : payload(record.payload).artifact;
      return (await post("/migrate/review", {
        artifactContent: artifact,
        driver: record.dialect,
        schema: record.dialect === "sqlserver" ? "" : record.schemas[0],
      })) as ReviewedPlan;
    },
    staleTime: Infinity,
  });
  if (query.isError)
    return (
      <Callout variant="caution" title="Artifact could not be reviewed">
        {query.error.message}
      </Callout>
    );
  if (!query.data) return <p role="status">Loading reviewed artifact…</p>;
  return <>{render(query.data)}</>;
}

function MigrationOutcome({ value }: { value: MigrationPayload }) {
  const result = value.result;
  if (!result)
    return (
      <Callout variant="warning" title="No execution result">
        The run did not produce an execution outcome. Read the error before
        starting another run.
      </Callout>
    );
  const uncertain = result.outcome === "commit-unknown";
  return (
    <section className="space-y-3">
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
          : `${result.steps} steps reported. Reviewed hash: ${result.hash}`}
      </Callout>
      <CodeBlock
        language="json"
        source={JSON.stringify(result, null, 2)}
        copyable
      />
    </section>
  );
}
