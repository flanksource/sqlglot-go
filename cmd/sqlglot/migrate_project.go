package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/sqlglot-go/database/migrate/workflow"
	"github.com/spf13/cobra"
)

type projectDirectoryKey struct{}

type localMigrationProject struct {
	dir    string
	config *workflow.ProjectConfig
}

func readLocalMigrationProject(dir string) (_ *localMigrationProject, returnErr error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	config, err := workflow.ReadProjectConfig(root.FS())
	if err != nil {
		return nil, fmt.Errorf("project %s: %w", dir, err)
	}
	if config == nil {
		return nil, nil
	}
	return &localMigrationProject{dir: dir, config: config}, nil
}

func discoverMigrationProject(start string) (*localMigrationProject, error) {
	if start == "" {
		return nil, nil
	}
	project, err := readLocalMigrationProject(start)
	if err != nil || project != nil {
		return project, err
	}
	for dir := start; ; dir = filepath.Dir(dir) {
		_, err := os.Stat(filepath.Join(dir, ".git"))
		if err == nil {
			if dir == start {
				return nil, nil
			}
			return readLocalMigrationProject(dir)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if dir == filepath.Dir(dir) {
			return nil, nil
		}
	}
}

func (project *localMigrationProject) sources() (_ *workflow.Project, returnErr error) {
	root, err := os.OpenRoot(project.dir)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	return project.config.Load(root.FS())
}

func (project *localMigrationProject) resolveAlias(opts *MigrationOptions) error {
	if opts.Target == "" {
		return nil
	}
	if project == nil {
		return fmt.Errorf("target %q requires %s in the current directory or Git root", opts.Target, workflow.ProjectConfigName)
	}
	dsn, exists := project.config.Connections[opts.Target]
	if !exists {
		return fmt.Errorf("target %q is not defined in %s", opts.Target, filepath.Join(project.dir, workflow.ProjectConfigName))
	}
	opts.DSN = projectTargetDSN(project.dir, dsn)
	return nil
}

func projectTargetDSN(dir, dsn string) string {
	name, query, hasQuery := strings.Cut(dsn, "?")
	prefix := ""
	if strings.HasPrefix(strings.ToLower(name), "sqlite://") {
		prefix, name = name[:len("sqlite://")], name[len("sqlite://"):]
	} else if !strings.EqualFold(filepath.Ext(name), ".db") || strings.Contains(name, "://") || strings.ContainsAny(name, "=;") {
		return dsn
	}
	if name == "" || name == ":memory:" || filepath.IsAbs(name) || strings.HasPrefix(strings.ToLower(name), "file:") {
		return dsn
	}
	resolved := prefix + filepath.Join(dir, name)
	if hasQuery {
		resolved += "?" + query
	}
	return resolved
}

func explicitMigrationDirectory(opts MigrationOptions) (string, error) {
	if opts.Bundle != "" || len(opts.Paths) != 1 {
		return opts.Bundle, nil
	}
	info, err := os.Stat(opts.Paths[0])
	if err == nil && info.IsDir() {
		return opts.Paths[0], nil
	}
	return "", err
}

func loadCLIMigrationProject(opts *MigrationOptions, role pathRole) error {
	var project *localMigrationProject
	var err error
	var explicit string
	if role == pathsAreSources {
		explicit, err = explicitMigrationDirectory(*opts)
		if err != nil {
			return err
		}
	}
	if explicit != "" {
		project, err = readLocalMigrationProject(explicit)
		if err != nil {
			return err
		}
	}
	implicit := role == pathsAreSources && explicit == "" && opts.Desired == "" && opts.CSVRoot == "" && len(opts.Paths) == 0
	mapped := project != nil || implicit
	if project == nil && (implicit || opts.Target != "") {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		project, err = discoverMigrationProject(cwd)
		if err != nil {
			return err
		}
	}
	if err := project.resolveAlias(opts); err != nil {
		return err
	}
	if project != nil && mapped {
		opts.project, err = project.sources()
	}
	return err
}

func loadHTTPMigrationProject(opts *MigrationOptions, role pathRole, request *http.Request) error {
	useMappings := opts.Source.Kind == "project"
	if !useMappings && opts.Target == "" {
		return nil
	}
	if useMappings && (role != pathsAreSources || len(opts.Source.Files) > 0 || opts.Source.Content != "" || len(opts.ArtifactContent) > 0) {
		return entity.NewStatusError(http.StatusBadRequest, "invalid_input", "a local project is a plan source and does not accept uploaded contents")
	}
	start, _ := request.Context().Value(projectDirectoryKey{}).(string)
	project, err := discoverMigrationProject(start)
	if err == nil {
		err = project.resolveAlias(opts)
	}
	if err == nil && useMappings {
		if project == nil || len(project.config.Mappings) == 0 {
			err = fmt.Errorf("no local %s with migration mappings was found", workflow.ProjectConfigName)
		} else {
			opts.project, err = project.sources()
			opts.Source = workflow.Source{}
		}
	}
	if err != nil {
		return entity.NewStatusErrorf(http.StatusBadRequest, "invalid_project", "%v", err)
	}
	return nil
}

type migrationProjectInfo struct {
	Config  string                   `json:"config,omitempty"`
	Targets []string                 `json:"targets,omitempty"`
	Preview *workflow.ProjectPreview `json:"preview,omitempty"`
	Error   string                   `json:"error,omitempty"`
}

func addMigrationProjectCommand(parent *cobra.Command) {
	cmd := clicky.AddNamedCommandWithContext("project", parent, struct{}{}, inspectMigrationProject)
	cmd.Short = "Preview the local migration project and list its target aliases"
	entity.MarkServedOnly(cmd)
}

func inspectMigrationProject(ctx context.Context, input struct{}) (migrationProjectInfo, error) {
	var info migrationProjectInfo
	request, ok := rpc.RequestFromContext(ctx)
	if !ok {
		return info, entity.NewStatusError(http.StatusBadRequest, "invalid_input", "the local project is available through the UI")
	}
	if err := decodeRequest(request, &input); err != nil {
		return info, err
	}
	start, _ := request.Context().Value(projectDirectoryKey{}).(string)
	project, err := discoverMigrationProject(start)
	if err != nil {
		return info, entity.NewStatusErrorf(http.StatusBadRequest, "invalid_project", "%v", err)
	}
	if project == nil {
		return info, nil
	}
	info.Config = filepath.Join(project.dir, workflow.ProjectConfigName)
	info.Targets = slices.Sorted(maps.Keys(project.config.Connections))
	sources, err := project.sources()
	if err != nil {
		info.Error = err.Error()
	} else if sources != nil {
		preview := sources.Preview()
		info.Preview = &preview
	}
	return info, nil
}
