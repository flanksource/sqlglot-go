import { TaskProgress } from "@flanksource/clicky-ui/data";
import { useTaskRun } from "@flanksource/clicky-ui/hooks";
import { API_BASE } from "../api";

// The task log of one run. Finished runs are served from the history database,
// so this works for a recorded run as well as a live one.
export function RunProgress({ run, title }: { run: string; title?: string }) {
  const { snapshots } = useTaskRun({ id: run, basePath: API_BASE });
  return <TaskProgress snapshots={snapshots} compact {...(title ? { title } : {})} />;
}
