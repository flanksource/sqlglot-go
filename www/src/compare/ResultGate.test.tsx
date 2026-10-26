import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import compareFixture from "../fixtures/compare.json";
import { COMPARISON_RUN, comparisonRecord, runSnapshots } from "../fixtures/records";
import { bodyOf, failure, json, renderWithClient, stubFetch, type Handler } from "../test-utils";
import type { CompareResult } from "../types";
import { ResultGate } from "./ResultGate";
import { useStartComparison } from "./queries";
import type { Navigate, Route } from "./route";
import type { RunMeta } from "./RunGate";

const route: Route = {
  page: "compare",
  connection: "",
  stage: "review",
  reference: "connection://dev",
  target: "connection://dev-0a",
  schemas: "main",
  selected: null,
  object: "",
  run: COMPARISON_RUN,
  patch: "",
  historyKind: "",
  historyConnection: "",
  historyRecord: "",
};

const RECORD = `/api/v1/history/${COMPARISON_RUN}`;
const TASKS = `/api/v1/tasks/${COMPARISON_RUN}`;
const notFound = failure("run not found", 404);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function Gate({ navigate = vi.fn(), seen = vi.fn() }: { navigate?: Navigate; seen?: (meta: RunMeta) => void }) {
  return (
    <ResultGate route={route} navigate={navigate}>
      {(result: CompareResult, _recompare, meta) => {
        seen(meta);
        return <p>{result.changes.length} changes in {result.dialect}</p>;
      }}
    </ResultGate>
  );
}

// Answers with each handler in turn, then keeps repeating the last.
const sequence = (...handlers: Handler[]): Handler => {
  let calls = 0;
  return (init) => handlers[Math.min(calls++, handlers.length - 1)]!(init);
};

