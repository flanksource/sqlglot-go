import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, Combobox } from "@flanksource/clicky-ui/components";
import { Callout, CodeBlock } from "@flanksource/clicky-ui/data";
import { MonacoEditor, type Monaco } from "@flanksource/clicky-ui/monaco";
import type { editor } from "monaco-editor";
import { ApiError, post, postText } from "../api";
import type { Route, Navigate } from "../compare/route";
import { useDraft } from "./drafts";
import { JSONTree } from "./JSONTree";
import { ResultTable } from "./ResultTable";
import { ResultExport } from "./ResultExport";
import type { SQLDiagnostic, SQLMode } from "./types";

const MODES: SQLMode[] = ["parse", "validate", "format", "tokens"];

export function SQLTools({
  route,
  navigate,
}: {
  route: Route;
  navigate: Navigate;
}) {
  const [sql, setSQL] = useDraft("sql-text");
  const [result, setResult] = useState<{
    mode: SQLMode;
    value: unknown;
  } | null>(null);
  const [error, setError] = useState("");
  const [diagnostics, setDiagnostics] = useState<SQLDiagnostic[]>([]);
  const [busy, setBusy] = useState(false);
  const instance = useRef<editor.IStandaloneCodeEditor | null>(null);
  const monaco = useRef<Monaco | null>(null);
  const requestID = useRef(0);
  const dialect = route.sqlDialect ?? "neutral";
  const dialects = useQuery({
    queryKey: ["dialects"],
    queryFn: async () => {
      const value = await post("/dialects", {});
      if (
        !Array.isArray(value) ||
        !value.every((item) => item && typeof item.name === "string")
      )
        throw new Error("Invalid dialect catalog");
      return value as { name: string }[];
    },
  });
  useEffect(() => {
    const model = instance.current?.getModel();
    if (!model || !monaco.current) return;
    monaco.current.editor.setModelMarkers(
      model,
      "sqlglot",
      diagnostics.map((item) => {
        const start = model.getPositionAt(
          [...sql].slice(0, item.start).join("").length,
        );
        const end = model.getPositionAt(
          [...sql].slice(0, Math.max(item.end, item.start + 1)).join("").length,
        );
        return {
          severity: 8,
          message: item.label,
          startLineNumber: start.lineNumber,
          startColumn: start.column,
          endLineNumber: end.lineNumber,
          endColumn: end.column,
        };
      }),
    );
  }, [diagnostics, sql]);
  async function run(mode: SQLMode) {
    const id = ++requestID.current;
    navigate({ sqlMode: mode });
    setBusy(true);
    setError("");
    setDiagnostics([]);
    setResult(null);
    try {
      const value = await (mode === "format" ? postText : post)(`/${mode}`, {
        sql,
        dialect,
      });
      if (id === requestID.current) setResult({ mode, value });
    } catch (failure) {
      if (id === requestID.current) {
        setError(failure instanceof Error ? failure.message : String(failure));
        if (failure instanceof ApiError) setDiagnostics(failure.diagnostics);
      }
    } finally {
      setBusy(false);
    }
  }
  async function upload(file: File | undefined) {
    if (!file) return;
    try {
      const value = await file.text();
      requestID.current++;
      setSQL(value);
      setResult(null);
      setError("");
      setDiagnostics([]);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    }
  }
  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-4 py-4">
      <header>
        <h1 className="text-xl font-semibold">SQL Tools</h1>
        <p className="text-sm text-muted-foreground">
          Parse scripts, validate one query, preserve tokens when formatting,
          and inspect lexical tokens.
        </p>
      </header>
      <div className="flex flex-wrap items-end gap-4">
        <label className="space-y-1 text-sm">
          <span className="block font-medium">SQL dialect</span>
          <Combobox
            ariaLabel="SQL dialect"
            allowCustomValue={false}
            value={dialect}
            onChange={(sqlDialect) => {
              requestID.current++;
              navigate({ sqlDialect });
              setResult(null);
              setDiagnostics([]);
              setError("");
            }}
            options={(dialects.data ?? []).map(({ name }) => ({
              value: name,
              label: name,
            }))}
          />
        </label>
        <label className="space-y-1 text-sm">
          <span className="block font-medium">Upload SQL file</span>
          <input
            type="file"
            accept=".sql,text/plain"
            onChange={(event) => void upload(event.target.files?.[0])}
          />
        </label>
      </div>
      {dialects.isError && (
        <Callout variant="caution" title="Dialects could not be loaded">
          {dialects.error.message}
        </Callout>
      )}
      <section aria-label="SQL editor" className="space-y-2">
        <h2 className="text-sm font-medium">SQL</h2>
        <MonacoEditor
          path="sqlglot-input.sql"
          language="sql"
          value={sql}
          onChange={(next) => {
            requestID.current++;
            setSQL(next);
            setResult(null);
            setDiagnostics([]);
            setError("");
          }}
          height="22rem"
          onMount={(value, api) => {
            instance.current = value;
            monaco.current = api;
            value.updateOptions({ ariaLabel: "SQL" });
          }}
        />
      </section>
      <div className="flex flex-wrap gap-2">
        {MODES.map((mode) => (
          <Button
            key={mode}
            variant={route.sqlMode === mode ? "default" : "outline"}
            disabled={busy || !sql.trim()}
            onClick={() => void run(mode)}
          >
            {mode.charAt(0).toUpperCase() + mode.slice(1)}
          </Button>
        ))}
        {busy && (
          <span role="status" className="text-sm">
            Processing SQL…
          </span>
        )}
      </div>
      {error && (
        <Callout variant="caution" title="SQL could not be processed">
          {error}
        </Callout>
      )}
      {diagnostics.map((item, index) => (
        <Button
          key={index}
          variant="ghost"
          className="justify-start whitespace-normal text-left"
          onClick={() => {
            const model = instance.current?.getModel();
            if (model && instance.current) {
              const position = model.getPositionAt(
                [...sql].slice(0, item.start).join("").length,
              );
              instance.current.setPosition(position);
              instance.current.revealPositionInCenter(position);
              instance.current.focus();
            }
          }}
        >
          {item.line}:{item.column} · {item.label}
        </Button>
      ))}
      {result && (
        <>
          {result.mode === "format" && typeof result.value === "string" ? (
            <section className="space-y-2">
              <h2 className="text-sm font-medium">Formatted SQL</h2>
              <CodeBlock language="sql" source={result.value} copyable />
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setSQL(String(result.value));
                  setResult(null);
                }}
              >
                Replace editor content
              </Button>
            </section>
          ) : result.mode === "tokens" && Array.isArray(result.value) ? (
            <ResultTable
              rows={result.value as Record<string, unknown>[]}
              onSelect={(row) => {
                if (typeof row.start === "number") {
                  const model = instance.current?.getModel();
                  if (model) {
                    const position = model.getPositionAt(
                      [...sql].slice(0, row.start).join("").length,
                    );
                    instance.current?.setPosition(position);
                    instance.current?.revealPositionInCenter(position);
                    instance.current?.focus();
                  }
                }
              }}
            />
          ) : (
            <JSONTree
              value={result.value}
              label={
                result.mode === "parse"
                  ? "Expression tree and SQL"
                  : "Validation result"
              }
              selected={route.sqlObject ?? ""}
              onSelect={(sqlObject) => navigate({ sqlObject })}
            />
          )}
          <ResultExport
            value={result.value}
            route={route}
            navigate={navigate}
          />
        </>
      )}
    </div>
  );
}
