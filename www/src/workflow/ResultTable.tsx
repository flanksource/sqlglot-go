import { useMemo } from "react";
import { DataTable } from "@flanksource/clicky-ui/data";

export function ResultTable({
  rows,
  onSelect,
}: {
  rows: Record<string, unknown>[];
  onSelect?: (row: Record<string, unknown>) => void;
}) {
  const columns = useMemo(
    () =>
      [...new Set(rows.flatMap(Object.keys))].map((key) => ({
        key,
        label: key.replaceAll("_", " "),
        accessor: (row: Record<string, unknown>) => row[key],
        sortable: true,
        filterable: true,
      })),
    [rows],
  );
  return (
    <div className="flex h-[26rem] min-h-0 flex-col">
      <DataTable
        data={rows}
        columns={columns}
        autoFilter
        {...(onSelect ? { onRowClick: onSelect } : {})}
        className="min-h-0 flex-1"
      />
    </div>
  );
}
