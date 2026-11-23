package main

import (
	"context"
	"net/http"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/sqlglot-go/database/migrate/workflow"
	"github.com/spf13/cobra"
)

type migrationSourceInput struct {
	Config string          `json:"config,omitempty" flag:"-"`
	Paths  []string        `json:"paths,omitempty" flag:"-"`
	Files  []workflow.File `json:"files,omitempty" flag:"-"`
}

type migrationSourceResult struct {
	*workflow.ProjectUpload
	*workflow.ProjectPreview
}

func addMigrationSourcesCommand(parent *cobra.Command) {
	cmd := clicky.AddNamedCommandWithContext("sources", parent, migrationSourceInput{}, inspectMigrationSources)
	cmd.Short = "Inspect uploaded project migration sources without connecting to a database"
	entity.MarkServedOnly(cmd)
}

func inspectMigrationSources(ctx context.Context, input migrationSourceInput) (migrationSourceResult, error) {
	request, ok := rpc.RequestFromContext(ctx)
	if !ok {
		return migrationSourceResult{}, entity.NewStatusError(http.StatusBadRequest, "invalid_input", "uploaded project contents are required")
	}
	if err := decodeRequest(request, &input); err != nil {
		return migrationSourceResult{}, err
	}
	if input.Config != "" || input.Paths != nil {
		if input.Files != nil {
			return migrationSourceResult{}, entity.NewStatusError(http.StatusBadRequest, "invalid_input", "send project paths or uploaded files")
		}
		upload, err := workflow.ProjectUploadPaths(input.Config, input.Paths)
		if err != nil {
			return migrationSourceResult{}, entity.NewStatusErrorf(http.StatusBadRequest, "invalid_input", "%v", err)
		}
		return migrationSourceResult{ProjectUpload: &upload}, nil
	}
	preview, err := workflow.PreviewProject(input.Files)
	if err != nil {
		return migrationSourceResult{}, entity.NewStatusErrorf(http.StatusBadRequest, "invalid_input", "%v", err)
	}
	return migrationSourceResult{ProjectPreview: &preview}, nil
}
