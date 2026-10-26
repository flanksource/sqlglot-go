import { useRef, useState } from "react";
import {
  Button,
  Checkbox,
  CopyButton,
  InputField,
  Panel,
  useConfirm,
} from "@flanksource/clicky-ui/components";
import { Badge, Callout, CodeBlock } from "@flanksource/clicky-ui/data";
import { downloadText } from "../download";
import type { ReviewedPlan, ApplyInput } from "./types";

function targetLabel(target: string): string {
  if (target.startsWith("connection://")) return target;
  try {
    const url = new URL(target);
    return `${url.protocol}//${url.host}${url.pathname}`;
  } catch {
    return "Unsaved connection";
  }
}

export function MigrationReview({
  plan,
  target,
  onApply,
  busy,
}: {
  plan: ReviewedPlan;
  target: string;
  onApply: (input: ApplyInput) => void;
  busy: boolean;
}) {
  const [selection, setSelection] = useState<{
    hash: string;
    target: string;
    ids: string[];
  }>({ hash: plan.hash, target, ids: [] });
  const [timeout, setTimeout] = useState("5s");
  const [error, setError] = useState("");
  const { confirm, dialog } = useConfirm();
  const latest = useRef({ hash: plan.hash, target });
  latest.current = { hash: plan.hash, target };
  const acknowledged =
    selection.hash === plan.hash && selection.target === target
      ? selection.ids
      : [];
  const ready =
    plan.applicable &&
    !!target &&
    plan.acks.every((id) => acknowledged.includes(id)) &&
    !busy &&
    timeout.trim() !== "";
  async function apply() {
    if (!ready) return;
    setError("");
    const hash = plan.hash;
    const accepted = await confirm({
      title: "Apply reviewed migration?",
      message: `Target: ${targetLabel(target)}\nSchema: ${plan.schema}\nReviewed hash: ${hash}\n${plan.steps.length} SQL steps will run.`,
      confirmLabel: "Apply reviewed migration",
      variant: plan.acks.length ? "destructive" : "default",
    });
    if (!accepted) return;
    if (latest.current.hash !== hash || latest.current.target !== target) {
      setError("The target or plan changed. Review it again before applying.");
      return;
    }
    onApply({ hash, ack: acknowledged, "lock-timeout": timeout });
  }
  return (
    <div className="space-y-4">
      <Panel
        title="Reviewed migration"
        actions={
          <>
            <Button
              size="sm"
              variant="outline"
              onClick={() =>
                downloadText({
                  filename: "artifact.json",
                  content: JSON.stringify(plan.artifact, null, 2),
                  type: "application/json",
                })
              }
            >
              Download artifact
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() =>
                downloadText({
                  filename: "migration.sql",
                  content: plan.sql,
                  type: "application/sql",
                })
              }
            >
              Download SQL
            </Button>
          </>
        }
      >
        <dl className="grid gap-2 text-sm sm:grid-cols-[auto_1fr]">
          <dt>Dialect / schema</dt>
          <dd>
            {plan.dialect} / {plan.schema}
          </dd>
          <dt>Reviewed hash</dt>
          <dd className="flex min-w-0 items-center gap-2">
            <code className="break-all text-xs">{plan.hash}</code>
            <CopyButton value={plan.hash} label="Copy reviewed hash" />
          </dd>
        </dl>
      </Panel>
      {plan.warnings.map((warning, index) => (
        <Callout key={index} variant="warning" title="Planning warning">
          {warning}
        </Callout>
      ))}
      {!plan.applicable && (
        <Callout variant="warning" title="Preview only">
          This plan includes advisory or unsupported phases and cannot be
          applied.
        </Callout>
      )}
      <section aria-label="Ordered migration steps" className="space-y-3">
        {plan.steps.map((step, index) => (
          <Panel
            key={`${index}:${step.id}`}
            title={
              <span className="flex items-center gap-2">
                Step {index + 1}{" "}
                {step.destructive && <Badge tone="danger">Destructive</Badge>}
              </span>
            }
          >
            <code className="mb-2 block break-all text-xs text-muted-foreground">
              {step.id}
            </code>
            <CodeBlock language="sql" source={step.sql} copyable />
          </Panel>
        ))}
      </section>
      {plan.steps.length === 0 && (
        <Callout variant="note" title="No SQL changes">
          The desired schema already matches the target.
        </Callout>
      )}
      <section
        aria-label="Application approval"
        className="space-y-3 rounded-lg border border-border p-4"
      >
        <h2 className="text-sm font-semibold">
          Apply to {targetLabel(target)}
        </h2>
        {!target && (
          <p className="text-sm text-muted-foreground">
            Select the target connection again before applying this recorded
            plan.
          </p>
        )}
        {plan.acks.map((id) => (
          <label key={id} className="flex items-start gap-2 text-sm">
            <Checkbox
              aria-label={`Acknowledge ${id}`}
              checked={acknowledged.includes(id)}
              onChange={(checked) =>
                setSelection({
                  hash: plan.hash,
                  target,
                  ids: checked
                    ? [...acknowledged, id]
                    : acknowledged.filter((value) => value !== id),
                })
              }
            />
            <span>
              Acknowledge destructive step{" "}
              <code className="break-all text-xs">{id}</code>
            </span>
          </label>
        ))}
        <label className="block max-w-xs space-y-1 text-sm">
          <span className="font-medium">Migration lock timeout</span>
          <InputField
            aria-label="Migration lock timeout"
            value={timeout}
            onChange={setTimeout}
          />
          <span className="block text-xs text-muted-foreground">
            Duration such as 5s or 1m; maximum 24h.
          </span>
        </label>
        <Button disabled={!ready} onClick={() => void apply()}>
          {busy ? "Applying…" : "Review application"}
        </Button>
        {error && (
          <Callout variant="caution" title="Review required">
            {error}
          </Callout>
        )}
      </section>
      {dialog}
    </div>
  );
}
