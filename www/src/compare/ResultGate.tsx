import type { ReactNode } from "react";
import { Button } from "@flanksource/clicky-ui/components";
import { asCompareResult } from "../api";
import type { CompareResult } from "../types";
import { parseSchemas } from "./model";
import { comparisonOf, useStartComparison } from "./queries";
import type { Navigate, Route } from "./route";
import { RunGate, type RunMeta } from "./RunGate";

export type Recompare = { run: () => void; running: boolean };

// ResultGate shows the comparison run the route names: its progress while it
// runs, its recorded result once it has finished, its error if it failed. It
// never inspects a database itself, so a reload or a recorded run is read from history.
export function ResultGate({ route, navigate, children }: { route: Route; navigate: Navigate; children: (result: CompareResult, recompare: Recompare, meta: RunMeta) => ReactNode }) {
  const comparison = useStartComparison(navigate);
  const schemas = parseSchemas(route.schemas);
  const again = { reference: route.reference, target: route.target, ...(schemas ? { schemas } : {}) };
  const backToSetup = <Button variant="outline" size="sm" onClick={() => navigate({ stage: "setup" })}>Back to setup</Button>;
  const canRetry = route.reference !== "" && route.target !== "";
  return (
    <RunGate
      run={route.run}
      kind="comparison"
      parse={asCompareResult}
      progressTitle={canRetry ? `Comparing ${route.reference} with ${route.target}…` : "Comparing…"}
      failureActions={() => (
        <>
          {backToSetup}
          {canRetry && <Button size="sm" disabled={comparison.running} onClick={() => comparison.start(again)}>Re-compare</Button>}
        </>
      )}
      missingActions={backToSetup}
    >
      {(result, meta) => children(result, { run: () => result.reference.startsWith("connection://") && result.target.startsWith("connection://") ? comparison.start(comparisonOf(result)) : navigate({ stage: "setup" }), running: comparison.running }, meta)}
    </RunGate>
  );
}
