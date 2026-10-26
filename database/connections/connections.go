// Package connections resolves named commons-db connections, kept as typed
// Connection resources in the commons-db connection store, into database DSNs.
package connections

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/connection/store"
	dbcontext "github.com/flanksource/commons-db/context"
	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/models"
)

// Prefix marks a --dsn value as a named connection reference.
const Prefix = "connection://"

// App names sqlglot's own connection layer, <config>/sqlglot-go/connections.
const App = "sqlglot-go"

// KubeContextProperty selects the kubeconfig context used to read the
// connection's secret:// and configmap:// references.
const KubeContextProperty = dbcontext.KubeContextProperty

// SQLTypes are the connection types sqlglot can open.
var SQLTypes = []string{
	models.ConnectionTypePostgres,
	models.ConnectionTypeMySQL,
	models.ConnectionTypeSQLServer,
	models.ConnectionTypeClickHouse,
	models.ConnectionTypeSQLite,
}

// DefaultDir is sqlglot's own layer: $XDG_CONFIG_HOME/sqlglot-go/connections,
// otherwise ~/.config/sqlglot-go/connections.
func DefaultDir() string {
	dir, err := store.ConfigDir(App)
	if err != nil {
		panic(fmt.Errorf("connections: %w", err))
	}
	return filepath.Join(dir, "connections")
}

// Open is the connection store: the connections every Flanksource tool shares,
// then dir as sqlglot's own layer (DefaultDir when empty), then ./.connections,
// each overriding the one before.
func Open(dir string) (*store.Store, error) {
	layers, err := store.DefaultLayers(App)
	if err != nil {
		return nil, err
	}
	if dir != "" {
		for i := range layers {
			if layers[i].Name == store.LayerApp {
				layers[i].Dir = dir
			}
		}
	}
	return store.New(layers...)
}

type ResolveOptions struct {
	Dir       string
	Reference string
	// Kubernetes is the client for references of a connection without a
	// kubecontext property; one with that property uses its own context.
	Kubernetes *dutyKubernetes.Client
}

var (
	usernameTemplate = regexp.MustCompile(`\$\(username\)|\{\{\s*\.username\s*\}\}`)
	passwordTemplate = regexp.MustCompile(`\$\(password\)|\{\{\s*\.password\s*\}\}`)
)

// Resolve hydrates a connection://[<namespace>/]<name> reference and returns
// its templated URL as a DSN.
func Resolve(ctx context.Context, opts ResolveOptions) (string, error) {
	if !strings.HasPrefix(opts.Reference, Prefix) {
		return "", fmt.Errorf("connection reference %q must start with %s", opts.Reference, Prefix)
	}
	connections, err := Open(opts.Dir)
	if err != nil {
		return "", err
	}
	dbctx := connections.Attach(dbcontext.NewContext(ctx))
	if opts.Kubernetes != nil {
		dbctx = dbctx.WithLocalKubernetes(opts.Kubernetes)
	}
	connection, err := connections.ResolveConnection(dbctx, opts.Reference)
	if err != nil {
		return "", err
	}
	if connection == nil {
		return "", fmt.Errorf("connection %q not found in the connection store layers %v", strings.TrimPrefix(opts.Reference, Prefix), connections.Layers())
	}
	if len(connection.Warnings) > 0 {
		return "", fmt.Errorf("connection %q cannot be used: %s", connection.Name, strings.Join(connection.Warnings, "; "))
	}
	if !slices.Contains(SQLTypes, connection.Type) {
		return "", fmt.Errorf("connection %q has type %q, expected one of %s", connection.Name, connection.Type, strings.Join(SQLTypes, ", "))
	}
	if connection.Username != "" && !usernameTemplate.MatchString(connection.URL) {
		return "", fmt.Errorf("connection %q sets username but its url does not reference $(username)", connection.Name)
	}
	if connection.Password != "" && !passwordTemplate.MatchString(connection.URL) {
		return "", fmt.Errorf("connection %q sets password but its url does not reference $(password)", connection.Name)
	}
	hydrated, err := dbcontext.HydrateConnectionByURL(dbctx, opts.Reference)
	if err != nil {
		return "", err
	}
	if hydrated.URL == "" {
		return "", fmt.Errorf("connection %q has an empty url", connection.Name)
	}
	return hydrated.URL, nil
}
