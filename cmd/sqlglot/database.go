package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/flanksource/clicky"
	_ "github.com/go-sql-driver/mysql"

	"github.com/flanksource/sqlglot-go/database/connections"
	"github.com/spf13/cobra"
)

func addDatabaseCommands(root *cobra.Command) {
	schemaCmd := &cobra.Command{Use: "schema", Short: "Inspect database schemas"}
	addInspectCommand(schemaCmd)
	addCompareCommands(schemaCmd)
	migrateCmd := &cobra.Command{Use: "migrate", Short: "Plan and apply reviewed database migrations"}
	migrateCmd.AddCommand(newMigrationPlanCommand(), newMigrationApplyCommand(), newMigrationReviewCommand())
	root.AddCommand(schemaCmd, migrateCmd)
	connectionsCmd := &cobra.Command{Use: "connections", Short: "Manage named database connections"}
	list := clicky.AddNamedCommand("list", connectionsCmd, ConnectionListOptions{}, listConnections)
	list.Short = "List named connections without resolving their secrets"
	root.AddCommand(connectionsCmd)
}

type ConnectionListOptions struct {
	Dir string `json:"dir" flag:"connections-dir" help:"sqlglot's own connection layer (defaults to $XDG_CONFIG_HOME/sqlglot-go/connections)"`
}

type ConnectionSummary struct {
	Name        string   `json:"name"`
	Namespace   string   `json:"namespace,omitempty"`
	Type        string   `json:"type"`
	KubeContext string   `json:"kubecontext,omitempty"`
	Layer       string   `json:"layer"`
	Source      string   `json:"source"`
	Warnings    []string `json:"warnings,omitempty"`
}

func listConnections(opts ConnectionListOptions) ([]ConnectionSummary, error) {
	catalog, err := connections.Open(opts.Dir)
	if err != nil {
		return nil, err
	}
	entries, err := catalog.Entries()
	if err != nil {
		return nil, err
	}
	result := make([]ConnectionSummary, 0, len(entries))
	for _, e := range entries {
		c := e.Connection
		result = append(result, ConnectionSummary{Name: c.Name, Namespace: c.Namespace, Type: c.Type, KubeContext: c.Properties[connections.KubeContextProperty], Layer: e.Layer.Name, Source: e.Path, Warnings: c.Warnings})
	}
	return result, nil
}

func writeDatabaseJSON(cmd *cobra.Command, path string, value any) (returnErr error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path != "" {
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
		return root.WriteFile(filepath.Base(path), data, 0600)
	}
	_, err = cmd.OutOrStdout().Write(data)
	return err
}
