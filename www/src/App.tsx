import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  AppShell,
  Button,
  ErrorWrapper,
} from "@flanksource/clicky-ui/components";
import { Callout, TaskManagerButton } from "@flanksource/clicky-ui/data";
import {
  DensityProvider,
  ThemeProvider,
  useHistoryRoute,
} from "@flanksource/clicky-ui/hooks";
import { ConnectionsPage } from "@flanksource/clicky-ui/connections";
import {
  UiCode2,
  UiDatabase,
  UiHistory,
  UiPlug,
  UiSearch,
  UiSwap,
} from "@flanksource/clicky-ui/icons";
import { API_BASE } from "./api";
import { MonacoProvider } from "@flanksource/clicky-ui/monaco";
import EditorWorker from "monaco-editor/esm/vs/editor/editor.worker?worker";
import JSONWorker from "monaco-editor/esm/vs/language/json/json.worker?worker";
import { Inspect } from "./workflow/Inspect";
import { Migrations } from "./workflow/Migrations";
import { SQLTools } from "./workflow/SQLTools";
import { DraftProvider } from "./workflow/drafts";
import { connectionRef } from "./compare/model";
import { About } from "./workflow/About";
import { Patch } from "./compare/Patch";
import { Review } from "./compare/Review";
import {
  buildRoute,
  parseRoute,
  type Navigate,
  type Page,
  type Route,
} from "./compare/route";
import { Setup } from "./compare/Setup";
import { History } from "./history/History";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 0, refetchOnWindowFocus: false } },
});

const NAV = [
  { page: "compare", label: "Compare", icon: UiSwap },
  { page: "inspect", label: "Inspect", icon: UiSearch },
  { page: "migrations", label: "Migrations", icon: UiDatabase },
  { page: "sql", label: "SQL Tools", icon: UiCode2 },
  { page: "history", label: "History", icon: UiHistory },
  { page: "connections", label: "Connections", icon: UiPlug },
] as const satisfies readonly { page: Page; label: string; icon: unknown }[];

function ConnectionsScreen({
  route,
  navigate,
}: {
  route: Route;
  navigate: Navigate;
}) {
  return (
    <div className="mx-auto max-w-5xl space-y-4 py-4">
      <h1 className="text-xl font-semibold">Connections</h1>
      <ConnectionsPage
        apiBase={API_BASE}
        renderConnectionActions={(connection) => (
          <a
            className="px-2 text-xs text-primary hover:underline"
            href={buildRoute({
              ...route,
              page: "inspect",
              workflowConnection: connectionRef(connection),
              inspectionRun: "",
              inspectionMode: "fields",
            })}
          >
            Inspect
          </a>
        )}
        {...(route.connection ? { selected: route.connection } : {})}
        onSelect={(id) => navigate({ connection: id ?? "" })}
        connectionHref={(id) =>
          buildRoute({ ...route, page: "connections", connection: id })
        }
      />
    </div>
  );
}

function CompareScreen({
  route,
  navigate,
}: {
  route: Route;
  navigate: Navigate;
}) {
  if (route.stage === "setup")
    return <Setup route={route} navigate={navigate} />;
  if (!route.run) {
    return (
      <Callout variant="warning" title="Choose a reference and a target">
        The {route.stage} screen shows a comparison run, and the URL names none.
        Compare two connections, or open an earlier comparison from the history.{" "}
        <Button
          size="sm"
          variant="outline"
          onClick={() => navigate({ stage: "setup" })}
        >
          Back to setup
        </Button>
      </Callout>
    );
  }
  return route.stage === "review" ? (
    <Review route={route} navigate={navigate} />
  ) : (
    <Patch route={route} navigate={navigate} />
  );
}

function Screen({ route, navigate }: { route: Route; navigate: Navigate }) {
  if (route.page === "inspect")
    return <Inspect route={route} navigate={navigate} />;
  if (route.page === "migrations")
    return <Migrations route={route} navigate={navigate} />;
  if (route.page === "sql")
    return <SQLTools route={route} navigate={navigate} />;
  if (route.page === "connections")
    return <ConnectionsScreen route={route} navigate={navigate} />;
  if (route.page === "history")
    return <History route={route} navigate={navigate} />;
  return <CompareScreen route={route} navigate={navigate} />;
}

function Shell() {
  const [route, navigate] = useHistoryRoute({
    parse: parseRoute,
    build: buildRoute,
  });
  return (
    <AppShell
      brand={
        <span className="flex items-center gap-2 text-sm font-semibold">
          <UiDatabase className="size-4" aria-hidden />
          sqlglot
        </span>
      }
      contentWidth="full"
      contentClassName="px-4 py-3"
      navSections={[
        {
          items: NAV.map(({ page, label, icon }) => ({
            key: page,
            label,
            icon,
            active: route.page === page,
            to: buildRoute({ ...route, page }),
          })),
        },
      ]}
      actions={
        <>
          <About />
          <TaskManagerButton basePath={API_BASE} />
        </>
      }
    >
      <Screen route={route} navigate={navigate} />
    </AppShell>
  );
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <DensityProvider>
          <ErrorWrapper>
            <MonacoProvider
              getWorker={(label) =>
                label === "json" ? new JSONWorker() : new EditorWorker()
              }
            >
              <DraftProvider>
                <Shell />
              </DraftProvider>
            </MonacoProvider>
          </ErrorWrapper>
        </DensityProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
}
