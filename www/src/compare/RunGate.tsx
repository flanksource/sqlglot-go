import { useEffect, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Callout, TaskProgress } from "@flanksource/clicky-ui/data";
import { useTaskRun } from "@flanksource/clicky-ui/hooks";
import { API_BASE, historyGet, isNotFound } from "../api";
import type { HistoryKind, HistoryRecord } from "../types";
import { RunProgress } from "./RunProgress";
import { historyKey, useHistoryRecord, useRunKnown, useRunRequest } from "./queries";

/** `recorded` is true when the run is read from history, so no database was contacted for it. */
export type RunMeta = { createdAt: string; recorded: boolean };

export type RunGateProps<P> = {
  run: string;
  kind: HistoryKind;
  renderFailure?: (record: HistoryRecord) => ReactNode;
  parse: (payload: unknown) => P;
  progressTitle: string;
  /** Buttons beside a failure; `message` lets the caller offer a different way out for a drift. */
  failureActions: (message: string) => ReactNode;
  /** Buttons beside "not found": the run is neither recorded nor running. */
  missingActions: ReactNode;
  children: (payload: P, meta: RunMeta) => ReactNode;
};

const Status = ({ children }: { children: ReactNode }) => <p role="status" className="py-10 text-center text-sm text-muted-foreground">{children}</p>;

function Failure({ run, kind, message, actions, log }: { run: string; kind: string; message: string; actions: ReactNode; log: boolean }) {
  const known = useRunKnown(run, log);
  return (
    <div className="mx-auto max-w-3xl space-y-3 py-4">
      <Callout variant="caution" title={`The ${kind} failed`}>{message}</Callout>
      <div className="flex gap-2">{actions}</div>
      {known.isError && <p className="text-xs text-muted-foreground">The run log is unavailable: {known.error.message}</p>}
      {known.data === true && <RunProgress run={run} title="Run log" />}
    </div>
  );
}

// PendingRun is a run with no record: a request this session is still waiting
// on, or a run another session started. Its task stream shows the progress and
// its end is the cue to read the record.
function PendingRun({ run, kind, requestPending, progressTitle, missing }: { run: string; kind: string; requestPending: boolean; progressTitle: string; missing: ReactNode }) {
  const queryClient = useQueryClient();
  const known = useRunKnown(run, !requestPending);
  const live = requestPending || known.data === true;
  const tasks = useTaskRun({ id: run, basePath: API_BASE, enabled: live });
  const [unrecorded, setUnrecorded] = useState<Error | null>(null);
  // Read outside the record's query: refetching an errored query returns it to
  // pending, which would unmount this screen mid-run.
  useEffect(() => {
    if (!tasks.isComplete) return;
    let current = true;
    historyGet(run).then(
      (found) => { queryClient.setQueryData(historyKey(run), found); },
      (error: Error) => { if (current) setUnrecorded(error); },
    );
    return () => { current = false; };
  }, [tasks.isComplete]);
  if (!requestPending && known.isPending) return <Status>Looking for the {kind}…</Status>;
  if (known.isError) return <Callout variant="caution" title={`The ${kind} could not be looked up`}>{known.error.message}</Callout>;
  if (!live) {
    return (
      <div className="mx-auto max-w-3xl space-y-3 py-4">
        <Callout variant="warning" title={`This ${kind} was not found`}>No run with this id is recorded in the history or running now.</Callout>
        <div className="flex gap-2">{missing}</div>
      </div>
    );
  }
  return (
    <div className="mx-auto max-w-3xl space-y-3 py-4">
      {unrecorded && <Callout variant="caution" title={`The ${kind} finished without a recorded result`}>{unrecorded.message}</Callout>}
      <TaskProgress snapshots={tasks.snapshots} title={progressTitle} compact />
    </div>
  );
}

function Recorded<P>({ record, props, recorded }: { record: HistoryRecord; props: RunGateProps<P>; recorded: boolean }) {
  const { run, kind, parse, failureActions, children } = props;
  if (record.kind !== kind) {
    return <Callout variant="caution" title={`This is not a ${kind}`}>Run {run} is a recorded {record.kind}.</Callout>;
  }
  if (record.status === "failed") {
    if (props.renderFailure) return <>{props.renderFailure(record)}</>;
    const message = record.error ?? `The ${kind} failed without an error message`;
    return <Failure run={run} kind={kind} message={message} actions={failureActions(message)} log />;
  }
  return <>{children(parse(record.payload), { createdAt: record.createdAt, recorded })}</>;
}

// RunGate renders its children once the run has a succeeded record. Until then
// it shows the run's task progress; a failure is shown, never swallowed.
export function RunGate<P>(props: RunGateProps<P>) {
  const { run, kind, progressTitle, failureActions, missingActions } = props;
  const record = useHistoryRecord(run);
  const request = useRunRequest(run);
  if (record.data) return <Recorded record={record.data} props={props} recorded={request?.status !== "success"} />;
  if (record.isPending) return <Status>Loading the {kind}…</Status>;
  const failure = request?.status === "error" ? request.error : record.error && !isNotFound(record.error) ? record.error : null;
  if (failure) return <Failure run={run} kind={kind} message={failure.message} actions={failureActions(failure.message)} log={false} />;
  return <PendingRun run={run} kind={kind} requestPending={request?.status === "pending"} progressTitle={progressTitle} missing={missingActions} />;
}
