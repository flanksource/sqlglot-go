import type { ReactNode } from "react";
import { Button, InputField } from "@flanksource/clicky-ui/components";
import { Callout } from "@flanksource/clicky-ui/data";
import { useConnectionDashboard } from "@flanksource/clicky-ui/connections";
import { UiAdd, UiSwap } from "@flanksource/clicky-ui/icons";
import { API_BASE } from "../api";
import { useDraft } from "../workflow/drafts";
import { ConnectionPicker } from "../workflow/ConnectionPicker";
export { connectionOption } from "../workflow/connectionOption";
import { parseSchemas } from "./model";
import { useStartComparison } from "./queries";
import { buildRoute, type Navigate, type Route } from "./route";

// SetupCard is the playground's setup layout: two sides with a swap between
// them, an options row, and the primary action with its caveat beside it.
function SetupCard({ sides, options, action, hint }: {
  sides: { reference: ReactNode; target: ReactNode; onSwap: () => void; swapDisabled: boolean };
  options: ReactNode;
  action: ReactNode;
  hint: ReactNode;
}) {
  return (
    <section aria-label="Comparison setup" className="space-y-3 rounded-lg border border-border bg-card p-3">
      <div className="grid items-end gap-x-2 gap-y-2 md:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]">
        {sides.reference}
        <Button variant="ghost" size="sm" className="h-9 w-9 justify-self-center px-0" aria-label="Swap reference and target" title="Swap reference and target" disabled={sides.swapDisabled} onClick={sides.onSwap}>
          <UiSwap className="size-4" />
        </Button>
        {sides.target}
      </div>
      {options}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
        <span className="ml-auto flex min-w-0 items-center gap-3">
          <p className="min-w-0 max-w-md text-right text-xs text-muted-foreground">{hint}</p>
          <span className="flex shrink-0 items-center gap-2">{action}</span>
        </span>
      </div>
    </section>
  );
}

// SidePicker is one side of the comparison: a label, an inline "New connection"
// link to the connections page, and a picker over the dashboard's connections.
function SidePicker({ label, value, onChange, newConnectionHref, onNewConnection }: {
  label: string;
  value: string;
  onChange: (value: string) => void;


  newConnectionHref: string;
  onNewConnection: () => void;
}) {
  return (
    <div className="min-w-0 space-y-1">
      <div className="flex min-h-7 items-center justify-between gap-2">
        <span className="text-sm font-medium">{label}</span>
        <a href={newConnectionHref} className="inline-flex items-center gap-1 text-xs text-primary hover:underline"
          onClick={(event) => { event.preventDefault(); onNewConnection(); }}>
          <UiAdd className="size-3" aria-hidden />New connection
        </a>
      </div>
      <ConnectionPicker label={label} value={value} onChange={onChange} />
    </div>
  );
}

export function Setup({ route, navigate }: { route: Route; navigate: Navigate }) {
  const comparison = useStartComparison(navigate);
  const schemas = parseSchemas(route.schemas);
  const dashboard = useConnectionDashboard({ apiBase: API_BASE });
  // A connection that failed to load is flagged on the connections page and cannot be compared.
  const connections = { ...dashboard, data: dashboard.data?.connections.filter((connection) => !connection.warnings?.length) };
  const [rawReference, setReference] = useDraft("compare-reference");
  const [rawTarget, setTarget] = useDraft("compare-target");
  const reference = route.reference || rawReference;
  const target = route.target || rawTarget;
  const same = reference !== "" && reference === target;
  const ready = reference !== "" && target !== "" && !same;
  const newConnection = { newConnectionHref: buildRoute({ ...route, page: "connections" }), onNewConnection: () => navigate({ page: "connections" }) };
  const side = (label: string, key: "reference" | "target") => (
    <SidePicker label={label} value={key === "reference" ? reference : target} onChange={(value) => { (key === "reference" ? setReference : setTarget)(value); navigate({ [key]: value.startsWith("connection://") ? value : "" }); }} {...newConnection} />
  );
  return (
    <div className="mx-auto max-w-5xl space-y-5 py-4" data-comparison-stage="setup">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold">What would you like to compare?</h1>
        <p className="text-sm text-muted-foreground">The reference is the desired schema; the target is the database to be patched.</p>
      </header>
      {connections.isError && <Callout variant="caution" title="Connections could not be loaded">{connections.error.message}</Callout>}
      {connections.data?.length === 0 &&<Callout variant="note" title="No connections yet">Create the reference and target connections first.</Callout>}
      <SetupCard
        sides={{
          reference: side("Reference (desired)", "reference"),
          target: side("Target (to patch)", "target"),
          onSwap: () => { setReference(target); setTarget(reference); navigate({ reference: target.startsWith("connection://") ? target : "", target: reference.startsWith("connection://") ? reference : "" }); },
          swapDisabled: !reference || !target,
        }}
        options={
          <label className="block space-y-1 text-sm">
            <span className="font-medium">Schemas <span className="text-xs font-normal text-muted-foreground">optional, comma-separated</span></span>
            <InputField aria-label="Schemas" placeholder="main, dbo" value={route.schemas} onChange={(schemas) => navigate({ schemas })} />
          </label>
        }
        action={<Button disabled={!ready || comparison.running} onClick={() => comparison.start({ reference, target, ...(schemas ? { schemas } : {}) })}>Compare</Button>}
        hint={same ? <span role="alert" className="text-destructive">Choose two distinct connections to compare.</span> : "Both connections are opened read-only and must use the same dialect."}
      />
    </div>
  );
}
