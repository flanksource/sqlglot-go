package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"io"
	"os"
	"strings"
	"time"

	"github.com/flanksource/sqlglot-go/database/inspect/snapshot"
	bundle "github.com/flanksource/sqlglot-go/database/migrate"
	"github.com/flanksource/sqlglot-go/database/migrate/review"
	executor "github.com/flanksource/sqlglot-go/database/migrate/sqlserver"
	native "github.com/flanksource/sqlglot-go/migrate"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	"github.com/spf13/cobra"
)

type migrationFlags struct {
	databaseFlags
	bundle      string
	desired     string
	output      string
	artifact    string
	hash        string
	allowDrops  bool
	ack         []string
	lockTimeout time.Duration
	routines    string
	variables   []string
}

func newMigrationPlanCommand() *cobra.Command {
	f := &migrationFlags{}
	cmd := &cobra.Command{Use: "plan", Short: "Generate a reviewed artifact without changing the database", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) (returnErr error) {
		if (f.bundle == "") == (f.desired == "") {
			return fmt.Errorf("select exactly one of --bundle or --desired")
		}
		if err := f.validateBundleOptions(); err != nil {
			return err
		}
		db, err := f.open(cmd.Context(), true)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, db.Close()) }()
		result, planErr := f.plan(cmd, db)
		if result != nil {
			if err := writeDatabaseJSON(cmd, f.output, result); err != nil {
				return err
			}
		}
		return planErr
	}}
	f.bind(cmd)
	cmd.Flags().StringVar(&f.bundle, "bundle", "", "Directory containing HCL and SQL migration files")
	cmd.Flags().StringVar(&f.desired, "desired", "", "Desired migration snapshot JSON or backend HCL file")
	cmd.Flags().StringVar(&f.output, "out", "", "Write the artifact JSON to this file")
	cmd.Flags().BoolVar(&f.allowDrops, "allow-drops", false, "Include omitted tables and columns as removals in a bundle plan")
	cmd.Flags().StringVar(&f.routines, "routines", "", "SQL Server routine directory within the bundle")
	cmd.Flags().StringArrayVar(&f.variables, "var", nil, "Bundle variable as name=HCL-literal (repeatable)")
	return cmd
}

func (f *migrationFlags) plan(cmd *cobra.Command, db *sql.DB) (any, error) {
	if f.bundle != "" {
		opts := []bundle.Option{}
		variables, err := parseVariables(f.variables)
		if err != nil {
			return nil, err
		}
		opts = append(opts, bundle.WithVariables(variables))
		if f.schema != "main" {
			opts = append(opts, bundle.WithSchema(f.schema))
		}
		if f.allowDrops {
			opts = append(opts, bundle.WithDrops())
		}
		if f.routines != "" {
			opts = append(opts, bundle.WithRoutines(f.routines))
		}
		preview, err := bundle.PreviewBundle(cmd.Context(), db, f.dialect, os.DirFS(f.bundle), opts...)
		for _, warning := range preview.Warnings {
			if _, writeErr := fmt.Fprintln(cmd.ErrOrStderr(), "warning:", warning); writeErr != nil {
				return nil, writeErr
			}
		}
		return preview, err
	}
	data, err := os.ReadFile(f.desired)
	if err != nil {
		return nil, err
	}
	if f.dialect == "sqlserver" {
		var desired snapshot.Result
		if err := decodeJSON(data, &desired); err != nil {
			return nil, fmt.Errorf("SQL Server requires a complete snapshot JSON: %w", err)
		}
		current, err := snapshot.Inspect(cmd.Context(), db, snapshot.Options{Schemas: []string{f.schema}})
		if err != nil {
			return nil, err
		}
		return planner.BuildArtifact(native.ArtifactRequest{Schemas: current.Schemas, Request: native.Request{Current: current.Schema, Desired: desired.Schema}})
	}
	text := string(data)
	if json.Valid(data) {
		var desired review.Snapshot
		if err := decodeJSON(data, &desired); err != nil {
			return nil, err
		}
		if desired.Dialect != f.dialect || desired.Schema != f.schema {
			return nil, fmt.Errorf("desired snapshot dialect or schema does not match target")
		}
		text = desired.HCL
	}
	return review.Build(cmd.Context(), db, review.Options{Dialect: f.dialect, Schema: f.schema, DesiredHCL: text})
}

