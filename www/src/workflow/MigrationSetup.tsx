import { useMemo, useRef, useState } from "react";
import {
  Button,
  Checkbox,
  InputField,
  Combobox,
} from "@flanksource/clicky-ui/components";
import { Callout } from "@flanksource/clicky-ui/data";
import { MonacoEditor } from "@flanksource/clicky-ui/monaco";
import { useDraft } from "./drafts";
import { PROJECT_CONFIG, previewText, uploadMigrationBundle, type BundleFile, type LocalProject, type ProjectPreview } from "./projectUpload";
import type { Source } from "./types";

export function MigrationSetup({
  disabled,
  targetReady,
  project,
  onPlan,
}: {
  disabled: boolean;
  targetReady: boolean;
  project: LocalProject | undefined;
  onPlan: (input: Record<string, unknown>) => void;
}) {
  const [content, setContent] = useDraft("migration-content");
  const hasLocalMappings = !!(project?.preview || project?.error);
  const [kind, setKind] = useDraft("migration-kind", hasLocalMappings && !content ? "project" : "hcl");
  const [filesJSON, setFiles] = useDraft("migration-files", "[]");
  const [previewJSON, setPreview] = useDraft("migration-project-preview");
  const [variables, setVariables] = useDraft("migration-variables");
  const [routines, setRoutines] = useDraft("migration-routines");
  const [allowDrops, setAllowDrops] = useState(false);
  const [error, setError] = useState("");
  const [uploading, setUploading] = useState(false);
  const uploadID = useRef(0);
  const bundleFiles = useMemo(() => JSON.parse(filesJSON) as BundleFile[], [filesJSON]);
  const preview = previewJSON ? JSON.parse(previewJSON) as ProjectPreview : undefined;
  async function upload(files: FileList | null) {
    if (!files?.length) return;
    const id = ++uploadID.current;
    setUploading(true);
    setError("");
    try {
      if (kind === "bundle") {
        setFiles("[]");
        setPreview("");
        const uploaded = await uploadMigrationBundle(files);
        if (uploadID.current !== id) return;
        setFiles(JSON.stringify(uploaded.files));
        setPreview(uploaded.preview ? JSON.stringify(uploaded.preview) : "");
      } else {
        const file = files[0];
        if (!file) throw new Error("No file selected");
        const text = await file.text();
        if (uploadID.current === id) setContent(text);
      }
    } catch (failure) {
      if (uploadID.current === id) setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      if (uploadID.current === id) setUploading(false);
    }
  }
  function plan() {
    try {
      if (!["hcl", "snapshot", "bundle", "artifact", "project"].includes(kind))
        throw new Error("Unknown source type");
      if (kind === "snapshot" || kind === "artifact") JSON.parse(content);
      const source: Source =
        kind === "bundle"
          ? {
              kind,
              files: bundleFiles,
            }
          : kind === "project" ? { kind } : { kind: kind as Source["kind"], content };
      setError("");
      onPlan({
        source,
        ...(kind === "bundle" || kind === "project"
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
          onChange={(value) => {
            uploadID.current++;
            setUploading(false);
            setError("");
            setKind(value);
          }}
          options={[
            ...(hasLocalMappings ? [{ value: "project", label: "Local project" }] : []),
            { value: "hcl", label: "HCL schema" },
            { value: "snapshot", label: "Migration snapshot JSON" },
            { value: "bundle", label: "HCL / SQL project folder" },
            { value: "artifact", label: "Reviewed artifact JSON" },
          ]}
        />
      </label>
      {kind !== "project" && <label className="block space-y-1 text-sm">
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
      </label>}
      {kind === "project" ? (
        <div className="space-y-2 rounded-md border border-border p-3 text-sm">
          {project?.config && <code className="block break-all">{project.config}</code>}
          {project?.preview && <p>{previewText(project.preview)}</p>}
          {project?.error && <Callout variant="caution" title="Check project mappings">{project.error}</Callout>}
          {!hasLocalMappings && <p>Select another source or add mappings to {PROJECT_CONFIG}.</p>}
        </div>
      ) : null}
      {kind === "bundle" || kind === "project" ? (
        <>
          {kind === "bundle" && <p role="status" className="text-sm text-muted-foreground">
            {uploading ? "Checking uploaded sources…" : preview
              ? <><code>{PROJECT_CONFIG}</code> · {previewText(preview)}</>
              : `${bundleFiles.length} files selected.`}
          </p>}
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
              A relative directory inside the SQL Server project or bundle.
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
          disabled || uploading ||
          (kind !== "artifact" && !targetReady) ||
          (kind === "project" ? !project?.preview : kind === "bundle" ? bundleFiles.length === 0 : !content.trim())
        }
        onClick={plan}
      >
        {kind === "artifact" ? "Review artifact" : "Generate reviewed plan"}
      </Button>
    </section>
  );
}
