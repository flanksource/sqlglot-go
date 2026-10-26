import { useMutation, useMutationState, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { compare, historyGet, historyList, patch, taskRunKnown } from "../api";
import type { CompareResult, HistoryFilter, HistoryRecord, PatchResult } from "../types";
import type { Navigate } from "./route";
import { useDraft } from "../workflow/drafts";

export const historyKey = (id: string) => ["history", id] as const;
const RUN_MUTATION = ["run"] as const;

/**
 * One recorded run, read once: a run is immutable after it finishes, and the
 * diffHash of a comparison is what a later patch is validated against, so it
 * only changes through a new run. A run that is not recorded (yet) is an
 * ApiError with status 404, which the gate branches on.
 */
export function useHistoryRecord(id: string): UseQueryResult<HistoryRecord> {
  return useQuery({ queryKey: historyKey(id), queryFn: () => historyGet(id), staleTime: Infinity, retry: false, enabled: id !== "" });
}

export function useHistoryList(filter: HistoryFilter) {
  return useQuery({ queryKey: ["history", "list", filter.kind ?? "", filter.connection ?? "", filter.limit ?? 200], queryFn: () => historyList(filter) });
}

/** Whether the task API serves the run, so its progress can be shown. */
export function useRunKnown(run: string, enabled = true) {
  return useQuery({ queryKey: ["run-known", run], queryFn: () => taskRunKnown(run), enabled: enabled && run !== "" });
}

type RunVariables = { run: string };

/**
 * The state of the in-session request that started `run`, if any. The request
 * outlives the screen that sent it: a reload loses it and the run is then read
 * from history alone.
 */
export function useRunRequest(run: string) {
  const states = useMutationState({
    filters: { mutationKey: RUN_MUTATION, predicate: (mutation) => (mutation.state.variables as RunVariables | undefined)?.run === run },
    select: (mutation) => mutation.state,
  });
  return states.at(-1);
}

// The finished request seeds the record under its run id, so the screen
// renders without waiting on a second round trip to read what it just got.
function useStartRun<R, V extends RunVariables>({ call, record }: { call: (variables: V) => Promise<R>; record: (result: R, variables: V) => HistoryRecord }) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: RUN_MUTATION,
    mutationFn: call,
    onSuccess: (result, variables) => queryClient.setQueryData(historyKey(variables.run), record(result, variables)),
  });
}

const schemasOf = (result: CompareResult) => (result.schemas.length ? { schemas: result.schemas } : {});

export type StartComparison = { reference: string; target: string; schemas?: string[] };

/** Starts a comparison under a fresh run id and shows it; the previous run stays in history. */
export function useStartComparison(navigate: Navigate) {
  const mutation = useStartRun({
    call: ({ run, ...request }: StartComparison & RunVariables) => compare({ ...request, run }),
    record: (result, { run }): HistoryRecord => ({
      id: run,
      kind: "comparison",
      dialect: result.dialect,
      reference: result.reference,
      target: result.target,
      schemas: result.schemas,
      status: "succeeded",
      createdAt: new Date().toISOString(),
      summary: {},
      payload: result,
    }),
  });
  return {
    running: mutation.isPending,
    start: (request: StartComparison) => {
      const run = crypto.randomUUID();
      mutation.mutate({ ...request, run });
      navigate({ stage: "review", run, patch: "", selected: null, object: "" });
    },
  };
}

/** The request that starts a new comparison of what `result` compared. */
export const comparisonOf = (result: CompareResult): StartComparison => ({ reference: result.reference, target: result.target, ...schemasOf(result) });

export type StartPatch = { comparison: string; result: CompareResult; select: string[] };

/** Starts a patch run for a reviewed selection under a fresh run id and shows it. */
export function useStartPatch(navigate: Navigate) {
  const [reference] = useDraft("compare-reference");
  const [target] = useDraft("compare-target");
  const mutation = useStartRun({
    call: ({ run, comparison, result, select }: StartPatch & RunVariables) => patch({ ...comparisonOf(result), reference: result.reference.startsWith("connection://") ? result.reference : reference, target: result.target.startsWith("connection://") ? result.target : target, run, comparison, select, diffHash: result.diffHash }),
    record: (patched: PatchResult, { run, comparison, result }): HistoryRecord => ({
      id: run,
      kind: "patch",
      parent: comparison,
      dialect: patched.dialect,
      reference: result.reference,
      target: result.target,
      schemas: result.schemas,
      status: "succeeded",
      createdAt: new Date().toISOString(),
      summary: { selected: patched.selection.length, acks: patched.acks.length },
      payload: patched,
    }),
  });
  return {
    running: mutation.isPending,
    start: (request: StartPatch) => {
      const run = crypto.randomUUID();
      mutation.mutate({ ...request, run });
      navigate({ stage: "patch", patch: run });
    },
  };
}
