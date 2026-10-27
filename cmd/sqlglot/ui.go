package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/route"
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/clicky/task"
	queryconnections "github.com/flanksource/commons-db/cmd/query/connections"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/secretcatalog"
	"github.com/flanksource/commons-db/connection/store"
	dbcontext "github.com/flanksource/commons-db/context"
	dbquery "github.com/flanksource/commons-db/query"
	"github.com/flanksource/sqlglot-go/database/connections"
	"github.com/flanksource/sqlglot-go/database/history"
	"github.com/flanksource/sqlglot-go/www"
	"github.com/spf13/cobra"
)

const apiPrefix = "/api/v1"

// The connection service registers its `connection` entity in clicky's
// process-wide registry, so it is created once; each handler rebinds it to the
// connection store that handler serves.
var (
	uiConnectionsOnce sync.Once
	uiConnections     *queryconnections.Service
	uiConnectionsErr  error
)

// uiConnectionPolicy offers only the types sqlglot opens, and keeps secret
// values out of connection files: credentials are references, while a URL may
// be a literal (a SQLite path, or a template over $(username)/$(password)).
var uiConnectionPolicy = queryconnections.Policy{
	AllowedTypes:  connections.SQLTypes,
	SecretSources: []string{queryconnections.SourceSecret, queryconnections.SourceConfigMap, queryconnections.SourceHelm},
	URLSources:    []string{queryconnections.SourceSecret, queryconnections.SourceConfigMap, queryconnections.SourceHelm, queryconnections.SourceValue},
}

// uiConnectionProvider serves the connection store with dir as sqlglot's own,
// writable layer.
func uiConnectionProvider(dir string) (*queryconnections.StoreProvider, error) {
	catalog, err := connections.Open(dir)
	if err != nil {
		return nil, err
	}
	return queryconnections.NewStoreProvider(queryconnections.StoreProviderOptions{Store: catalog, WriteLayer: store.LayerApp})
}

func bindUIConnections(provider *queryconnections.StoreProvider) (*queryconnections.Service, error) {
	uiConnectionsOnce.Do(func() {
		uiConnections, uiConnectionsErr = queryconnections.New(queryconnections.Options{
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: profiles.DecodeRequestBody,
			// sqlglot has no profile store, so no connection counts profile uses.
			Profiles: func(context.Context) ([]dbquery.Profile, error) { return []dbquery.Profile{}, nil },
			Virtual:  provider,
			Policy:   uiConnectionPolicy,
			Secrets:  &secretcatalog.Options{},
		})
		if uiConnectionsErr == nil {
			uiConnections.RegisterClicky()
		}
	})
	if uiConnectionsErr != nil {
		return nil, uiConnectionsErr
	}
	uiConnections.SetVirtual(provider)
	return uiConnections, nil
}

type uiOptions struct {
	addr           string
	listenAny      bool
	connectionsDir string
	dev            bool
	wwwDir         string
}

// addUICommand registers the connection entity's commands and `sqlglot ui`,
// which serves every clicky operation of root under /api/v1.
func addUICommand(root *cobra.Command) {
	provider, err := uiConnectionProvider(connections.DefaultDir())
	if err == nil {
		_, err = bindUIConnections(provider)
	}
	if err != nil {
		panic(fmt.Errorf("create the connection service: %w", err))
	}
	clicky.GenerateCLI(root)
	opts := &uiOptions{}
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Serve the web UI for comparing and patching database schemas",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serveUI(cmd, root, *opts)
		},
	}
	cmd.Flags().StringVar(&opts.addr, "addr", "127.0.0.1:9150", "Address to listen on; must be loopback unless --listen-any")
	cmd.Flags().BoolVar(&opts.listenAny, "listen-any", false, "Allow a non-loopback --addr, exposing connection management and credentials to the network")
	cmd.Flags().StringVar(&opts.connectionsDir, "connections-dir", connections.DefaultDir(), "sqlglot's own connection layer, where the UI saves connections")
	cmd.Flags().BoolVar(&opts.dev, "dev", false, "Serve the UI from a Vite dev server over www/ (local clicky-ui source, on a random port) that proxies /api here")
	cmd.Flags().StringVar(&opts.wwwDir, "www", "www", "Frontend directory Vite serves with --dev")
	clicky.MarkLocalOnly(cmd)
	root.AddCommand(cmd)
}

