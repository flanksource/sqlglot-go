import { useMemo } from "react";
import { CodeBlock, Callout } from "@flanksource/clicky-ui/data";
import { SegmentedControl } from "@flanksource/clicky-ui/components";
import type { Navigate, Route } from "../compare/route";
import { snapshotNative, snapshotRefusals } from "../history/payload";
import type { Refusal } from "../types";
import { JSONTree } from "./JSONTree";
import { ResultExport } from "./ResultExport";
import { downloadText } from "../download";
import { Button } from "@flanksource/clicky-ui/components";

/** The objects a snapshot could not read faithfully; they are not in it, and a plan toward it must not touch them. */
export function SnapshotRefusals({ refusals }: { refusals: Refusal[] }) {
  if (refusals.length === 0) return null;
  return (
    <Callout variant="warning" title="Objects the inspector could not read">
      <ul aria-label="Refusals" className="list-inside list-disc text-sm">
        {refusals.map((refusal) => (
          <li key={`${refusal.object.schema}.${refusal.object.name}.${refusal.member ?? ""}`}>
            {refusal.object.schema ? `${refusal.object.schema}.` : ""}{refusal.object.name}{refusal.member ? `.${refusal.member}` : ""}: {refusal.reason}
          </li>
        ))}
      </ul>
    </Callout>
  );
}

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
  const hcl = snapshotNative(value);
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
      <SnapshotRefusals refusals={snapshotRefusals(value)} />
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
