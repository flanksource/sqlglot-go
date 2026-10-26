import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Button,
  InputField,
  Combobox,
} from "@flanksource/clicky-ui/components";
import { Callout, CodeBlock } from "@flanksource/clicky-ui/data";
import { post } from "../api";
import type { Navigate, Route } from "../compare/route";
import { downloadBytes } from "../download";

export function ResultExport({
  value,
  route,
  navigate,
}: {
  value: unknown;
  route: Route;
  navigate: Navigate;
}) {
  const formats = useQuery({
    queryKey: ["render-formats"],
    queryFn: async () => {
      const result = await post("/render/formats", {});
      if (!result || typeof result !== "object" || Array.isArray(result))
        throw new Error("Invalid format catalog");
      return result as Record<string, string>;
    },
  });
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const format = route.outputFormat ?? "json";
  const expression = route.outputFilter ?? "";
  const filterable =
    Array.isArray(value) &&
    value.every(
      (row) => row !== null && typeof row === "object" && !Array.isArray(row),
    );
  async function render(download: boolean) {
    setBusy(true);
    setError("");
    try {
      const result = (await post("/render", {
        value,
        "output-format": format,
        expression: filterable ? expression : "",
      })) as { data: string; contentType: string };
      if (
        typeof result.data !== "string" ||
        typeof result.contentType !== "string"
      )
        throw new Error("Invalid rendered result");
      const bytes = Uint8Array.from(atob(result.data), (character) =>
        character.charCodeAt(0),
      );
      if (download)
        downloadBytes({
          filename: `result.${format === "pretty" || format === "llm" || format === "toon" ? "txt" : format}`,
          content: bytes,
          type: result.contentType,
        });
      else
        setPreview(
          format === "pdf"
            ? "PDF output is available through Download."
            : new TextDecoder().decode(bytes),
        );
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section
      aria-label="Result export"
      className="space-y-3 rounded-lg border border-border p-3"
    >
      <div className="flex flex-wrap items-end gap-3">
        <label className="block text-sm">
          <span className="mb-1 block font-medium">Output format</span>
          <Combobox
            ariaLabel="Output format"
            allowCustomValue={false}
            value={format}
            onChange={(outputFormat) => navigate({ outputFormat })}
            options={Object.keys(formats.data ?? {}).map((key) => ({
              value: key,
              label: key.toUpperCase(),
            }))}
          />
        </label>
        {filterable && (
          <label className="min-w-56 flex-1 text-sm">
            <span className="mb-1 block font-medium">Result filter (CEL)</span>
            <InputField
              aria-label="Result filter"
              value={expression}
              onChange={(outputFilter) => navigate({ outputFilter })}
            />
            <span className="text-xs text-muted-foreground">
              Filters output rows; preserves the reviewed artifact.
            </span>
          </label>
        )}
        <Button
          size="sm"
          variant="outline"
          disabled={busy || !formats.data}
          onClick={() => void render(false)}
        >
          Preview output
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={busy || !formats.data}
          onClick={() => void render(true)}
        >
          Download output
        </Button>
      </div>
      {(error || formats.isError) && (
        <Callout variant="caution" title="Output could not be rendered">
          {error || formats.error?.message}
        </Callout>
      )}
      {preview !== null && (
        <CodeBlock
          language={format === "json" ? "json" : "text"}
          source={preview}
          copyable
        />
      )}
    </section>
  );
}
