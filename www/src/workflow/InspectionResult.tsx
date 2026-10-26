import { useMemo } from "react";
import { CodeBlock, Callout } from "@flanksource/clicky-ui/data";
import { SegmentedControl } from "@flanksource/clicky-ui/components";
import type { Navigate, Route } from "../compare/route";
import { JSONTree } from "./JSONTree";
import { ResultExport } from "./ResultExport";
import { downloadText } from "../download";
import { Button } from "@flanksource/clicky-ui/components";

export function InspectionResult({
  value,
  route,
  navigate,
}: {
  value: unknown;
  route: Route;
  navigate: Navigate;
}) {
  const source = useMemo(() => JSON.stringify(value, null, 2), [value]);
  const hcl =
    value &&
    typeof value === "object" &&
    "hcl" in value &&
    typeof value.hcl === "string"
      ? value.hcl
      : null;
  const truncated = Boolean(
    value &&
    typeof value === "object" &&
    "truncated" in value &&
    value.truncated === true,
  );
  return (
    <section className="space-y-4" aria-label="Inspected schema">
      <div className="flex flex-wrap justify-between gap-2">
        <SegmentedControl<"browse" | "source">
          aria-label="Inspection view"
          value={route.inspectionView === "source" ? "source" : "browse"}
          options={[
            { id: "browse", label: "Browse" },
            { id: "source", label: "Source" },
          ]}
          onChange={(inspectionView) => navigate({ inspectionView })}
        />
        <Button
          size="sm"
          variant="outline"
          onClick={() =>
            downloadText({
              filename: "snapshot.json",
              content: source,
              type: "application/json",
            })
          }
        >
          Download complete result
        </Button>
      </div>
      {truncated && (
        <Callout variant="warning" title="Catalog is truncated">
          This bounded catalog is incomplete. Use snapshot mode for migration
          input.
        </Callout>
      )}
      {route.inspectionView === "source" || hcl !== null ? (
        <CodeBlock
          language={hcl !== null ? "hcl" : "json"}
          source={hcl ?? source}
          copyable
        />
      ) : (
        <JSONTree
          value={value}
          label="Schema objects"
          selected={route.workflowObject ?? ""}
          onSelect={(workflowObject) => navigate({ workflowObject })}
        />
      )}
      <ResultExport value={value} route={route} navigate={navigate} />
    </section>
  );
}
