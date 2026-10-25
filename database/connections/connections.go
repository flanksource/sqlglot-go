// Package connections resolves named commons-db connections stored as YAML
// files into database DSNs.
package connections

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons/logger"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

// Prefix marks a --dsn value as a named connection reference.
const Prefix = "connection://"

// KubeContextProperty selects the kubeconfig context used to read the
// connection's secret:// and configmap:// references.
const KubeContextProperty = "kubecontext"

var sqlTypes = []string{
	models.ConnectionTypePostgres,
	models.ConnectionTypeMySQL,
	models.ConnectionTypeSQLServer,
	models.ConnectionTypeClickHouse,
	models.ConnectionTypeSQLite,
}

// DefaultDir follows XDG: $XDG_CONFIG_HOME/sqlglot-go/connections, otherwise
// ~/.config/sqlglot-go/connections.
func DefaultDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "sqlglot-go", "connections")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".config", "sqlglot-go", "connections")
	}
	return filepath.Join(home, ".config", "sqlglot-go", "connections")
}

// Load reads every *.yaml and *.yml connection in dir, keyed by name.
func Load(dir string) (map[string]models.Connection, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read connections directory: %w", err)
	}
	catalog := map[string]models.Connection{}
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if entry.IsDir() || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var connection models.Connection
		if err := yaml.UnmarshalStrict(data, &connection); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		stem := strings.TrimSuffix(entry.Name(), ext)
		if connection.Name == "" {
			connection.Name = stem
		}
		if connection.Name != stem {
			return nil, fmt.Errorf("%s: name %q does not match file stem %q", path, connection.Name, stem)
		}
		if _, exists := catalog[connection.Name]; exists {
			return nil, fmt.Errorf("duplicate connection %q in %s", connection.Name, dir)
		}
		if !slices.Contains(sqlTypes, connection.Type) {
			return nil, fmt.Errorf("connection %q has type %q, expected one of %s", connection.Name, connection.Type, strings.Join(sqlTypes, ", "))
		}
		connection.Source = path
		catalog[connection.Name] = connection
	}
	return catalog, nil
}

type ResolveOptions struct {
	Dir       string
	Reference string
	// Kubernetes overrides the client selected by the kubecontext property.
	Kubernetes *dutyKubernetes.Client
}

var (
	usernameTemplate = regexp.MustCompile(`\$\(username\)|\{\{\s*\.username\s*\}\}`)
	passwordTemplate = regexp.MustCompile(`\$\(password\)|\{\{\s*\.password\s*\}\}`)
)

// Resolve hydrates a connection://[<namespace>/]<name> reference and returns
// its templated URL as a DSN.
func Resolve(ctx context.Context, opts ResolveOptions) (string, error) {
	catalog, err := Load(opts.Dir)
	if err != nil {
		return "", err
	}
	connection, err := lookup(catalog, opts.Reference)
	if err != nil {
		return "", err
	}
	if connection.Username != "" && !usernameTemplate.MatchString(connection.URL) {
		return "", fmt.Errorf("connection %q sets username but its url does not reference $(username)", connection.Name)
	}
	if connection.Password != "" && !passwordTemplate.MatchString(connection.URL) {
		return "", fmt.Errorf("connection %q sets password but its url does not reference $(password)", connection.Name)
	}
	dbctx := dbcontext.NewContext(ctx).WithConnectionResolver(func(dbcontext.Context, string) (*models.Connection, error) {
		return connection, nil
	})
	client := opts.Kubernetes
	if kubeContext := connection.Properties[KubeContextProperty]; client == nil && kubeContext != "" {
		if client, err = kubeClient(kubeContext); err != nil {
			return "", fmt.Errorf("connection %q: %w", connection.Name, err)
		}
	}
	if client != nil {
		dbctx = dbctx.WithLocalKubernetes(client)
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

func lookup(catalog map[string]models.Connection, reference string) (*models.Connection, error) {
	path, found := strings.CutPrefix(reference, Prefix)
	if !found {
		return nil, fmt.Errorf("connection reference %q must start with %s", reference, Prefix)
	}
	namespace, name, qualified := strings.Cut(path, "/")
	if !qualified {
		name, namespace = namespace, ""
	}
	connection, exists := catalog[name]
	if !exists {
		return nil, fmt.Errorf("connection %q not found", name)
	}
	if qualified && connection.Namespace != namespace {
		return nil, fmt.Errorf("connection %q is in namespace %q, not %q", name, connection.Namespace, namespace)
	}
	connection.Properties = maps.Clone(connection.Properties)
	return &connection, nil
}

func kubeClient(kubeContext string) (*dutyKubernetes.Client, error) {
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig context %q: %w", kubeContext, err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client for context %q: %w", kubeContext, err)
	}
	return dutyKubernetes.NewKubeClient(logger.GetLogger("k8s"), clientset, config), nil
}
