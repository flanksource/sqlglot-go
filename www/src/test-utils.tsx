// Shared by the screen tests: a fetch stub keyed by URL and a render helper with a query client.
import type { ReactNode } from "react";
import { vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { DraftProvider } from "./workflow/drafts";

export type Handler = (init?: RequestInit) => Response | Promise<Response>;

export function stubFetch(handlers: Record<string, Handler>) {
  const fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const handler = handlers[url];
    if (!handler) throw new Error(`unexpected fetch ${url}`);
    return Promise.resolve(handler(init));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

export const json = (body: unknown, status = 200): Handler => () => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

/** The structured error body of the ui server. */
export const failure = (message: string, status = 400): Handler => json({ code: "failed", message, trace: "" }, status);

export function renderWithClient(node: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: 0 } } });
  return render(<QueryClientProvider client={client}><DraftProvider>{node}</DraftProvider></QueryClientProvider>);
}

/** The JSON body of the first request the stub saw for `url`. */
export function bodyOf(fetchMock: ReturnType<typeof stubFetch>, url: string): Record<string, unknown> {
  const call = fetchMock.mock.calls.find(([called]) => called === url);
  if (!call?.[1]?.body) throw new Error(`no request with a body was sent to ${url}`);
  return JSON.parse(call[1].body as string) as Record<string, unknown>;
}
