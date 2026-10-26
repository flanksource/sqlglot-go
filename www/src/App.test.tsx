import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import dashboardFixture from "./fixtures/connections-dashboard.json";
import App from "./App";
import type { ReactNode } from "react";

vi.mock("@flanksource/clicky-ui/monaco", () => ({
  MonacoProvider: ({ children }: { children: ReactNode }) => children,
  MonacoEditor: () => <div>SQL editor</div>,
}));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

function serve() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (url === "/api/v1/connections/dashboard")
        return new Response(JSON.stringify(dashboardFixture));
      if (url === "/api/v1/history") return new Response("[]");
      if (url.startsWith("/api/v1/tasks")) return new Response("[]");
      throw new Error(`unexpected fetch ${url}`);
    }),
  );
}

describe("App shell", () => {
  it("catches shell routing failures and exposes shared error details", async () => {
    serve();
    window.history.replaceState(null, "", "/?page=unknown");
    render(<App />);
    expect(
      await screen.findByRole("heading", { name: "Something went wrong" }),
    ).toBeTruthy();
    const alert = screen.getByRole("alert");
    const details = alert.querySelector("details");
    expect(details?.textContent).toContain("Unknown page: unknown");
    expect(details?.textContent).toContain("React component stack:");
    expect(
      screen.getByRole("button", { name: "Copy error details" }),
    ).toBeTruthy();
  });
  it("navigates between Compare, History and Connections from the nav rail and marks the current page", async () => {
    serve();
    render(<App />);
    const nav = (name: string) =>
      screen
        .getAllByRole("link", { name })
        .find((link) => link.closest("aside"))!;
    // The rail marks the current destination with its accent class.
    const current = (name: string) =>
      nav(name).classList.contains("bg-sidebar-accent");
    expect(current("Compare")).toBe(true);
    expect(current("History")).toBe(false);
    fireEvent.click(nav("History"));
    expect(
      await screen.findByRole("heading", { name: "History" }),
    ).toBeTruthy();
    expect(window.location.search).toBe("?page=history");
    await waitFor(() => expect(current("History")).toBe(true));
    expect(current("Compare")).toBe(false);
    fireEvent.click(nav("Connections"));
    expect(
      await screen.findByRole("heading", { name: "Connections" }),
    ).toBeTruthy();
  });

  it("asks for a comparison when the review URL names no run", async () => {
    serve();
    window.history.replaceState(
      null,
      "",
      "/?cmp.stage=review&cmp.reference=connection%3A%2F%2Fdev&cmp.target=connection%3A%2F%2Fdev-0a",
    );
    render(<App />);
    expect(
      await screen.findByText("Choose a reference and a target"),
    ).toBeTruthy();
  });
});