func serveUI(cmd *cobra.Command, root *cobra.Command, opts uiOptions) error {
	if err := checkUIAddr(opts.addr, opts.listenAny); err != nil {
		return err
	}
	handlerOpts := uiHandlerOptions{Root: root, ConnectionsDir: opts.connectionsDir, ListenAny: opts.listenAny}
	var devPort int
	var devURL *url.URL
	if opts.dev {
		if _, err := os.Stat(filepath.Join(opts.wwwDir, "package.json")); err != nil {
			return fmt.Errorf("--dev needs the frontend directory: %w", err)
		}
		port, err := freeLoopbackPort()
		if err != nil {
			return fmt.Errorf("pick a port for vite: %w", err)
		}
		devPort = port
		devURL = &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(devPort))}
		handlerOpts.UI = httputil.NewSingleHostReverseProxy(devURL)
		handlerOpts.AllowedOrigins = []string{devURL.Host, fmt.Sprintf("localhost:%d", devPort)}
	} else {
		spa, err := www.Handler()
		if err != nil {
			return err
		}
		handlerOpts.UI = spa
	}
	if err := os.MkdirAll(opts.connectionsDir, 0o700); err != nil {
		return fmt.Errorf("create connections directory: %w", err)
	}
	handler, err := newUIHandler(handlerOpts)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", opts.addr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	shutdownResult := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownResult <- server.Shutdown(shutdown)
	}()
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "sqlglot UI API listening on http://%s (connections: %s)\n", listener.Addr(), opts.connectionsDir); err != nil {
		return err
	}
	viteResult := make(chan error, 1)
	if opts.dev {
		vite := exec.CommandContext(ctx, "pnpm", "dev")
		vite.Dir = opts.wwwDir
		vite.Env = append(os.Environ(), "SQLGLOT_API_URL=http://"+listener.Addr().String(), "SQLGLOT_DEV_PORT="+strconv.Itoa(devPort))
		vite.Stdout, vite.Stderr = cmd.ErrOrStderr(), cmd.ErrOrStderr()
		if err := vite.Start(); err != nil {
			return errors.Join(fmt.Errorf("start vite: %w", err), listener.Close())
		}
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "sqlglot UI (dev, local clicky-ui) on http://%s, proxying Vite at %s\n", listener.Addr(), devURL); err != nil {
			return err
		}
		go func() {
			err := vite.Wait()
			if ctx.Err() == nil {
				err = fmt.Errorf("vite dev server exited: %w", errors.Join(err, errors.New("stopped unexpectedly")))
				stop()
			} else {
				err = nil
			}
			viteResult <- err
		}()
	} else {
		viteResult <- nil
	}
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return errors.Join(<-viteResult, <-shutdownResult)
}

// freeLoopbackPort asks the OS for an unused loopback port. Vite binds it with
// strictPort, so losing it to another process before then fails loudly.
func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return port, listener.Close()
}

func checkUIAddr(addr string, listenAny bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	if listenAny || isLoopbackHost(host) {
		return nil
	}
	return fmt.Errorf("--addr %q is not a loopback address; pass --listen-any to serve it", addr)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type uiHandlerOptions struct {
	Root           *cobra.Command
	ConnectionsDir string
	UI             http.Handler
	ListenAny      bool
	// AllowedOrigins are extra Origin hosts the API accepts: the Vite dev
	// server, whose proxy forwards the page's own origin.
	AllowedOrigins []string
}

func newUIHandler(opts uiHandlerOptions) (http.Handler, error) {
	if opts.UI == nil {
		return nil, errors.New("ui handler is required")
	}
	provider, err := uiConnectionProvider(opts.ConnectionsDir)
	if err != nil {
		return nil, err
	}
	service, err := bindUIConnections(provider)
	if err != nil {
		return nil, err
	}
	compareConnectionsDir = opts.ConnectionsDir
	// The ui serves every run its commands record, and stores clicky's task
	// runs beside them, so a run's progress outlives the process.
	runs, err := history.Open(historyPath)
	if err != nil {
		return nil, err
	}
	task.SetStore(context.Background(), runs)
	server := rpc.NewSwaggerServer(&rpc.ServeConfig{
		Title: "sqlglot", Version: version, StructuredErrorResponses: true,
		Executor: &rpc.ExecutorConfig{Enabled: true, SkipPreRun: true, PathPrefix: apiPrefix},
	}, opts.Root, &rpc.OpenAPIConfig{Title: "sqlglot", Version: version,
		Extensions: []func(*rpc.OpenAPISpec){queryconnections.AddConnectionsOpenAPI, addContentOpenAPI}})
	if server.Executor() == nil {
		return nil, errors.New("register sqlglot clicky operations: executor is unavailable")
	}
	mux := http.NewServeMux()
	router := route.NewRouter(mux)
	server.RegisterRoutes(router)
	task.RegisterHandlersWithSource(router, apiPrefix, runs)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		opts.UI.ServeHTTP(w, r)
	}))
	return guardUIRequests(service.Handler(apiPrefix, mux), opts), nil
}

// guardUIRequests refuses API requests from other origins, so a page in the
// browser cannot drive the API (or read connections) through the user's
// session, and, unless listenAny, from non-loopback peers or Host names, which
// also defeats DNS rebinding.
func guardUIRequests(next http.Handler, opts uiHandlerOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if reason := refuseUIRequest(r, opts); reason != "" {
				http.Error(w, reason, http.StatusForbidden)
				return
			}
			if r.ContentLength > 16*1024*1024 {
				http.Error(w, "request exceeds 16 MiB", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 16*1024*1024)
		}
		next.ServeHTTP(w, r)
	})
}

func refuseUIRequest(r *http.Request, opts uiHandlerOptions) string {
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		allowed := err == nil && (strings.EqualFold(parsed.Host, r.Host) || slices.ContainsFunc(opts.AllowedOrigins, func(host string) bool { return strings.EqualFold(parsed.Host, host) }))
		if !allowed {
			return fmt.Sprintf("cross-origin request from %q refused", origin)
		}
	}
	if opts.ListenAny {
		return ""
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !isLoopbackHost(peer) {
		return fmt.Sprintf("request from non-loopback peer %q refused", r.RemoteAddr)
	}
	host := r.Host
	if name, _, err := net.SplitHostPort(r.Host); err == nil {
		host = name
	}
	if !isLoopbackHost(host) {
		return fmt.Sprintf("request for non-loopback host %q refused", r.Host)
	}
	return ""
}
