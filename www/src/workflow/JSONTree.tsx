import { useMemo } from "react";
import { Tree, CodeBlock } from "@flanksource/clicky-ui/data";

type Node = { key: string; label: string; value: unknown };

function children(node: Node): Node[] | undefined {
  if (typeof node.value !== "object" || node.value === null) return undefined;
  return Object.entries(node.value).map(([key, value]) => ({
    key: `${node.key}/${key}`,
    label: key,
    value,
  }));
}

function description(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return `${value.length} items`;
  if (typeof value === "object") {
    const record = value as Record<string, unknown>;
    return typeof record.name === "string"
      ? record.name
      : `${Object.keys(record).length} fields`;
  }
  return String(value);
}

export function JSONTree({
  value,
  label = "Result",
  selected = "",
  onSelect,
}: {
  value: unknown;
  label?: string;
  selected?: string;
  onSelect?: (key: string) => void;
}) {
  const roots = useMemo(() => [{ key: "root", label, value }], [value, label]);
  const focus = selected
    .split("/")
    .slice(1)
    .reduce<unknown>(
      (current, key) =>
        current !== null && typeof current === "object"
          ? (current as Record<string, unknown>)[key]
          : undefined,
      value,
    );
  return (
    <div className="grid min-w-0 gap-3 lg:grid-cols-2">
      <Tree<Node>
        roots={roots}
        ariaLabel={label}
        getKey={(node) => node.key}
        getChildren={children}
        defaultOpen={(node) => node.key === "root"}
        getAriaLabel={(node) => `${node.label}: ${description(node.value)}`}
        onSelect={(node) => onSelect?.(node.key)}
        rowClass={(node) => (node.key === selected ? "bg-accent" : "")}
        renderRow={({ node }) => (
          <span className="min-w-0 truncate text-xs">
            <span className="font-medium">{node.label}</span>
            <span className="ml-2 text-muted-foreground">
              {description(node.value)}
            </span>
          </span>
        )}
      />
      {selected && focus !== undefined && (
        <CodeBlock
          language="json"
          source={JSON.stringify(focus, null, 2)}
          copyable
        />
      )}
    </div>
  );
}
