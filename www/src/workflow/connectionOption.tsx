import type { ComboboxOption } from "@flanksource/clicky-ui/components";
import {
  ConnectionTypeIcon,
  type ConnectionDashboardItem,
} from "@flanksource/clicky-ui/connections";
import { connectionRef } from "../compare/model";

export function connectionOption(
  connection: ConnectionDashboardItem,
): ComboboxOption {
  const endpoint = connection.endpoint
    ? ` · ${connection.endpoint.scheme}://${connection.endpoint.host}${connection.endpoint.path ?? ""}`
    : "";
  return {
    value: connectionRef(connection),
    label: connection.name,
    description: `${connection.type}${endpoint}`,
    icon: <ConnectionTypeIcon type={connection.type} className="size-4" />,
    ...(connection.namespace ? { group: connection.namespace } : {}),
  };
}
