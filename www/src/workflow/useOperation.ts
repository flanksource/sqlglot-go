import { useMutation, useQueryClient } from "@tanstack/react-query";
import { post } from "../api";
import { historyKey } from "../compare/queries";

export function useOperation(path: string, onRun: (run: string) => void) {
  const client = useQueryClient();
  const mutation = useMutation({
    mutationKey: ["run"],
    mutationFn: ({
      run,
      input,
    }: {
      run: string;
      input: Record<string, unknown>;
    }) => post(path, { ...input, run }),
    onSettled: async (_data, _error, { run }) => {
      await client.invalidateQueries({ queryKey: historyKey(run) });
      await client.invalidateQueries({ queryKey: ["history", "list"] });
    },
    retry: false,
  });
  return {
    busy: mutation.isPending,
    start: (input: Record<string, unknown>) => {
      const run = crypto.randomUUID();
      mutation.mutate({ run, input });
      onRun(run);
    },
  };
}
