import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import {
  Button,
  Checkbox,
  InputField,
  SegmentedControl,
} from "@flanksource/clicky-ui/components";
import { Callout } from "@flanksource/clicky-ui/data";
import { useConnectionDashboard } from "@flanksource/clicky-ui/connections";
import { API_BASE, post } from "../api";
import { connectionRef } from "../compare/model";
import type { Navigate, Route } from "../compare/route";
import { RunGate } from "../compare/RunGate";
import { useDraft } from "./drafts";
import { InspectionResult } from "./InspectionResult";
import { ResultTable } from "./ResultTable";
import { TargetFields } from "./TargetFields";
import { JSONTree } from "./JSONTree";
import { ResultExport } from "./ResultExport";
import { useOperation } from "./useOperation";

export function Inspect({
  route,
  navigate,
}: {
  route: Route;
  navigate: Navigate;
}) {
  const [raw, setRaw] = useDraft("inspection-target");
  const target = route.workflowConnection || raw;
  const mode = route.inspectionMode ?? "catalog";
  const operation = useOperation("/schema/inspect", (inspectionRun) =>
    navigate({ inspectionRun }),
  );
  const changeTarget = (value: string) => {
    setRaw(value);
    navigate({
      workflowConnection: value.startsWith("connection://") ? value : "",
      inspectionRun: "",
    });
  };
  return (
    <div className="mx-auto w-full max-w-6xl space-y-4 py-4">
      <header>
        <h1 className="text-xl font-semibold">Inspect</h1>
        <p className="text-sm text-muted-foreground">
          Browse a catalog, capture a complete migration snapshot, or inspect a
          saved connection’s fields.
        </p>
      </header>
      <TargetFields
        value={target}
        onChange={changeTarget}
        route={route}
        navigate={navigate}
      />
      <SegmentedControl<string>
        aria-label="Inspection mode"
        value={mode}
        options={[
          { id: "catalog", label: "Catalog" },
          { id: "snapshot", label: "Migration snapshot" },
          { id: "fields", label: "Fields and capabilities" },
        ]}
        onChange={(inspectionMode) =>
          navigate({ inspectionMode, inspectionRun: "" })
        }
      />
      {mode === "fields" ? (
        <FieldInspection
          key={target}
          target={target}
          route={route}
          navigate={navigate}
        />
      ) : (
        <>
          <Button
            disabled={!target || operation.busy}
            onClick={() =>
              operation.start({
                dsn: target,
                schema: route.workflowSchema ?? "",
                driver: route.workflowDriver ?? "",
                snapshot: mode === "snapshot",
              })
            }
          >
            {operation.busy ? "Inspecting…" : "Inspect database"}
          </Button>
          {route.inspectionRun && (
            <RunGate
              run={route.inspectionRun}
              kind="inspection"
              parse={(value) => value}
              progressTitle="Inspecting database…"
              failureActions={() => null}
              missingActions={null}
            >
              {(value) => (
                <InspectionResult
                  value={value}
                  route={route}
                  navigate={navigate}
                />
              )}
            </RunGate>
          )}
        </>
      )}
    </div>
  );
}

function FieldInspection({
  target,
  route,
  navigate,
}: {
  target: string;
  route: Route;
  navigate: Navigate;
}) {
  const dashboard = useConnectionDashboard({ apiBase: API_BASE });
  const connection = dashboard.data?.connections.find(
    (item) => connectionRef(item) === target,
  );
  const name = route.inspectionTarget ?? "";
  const setName = (inspectionTarget: string) => navigate({ inspectionTarget });
  const database = route.inspectionDatabase ?? "";
  const setDatabase = (inspectionDatabase: string) =>
    navigate({ inspectionDatabase });
  const kind = route.inspectionTargetKind ?? "";
  const setKind = (inspectionTargetKind: string) =>
    navigate({ inspectionTargetKind });
  const [sample, setSample] = useState(false);
  const [refresh, setRefresh] = useState(false);
  const inspection = useMutation({
    mutationFn: async () => {
      if (!connection)
        throw new Error("Choose a saved connection for field inspection.");
      return (await post(
        `/connection/${encodeURIComponent(connection.id)}/inspect`,
        {
          target: name,
          database,
          "target-kind": kind,
          schema: route.workflowSchema ?? "",
          sample,
          refresh,
        },
      )) as {
        targets?: Record<string, unknown>[];
        result?: { fields?: Record<string, unknown>[] };
      };
    },
    retry: false,
  });
  return (
    <section className="space-y-3">
      {!connection && (
        <Callout variant="note" title="Saved connection required">
          Field inspection resolves a connection resource. Use catalog mode for
          a direct DSN.
        </Callout>
      )}
      <label className="block max-w-md text-sm">
        Database (optional)
        <InputField
          aria-label="Inspection database"
          value={database}
          onChange={setDatabase}
        />
      </label>
      <label className="block max-w-md text-sm">
        Target kind (optional)
        <InputField aria-label="Target kind" value={kind} onChange={setKind} />
        <span className="text-xs text-muted-foreground">
          For OpenSearch: index, alias, data stream, or pattern.
        </span>
      </label>
      <label className="block max-w-md space-y-1 text-sm">
        <span className="font-medium">
          Table or target{" "}
          <span className="text-muted-foreground">optional</span>
        </span>
        <InputField
          aria-label="Inspection target"
          value={name}
          onChange={setName}
        />
        <span className="block text-xs text-muted-foreground">
          Leave empty to list targets. Set the schema above when inspecting a
          table.
        </span>
      </label>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox
          checked={sample}
          onChange={setSample}
          aria-label="Sample selected target"
        />
        Sample selected target to resolve cardinality and filters
      </label>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox
          checked={refresh}
          onChange={setRefresh}
          aria-label="Refresh inspection metadata"
        />
        Refresh metadata and connection secrets
      </label>
      <Button
        disabled={!connection || inspection.isPending || (sample && !name)}
        onClick={() => inspection.mutate()}
      >
        {inspection.isPending ? "Inspecting fields…" : "Inspect fields"}
      </Button>
      {inspection.isError && (
        <Callout variant="caution" title="Field inspection failed">
          {inspection.error.message}
        </Callout>
      )}
      {inspection.data && (
        <>
          <ResultTable
            rows={
              inspection.data.result?.fields ??
              (inspection.data.targets ?? []).map((row) =>
                Object.fromEntries(
                  Object.entries(row).filter(([key]) => key !== "inspect"),
                ),
              )
            }
            onSelect={(row) => {
              if (!inspection.data.result && typeof row.target === "string") {
                navigate({
                  inspectionTarget: row.target,
                  inspectionTargetKind:
                    typeof row.kind === "string" ? row.kind : "",
                  ...(typeof row.schema === "string"
                    ? { workflowSchema: row.schema }
                    : {}),
                });
              }
            }}
          />
          <JSONTree
            value={inspection.data.result ?? inspection.data}
            label="Field capabilities and metadata"
          />
          <ResultExport
            value={
              inspection.data.result?.fields ?? inspection.data.targets ?? []
            }
            route={route}
            navigate={navigate}
          />
        </>
      )}
    </section>
  );
}
