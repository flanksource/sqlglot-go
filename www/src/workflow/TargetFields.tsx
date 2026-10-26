import { InputField } from "@flanksource/clicky-ui/components";
import type { Navigate, Route } from "../compare/route";
import { ConnectionPicker } from "./ConnectionPicker";

export function TargetFields({
  value,
  onChange,
  route,
  navigate,
}: {
  value: string;
  onChange: (value: string) => void;
  route: Route;
  navigate: Navigate;
}) {
  return (
    <section
      aria-label="Database target"
      className="grid gap-3 rounded-lg border border-border p-4 md:grid-cols-2"
    >
      <ConnectionPicker
        label="Target connection"
        value={value}
        onChange={onChange}
      />
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="space-y-1 text-sm">
          <span className="block font-medium">
            Schema{" "}
            <span className="font-normal text-muted-foreground">optional</span>
          </span>
          <InputField
            aria-label="Target schema"
            value={route.workflowSchema ?? ""}
            onChange={(workflowSchema) => navigate({ workflowSchema })}
          />
          <span className="block text-xs text-muted-foreground">
            Defaults to main, public or dbo.
          </span>
        </label>
        <label className="space-y-1 text-sm">
          <span className="block font-medium">
            Driver override{" "}
            <span className="font-normal text-muted-foreground">optional</span>
          </span>
          <InputField
            aria-label="Driver override"
            value={route.workflowDriver ?? ""}
            onChange={(workflowDriver) => navigate({ workflowDriver })}
          />
          <span className="block text-xs text-muted-foreground">
            Must match the connection dialect.
          </span>
        </label>
      </div>
    </section>
  );
}
