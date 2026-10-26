import { useState } from "react";
import {
  Button,
  Checkbox,
  InputField,
  Combobox,
} from "@flanksource/clicky-ui/components";
import { Callout } from "@flanksource/clicky-ui/data";
import { MonacoEditor } from "@flanksource/clicky-ui/monaco";
import { useDraft } from "./drafts";
import type { Source } from "./types";

export function MigrationSetup({
  disabled,
  targetReady,
  onPlan,
}: {
  disabled: boolean;
  targetReady: boolean;
  onPlan: (input: Record<string, unknown>) => void;
}) {
  const [kind, setKind] = useDraft("migration-kind", "hcl");
  const [content, setContent] = useDraft("migration-content");
  const [filesJSON, setFiles] = useDraft("migration-files", "[]");
  const [variables, setVariables] = useDraft("migration-variables");
  const [routines, setRoutines] = useDraft("migration-routines");
  const [allowDrops, setAllowDrops] = useState(false);
  const [error, setError] = useState("");
  async function upload(files: FileList | null) {
    if (!files?.length) return;
    try {
      if (kind === "bundle") {
        const entries = await Promise.all(
          Array.from(files).map(async (file) => ({
            path:
              file.webkitRelativePath.split("/").slice(1).join("/") ||
              file.name,
            content: await file.text(),
          })),
        );
        setFiles(JSON.stringify(entries));
      } else {
        const file = files[0];
        if (!file) throw new Error("No file selected");
        setContent(await file.text());
      }
      setError("");
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    }
  }
  function plan() {
    try {
      if (!["hcl", "snapshot", "bundle", "artifact"].includes(kind))
        throw new Error("Unknown source type");
      if (kind === "snapshot" || kind === "artifact") JSON.parse(content);
      const source: Source =
        kind === "bundle"
          ? {
              kind,
              files: JSON.parse(filesJSON) as {
                path: string;
                content: string;
              }[],
            }
          : { kind: kind as Source["kind"], content };
      setError("");
      onPlan({
        source,
        ...(kind === "bundle"
          ? {
              var: variables
                .split("\n")
                .map((value) => value.trim())
                .filter(Boolean),
              routines,
              "allow-drops": allowDrops,
            }
          : {}),
      });
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    }
  }
  return (
    <section className="space-y-4" aria-label="Migration input">
      <label className="block max-w-md space-y-1 text-sm">
        <span className="font-medium">Desired source</span>
        <Combobox
          ariaLabel="Desired source"
          allowCustomValue={false}
          value={kind}
          onChange={setKind}
          options={[
            { value: "hcl", label: "HCL schema" },
            { value: "snapshot", label: "Migration snapshot JSON" },
            { value: "bundle", label: "HCL / SQL bundle folder" },
            { value: "artifact", label: "Reviewed artifact JSON" },
          ]}
        />
      </label>
      <label className="block space-y-1 text-sm">
        <span className="block font-medium">
          {kind === "bundle" ? "Upload bundle folder" : "Upload file"}
        </span>
        <input
          key={kind}
          type="file"
          {...(kind === "bundle"
            ? { webkitdirectory: "", multiple: true }
            : {
                accept:
                  kind === "hcl" ? ".hcl,text/plain" : ".json,application/json",
              })}
          onChange={(event) => void upload(event.target.files)}
        />
      </label>
      {kind === "bundle" ? (
        <>
          <p className="text-sm text-muted-foreground">
            {(JSON.parse(filesJSON) as unknown[]).length} files selected.
            Relative folder structure is preserved; source files remain in
            memory.
          </p>
          <label className="block space-y-1 text-sm">
            <span className="block font-medium">
              Variables{" "}
              <span className="font-normal text-muted-foreground">
                optional
              </span>
            </span>
            <InputField
              aria-label="Bundle variables"
              value={variables}
              onChange={setVariables}
              as="textarea"
              rows={3}
            />
            <span className="block text-xs text-muted-foreground">
              One name=HCL-literal per line, for example prefix="app".
            </span>
          </label>
          <label className="block max-w-md space-y-1 text-sm">
            <span className="block font-medium">
              Routine directory{" "}
              <span className="font-normal text-muted-foreground">
                optional
              </span>
            </span>
            <InputField
              aria-label="Routine directory"
              value={routines}
              onChange={setRoutines}
            />
            <span className="block text-xs text-muted-foreground">
              A relative directory inside the uploaded SQL Server bundle.
            </span>
          </label>
          <label className="flex items-start gap-2 text-sm">
            <Checkbox
              checked={allowDrops}
              onChange={setAllowDrops}
              aria-label="Include omitted objects as removals"
            />
            Include omitted objects as removals. Destructive steps still require
            individual approval.
          </label>
        </>
      ) : (
        <section className="space-y-2">
          <h2 className="text-sm font-medium">
            Paste {kind === "hcl" ? "HCL" : "JSON"}
          </h2>
          <MonacoEditor
            path={`migration-input.${kind === "hcl" ? "hcl" : "json"}`}
            language={kind === "hcl" ? "hcl" : "json"}
            value={content}
            onChange={setContent}
            height="20rem"
          />
        </section>
      )}
      {error && (
        <Callout variant="caution" title="Check migration input">
          {error}
        </Callout>
      )}
      <Button
        disabled={
          disabled ||
          (kind !== "artifact" && !targetReady) ||
          (kind === "bundle" ? filesJSON === "[]" : !content.trim())
        }
        onClick={plan}
      >
        {kind === "artifact" ? "Review artifact" : "Generate reviewed plan"}
      </Button>
    </section>
  );
}