func newMigrationApplyCommand() *cobra.Command {
	f := &migrationFlags{}
	cmd := &cobra.Command{Use: "apply", Short: "Apply exact reviewed SQL with hash and destructive acknowledgements", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) (returnErr error) {
		if f.hash == "" {
			return fmt.Errorf("reviewed --hash is required")
		}
		if (f.artifact == "") == (f.bundle == "") {
			return fmt.Errorf("select exactly one of --artifact or --bundle")
		}
		if err := f.validateBundleOptions(); err != nil {
			return err
		}
		var data []byte
		var err error
		if f.artifact != "" {
			data, err = os.ReadFile(f.artifact)
			if err != nil {
				return err
			}
			if err := f.validateArtifact(data); err != nil {
				return err
			}
		}
		db, err := f.open(cmd.Context(), false)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, db.Close()) }()
		if f.bundle != "" {
			planned, err := f.plan(cmd, db)
			if err != nil {
				return err
			}
			data, err = json.Marshal(planned)
			if err != nil {
				return err
			}
		}
		result, err := f.apply(cmd, db, data)
		if result != nil {
			if outputErr := writeDatabaseJSON(cmd, "", result); outputErr != nil {
				return outputErr
			}
		}
		return err
	}}
	f.bind(cmd)
	cmd.Flags().StringVar(&f.artifact, "artifact", "", "Saved reviewed artifact or bundle preview JSON")
	cmd.Flags().StringVar(&f.bundle, "bundle", "", "Replan this bundle and require its hash to match the reviewed hash")
	cmd.Flags().StringVar(&f.hash, "hash", "", "Explicitly reviewed artifact hash")
	cmd.Flags().StringSliceVar(&f.ack, "ack", nil, "Acknowledge individual destructive step IDs (repeat or comma separate)")
	cmd.Flags().BoolVar(&f.allowDrops, "allow-drops", false, "Include omitted objects when replanning a bundle")
	cmd.Flags().DurationVar(&f.lockTimeout, "lock-timeout", 5*time.Second, "Maximum migration lock wait")
	cmd.Flags().StringVar(&f.routines, "routines", "", "SQL Server routine directory within the bundle")
	cmd.Flags().StringArrayVar(&f.variables, "var", nil, "Bundle variable as name=HCL-literal (repeatable)")
	return cmd
}

func (f *migrationFlags) apply(cmd *cobra.Command, db *sql.DB, data []byte) (any, error) {
	var envelope struct {
		Artifact          json.RawMessage `json:"artifact"`
		SQLServerArtifact json.RawMessage `json:"sqlServerArtifact"`
		Version           int             `json:"version"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.Artifact != nil {
		data = envelope.Artifact
	}
	if envelope.SQLServerArtifact != nil {
		data = envelope.SQLServerArtifact
	}
	if f.dialect == "sqlserver" {
		var artifact native.Artifact
		if err := decodeJSON(data, &artifact); err != nil {
			return nil, err
		}
		return executor.Apply(cmd.Context(), db, executor.Request{Artifact: artifact, Hash: f.hash, Acknowledgements: f.ack, LockTimeout: f.lockTimeout})
	}
	var artifact review.Artifact
	if err := decodeJSON(data, &artifact); err != nil {
		return nil, err
	}
	if artifact.Baseline.Dialect != f.dialect || artifact.Baseline.Schema != f.schema {
		return nil, fmt.Errorf("artifact dialect or schema does not match target")
	}
	return review.Apply(cmd.Context(), db, review.Request{Artifact: artifact, Hash: f.hash, Acknowledgements: f.ack, LockTimeout: f.lockTimeout})
}

func decodeJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("artifact must contain exactly one JSON value")
	}
	return nil
}

func (f *migrationFlags) validateArtifact(data []byte) error {
	var envelope struct {
		Artifact          json.RawMessage `json:"artifact"`
		SQLServerArtifact json.RawMessage `json:"sqlServerArtifact"`
		Version           int             `json:"version"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Artifact != nil {
		return f.validateArtifact(envelope.Artifact)
	}
	if envelope.SQLServerArtifact != nil {
		return f.validateArtifact(envelope.SQLServerArtifact)
	}
	if envelope.Version == 1 {
		var a native.Artifact
		if err := decodeJSON(data, &a); err != nil {
			return err
		}
		return executor.Validate(executor.Request{Artifact: a, Hash: f.hash, Acknowledgements: f.ack, LockTimeout: f.lockTimeout})
	}
	var a review.Artifact
	if err := decodeJSON(data, &a); err != nil {
		return err
	}
	return review.Validate(review.Request{Artifact: a, Hash: f.hash, Acknowledgements: f.ack, LockTimeout: f.lockTimeout})
}

func parseVariables(values []string) (map[string]cty.Value, error) {
	result := map[string]cty.Value{}
	for _, value := range values {
		name, text, found := strings.Cut(value, "=")
		if !found || !hclsyntax.ValidIdentifier(name) {
			return nil, fmt.Errorf("variable must be name=HCL-literal")
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("duplicate variable %q", name)
		}
		expression, diagnostics := hclsyntax.ParseExpression([]byte(text), "variable "+name, hcl.Pos{Line: 1, Column: 1})
		if diagnostics.HasErrors() {
			return nil, fmt.Errorf("parse variable %s: %s", name, diagnostics.Error())
		}
		parsed, diagnostics := expression.Value(nil)
		if diagnostics.HasErrors() {
			return nil, fmt.Errorf("evaluate variable %s: %s", name, diagnostics.Error())
		}
		result[name] = parsed
	}
	return result, nil
}

func (f *migrationFlags) validateBundleOptions() error {
	if f.bundle == "" && (len(f.variables) > 0 || f.routines != "" || f.allowDrops) {
		return fmt.Errorf("--var, --routines and --allow-drops require --bundle")
	}
	return nil
}
