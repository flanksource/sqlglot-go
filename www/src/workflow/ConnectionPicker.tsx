import { useState } from "react";
import {
  Combobox,
  InputField,
  SegmentedControl,
} from "@flanksource/clicky-ui/components";
import { useConnectionDashboard } from "@flanksource/clicky-ui/connections";
import { Callout } from "@flanksource/clicky-ui/data";
import { API_BASE } from "../api";
import { connectionOption } from "./connectionOption";
import { PROJECT_TARGET_PREFIX } from "./projectUpload";

function isNamedTarget(value: string) {
  return value.startsWith("connection://") || value.startsWith(PROJECT_TARGET_PREFIX);
}

export function ConnectionPicker({
  label,
  value,
  onChange,
  projectTargets = [],
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  projectTargets?: string[];
}) {
  const [choice, setMode] = useState<"saved" | "dsn">(
    value && !isNamedTarget(value) ? "dsn" : "saved",
  );
  const mode = value
    ? isNamedTarget(value)
      ? "saved"
      : "dsn"
    : choice;
  const connections = useConnectionDashboard({ apiBase: API_BASE });
  return (
    <div className="min-w-0 space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-medium">{label}</span>
        <SegmentedControl<"saved" | "dsn">
          aria-label={`${label} source`}
          size="sm"
          value={mode}
          options={[
            { id: "saved", label: "Saved" },
            { id: "dsn", label: "Connection string" },
          ]}
          onChange={(next) => {
            setMode(next);
            onChange("");
          }}
        />
      </div>
      {mode === "saved" ? (
        <Combobox
          ariaLabel={label}
          value={value}
          onChange={onChange}
          options={[
            ...projectTargets.map((target) => ({ value: PROJECT_TARGET_PREFIX + target, label: `${target} (project)` })),
            ...(connections.data?.connections ?? [])
              .filter((connection) => !connection.warnings?.length)
              .map(connectionOption),
          ]}
          allowCustomValue={false}
          loading={connections.isPending && projectTargets.length === 0}
          placeholder="Select connection…"
        />
      ) : (
        <label className="block space-y-1 text-sm">
          <span className="sr-only">{label}</span>
          <InputField
            aria-label={label}
            value={value}
            onChange={onChange}
            type="password"
            autoComplete="off"
          />
          <span className="block text-xs text-muted-foreground">
            Enter a DSN or SQLite path. Kept in memory; omitted from URLs and
            redacted in history.
          </span>
        </label>
      )}
      {connections.isError && mode === "saved" && (
        <Callout variant="caution" title="Connections could not be loaded">
          {connections.error.message}
        </Callout>
      )}
    </div>
  );
}