describe("ResultGate", () => {
  it("renders a recorded comparison from the history without starting anything", async () => {
    const fetchMock = stubFetch({ [RECORD]: json(comparisonRecord({ createdAt: "2026-01-15T09:15:00Z" })) });
    const seen = vi.fn();
    renderWithClient(<Gate seen={seen} />);
    expect(await screen.findByText(`${compareFixture.changes.length} changes in sqlite`)).toBeTruthy();
    expect(seen).toHaveBeenCalledWith({ createdAt: "2026-01-15T09:15:00Z", recorded: true });
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([RECORD]);
  });

  it("shows the run's task progress while it is running and has no record yet", async () => {
    stubFetch({ [RECORD]: notFound, [TASKS]: json(runSnapshots(COMPARISON_RUN, "running")) });
    renderWithClient(<Gate />);
    expect(await screen.findByText("Comparing connection://dev with connection://dev-0a…")).toBeTruthy();
    expect(await screen.findByText("Plan all differences")).toBeTruthy();
    expect(screen.queryByText(/changes in/)).toBeNull();
  });

  it("reads the record once the run it was following completes", async () => {
    stubFetch({
      [RECORD]: sequence(notFound, json(comparisonRecord())),
      [TASKS]: json(runSnapshots(COMPARISON_RUN, "success")),
    });
    renderWithClient(<Gate />);
    expect(await screen.findByText(`${compareFixture.changes.length} changes in sqlite`)).toBeTruthy();
  });

  it("says a finished run has no recorded result instead of waiting forever", async () => {
    stubFetch({ [RECORD]: notFound, [TASKS]: json(runSnapshots(COMPARISON_RUN, "success")) });
    renderWithClient(<Gate />);
    expect(await screen.findByText("The comparison finished without a recorded result")).toBeTruthy();
  });

  it("says the comparison was not found when nothing is recorded and nothing is running, and goes back to setup", async () => {
    stubFetch({ [RECORD]: notFound, [TASKS]: notFound });
    const navigate = vi.fn();
    renderWithClient(<Gate navigate={navigate} />);
    expect(await screen.findByText("This comparison was not found")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Back to setup" }));
    expect(navigate).toHaveBeenCalledWith({ stage: "setup" });
  });

  it("shows a failed comparison's error with its run log and offers back to setup and re-compare", async () => {
    stubFetch({
      [RECORD]: json(comparisonRecord({ status: "failed", error: "inspect target: connection refused" })),
      [TASKS]: json(runSnapshots(COMPARISON_RUN, "failed")),
    });
    renderWithClient(<Gate />);
    expect(await screen.findByText("inspect target: connection refused")).toBeTruthy();
    expect(await screen.findByText("Plan all differences")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Back to setup" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Re-compare" })).toBeTruthy();
  });

  it("re-compares a failed comparison under a new run id", async () => {
    const fetchMock = stubFetch({
      [RECORD]: json(comparisonRecord({ status: "failed", error: "inspect target: connection refused" })),
      [TASKS]: notFound,
      "/api/v1/schema/compare": json(compareFixture),
    });
    const navigate = vi.fn();
    renderWithClient(<Gate navigate={navigate} />);
    fireEvent.click(await screen.findByRole("button", { name: "Re-compare" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
    const { run } = navigate.mock.calls[0]![0] as { run: string };
    expect(run).not.toBe(COMPARISON_RUN);
    expect(bodyOf(fetchMock, "/api/v1/schema/compare")).toEqual({ reference: "connection://dev", target: "connection://dev-0a", schemas: ["main"], run });
  });

  it("shows the server's message when the record cannot be read", async () => {
    stubFetch({ [RECORD]: failure("history database is locked", 500) });
    renderWithClient(<Gate />);
    expect(await screen.findByText("history database is locked")).toBeTruthy();
  });

  it("refuses to show a record of another kind as a comparison", async () => {
    stubFetch({ [RECORD]: json(comparisonRecord({ kind: "inspection" })) });
    renderWithClient(<Gate />);
    expect(await screen.findByText("This is not a comparison")).toBeTruthy();
  });
});

describe("a comparison started from this page", () => {
  function Harness({ seen }: { seen: (meta: RunMeta) => void }) {
    const [current, setCurrent] = useState<Route>({ ...route, run: "" });
    const navigate: Navigate = (next) => setCurrent((previous) => ({ ...previous, ...next }));
    const comparison = useStartComparison(navigate);
    return (
      <>
        <button onClick={() => comparison.start({ reference: route.reference, target: route.target })}>Compare</button>
        {current.run && (
          <ResultGate route={current} navigate={navigate}>
            {(result, _recompare, meta) => { seen(meta); return <p>{result.changes.length} changes in {result.dialect}</p>; }}
          </ResultGate>
        )}
      </>
    );
  }

  it("shows the run's progress while the request is pending, then the result it returned, not read back from history", async () => {
    let finish!: () => void;
    const pending = new Promise<Response>((resolve) => { finish = () => resolve(new Response(JSON.stringify(compareFixture))); });
    let run = "";
    const requested: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      requested.push(url);
      if (url === "/api/v1/schema/compare") {
        run = (JSON.parse(init?.body as string) as { run: string }).run;
        return pending;
      }
      // The record and the task log are asked for as soon as the screen shows, before the request has been sent.
      if (url.startsWith("/api/v1/history/")) return new Response(JSON.stringify({ code: "not_found", message: "run not found" }), { status: 404 });
      if (url.startsWith("/api/v1/tasks/")) return new Response(JSON.stringify(runSnapshots(url.split("/").at(-1)!, "running")));
      throw new Error(`unexpected fetch ${url}`);
    }));
    const seen = vi.fn();
    renderWithClient(<Harness seen={seen} />);
    fireEvent.click(screen.getByRole("button", { name: "Compare" }));
    expect(await screen.findByText("Plan all differences")).toBeTruthy();
    finish();
    expect(await screen.findByText(`${compareFixture.changes.length} changes in sqlite`)).toBeTruthy();
    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ recorded: false }));
    expect(requested.filter((url) => url === `/api/v1/history/${run}`)).toHaveLength(1);
  });
});

