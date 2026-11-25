package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	mssql "github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlserver"
	dbtarget "github.com/flanksource/sqlglot-go/database/target"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	"github.com/spf13/cobra"
)

// HCLOutput is what `schema inspect --hcl` produces: the files with their
// content, or, when they were written to a directory, the paths written.
type HCLOutput struct {
	Files   []planner.HCLFile
	Written []string
}

func (o HCLOutput) MarshalJSON() ([]byte, error) {
	type file struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if o.Written != nil {
		return json.Marshal(struct {
			Written []string `json:"written"`
		}{o.Written})
	}
	files := make([]file, len(o.Files))
	for i, f := range o.Files {
		files[i] = file{f.Name, f.Content}
	}
	return json.Marshal(struct {
		Files []file `json:"files"`
	}{files})
}

// print writes the single document to stdout, or lists the files written.
func (o HCLOutput) print(cmd *cobra.Command) error {
	if o.Written != nil {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), strings.Join(o.Written, "\n"))
		return err
	}
	if len(o.Files) != 1 {
		return fmt.Errorf("expected one HCL document on stdout, got %d files", len(o.Files))
	}
	_, err := fmt.Fprint(cmd.OutOrStdout(), o.Files[0].Content)
	return err
}

// hclFlagsWithoutHCL names the first HCL-only flag set on a command that does not write HCL.
func (o InspectOptions) hclFlagsWithoutHCL() string {
	switch {
	case len(o.Tables) > 0:
		return "--table"
	case len(o.Columns) > 0:
		return "--columns"
	case o.Out != "":
		return "--out"
	case o.Document:
		return "--document"
	case o.OmitUnsupported:
		return "--omit-unsupported"
	}
	return ""
}

func exportHCL(ctx context.Context, conn dbtarget.Connection, opts InspectOptions) (InspectionOutput, error) {
	if conn.Dialect != "sqlserver" {
		return InspectionOutput{}, fmt.Errorf("--hcl writes SQL Server HCL; the target is %s", conn.Dialect)
	}
	if opts.Snapshot {
		return InspectionOutput{}, errors.New("--hcl and --snapshot are different outputs: select one")
	}
	if _, served := rpc.RequestFromContext(ctx); served && opts.Out != "" {
		return InspectionOutput{}, entity.NewStatusError(http.StatusBadRequest, "invalid_input", "HTTP requests cannot write server paths: omit out")
	}
	columns, err := parseColumns(opts.Columns)
	if err != nil {
		return InspectionOutput{}, err
	}
	layout := planner.HCLDocument
	if opts.Out != "" && !opts.Document {
		layout = planner.HCLFilePerTable
	}
	files, err := mssql.ExportHCL(ctx, conn.DB, mssql.ExportOptions{
		Schema: conn.Schema, Tables: opts.Tables,
		HCL: planner.HCLOptions{Layout: layout, OmitUnsupported: opts.OmitUnsupported, Columns: columns},
	})
	if err != nil {
		return InspectionOutput{}, err
	}
	if opts.Out == "" {
		return InspectionOutput{Value: HCLOutput{Files: files}}, nil
	}
	written, err := writeHCLFiles(opts.Out, files)
	return InspectionOutput{Value: HCLOutput{Written: written}}, err
}

// parseColumns reads TABLE=COLUMN,COLUMN entries.
func parseColumns(entries []string) (map[string][]string, error) {
	var out map[string][]string
	for _, entry := range entries {
		table, list, ok := strings.Cut(entry, "=")
		columns := strings.Split(list, ",")
		if !ok || table == "" || slices.Contains(columns, "") {
			return nil, fmt.Errorf("--columns %q must be TABLE=COLUMN,COLUMN", entry)
		}
		if out == nil {
			out = map[string][]string{}
		}
		if _, dup := out[table]; dup {
			return nil, fmt.Errorf("--columns names table %q twice", table)
		}
		out[table] = columns
	}
	return out, nil
}

func writeHCLFiles(dir string, files []planner.HCLFile) (written []string, returnErr error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	written = []string{}
	for _, f := range files {
		if err := root.WriteFile(f.Name, []byte(f.Content), 0o600); err != nil {
			return written, err
		}
		written = append(written, filepath.Join(dir, f.Name))
	}
	return written, nil
}
