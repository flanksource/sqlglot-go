package workflow

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"gopkg.in/yaml.v3"
)

const ProjectConfigName = ".sqlglot-go.yaml"

type Project struct {
	files   []projectFile
	ignored int
}

type ProjectSource struct {
	Path   string              `json:"path"`
	Kind   ContentKind         `json:"kind"`
	ID     string              `json:"id"`
	Name   string              `json:"name,omitempty"`
	Phase  string              `json:"phase,omitempty"`
	Labels map[string][]string `json:"labels,omitempty"`
}

type projectFile struct {
	ProjectSource
	bundlePath string
	content    string
}

type ProjectConfig struct {
	Version     int               `yaml:"version"`
	Mappings    []projectMapping  `yaml:"mappings"`
	Ignore      []string          `yaml:"ignore"`
	Connections map[string]string `yaml:"connections" json:"-"`
}

type projectMapping struct {
	Match     string              `yaml:"match"`
	Kind      ContentKind         `yaml:"kind"`
	Labels    map[string][]string `yaml:"labels"`
	Transform string              `yaml:"transform"`
}

// LoadProject resolves the configured project sources. A nil project means the
// filesystem has no source mappings.
func LoadProject(fsys fs.FS) (*Project, error) {
	config, err := ReadProjectConfig(fsys)
	if err != nil || config == nil {
		return nil, err
	}
	return config.Load(fsys)
}

// ReadProjectConfig reads a YAML config without opening migration files or targets.
func ReadProjectConfig(fsys fs.FS) (*ProjectConfig, error) {
	data, err := fs.ReadFile(fsys, ProjectConfigName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ProjectConfigName, err)
	}
	config, err := parseProjectConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ProjectConfigName, err)
	}
	return &config, nil
}

var projectAliasName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func parseProjectConfig(data []byte) (ProjectConfig, error) {
	var cfg ProjectConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("expected a single YAML document")
	}
	if cfg.Version != 1 || (len(cfg.Mappings) == 0 && len(cfg.Connections) == 0) {
		return cfg, fmt.Errorf("version 1 and at least one mapping or connection are required")
	}
	for alias, dsn := range cfg.Connections {
		if !projectAliasName.MatchString(alias) || strings.TrimSpace(dsn) == "" {
			return cfg, fmt.Errorf("connection %q requires a target alias and a nonempty DSN or connection reference", alias)
		}
		cfg.Connections[alias] = strings.TrimSpace(dsn)
	}
	for _, mapping := range cfg.Mappings {
		if !validProjectGlob(mapping.Match) {
			return cfg, fmt.Errorf("invalid match glob %q", mapping.Match)
		}
		if mapping.Kind != "" && mapping.Kind != SQLContent && mapping.Kind != HCLContent {
			return cfg, fmt.Errorf("mapping %q: unknown kind %q", mapping.Match, mapping.Kind)
		}
		if err := validateProjectLabels(mapping.Labels); err != nil {
			return cfg, fmt.Errorf("mapping %q: %w", mapping.Match, err)
		}
		for key, values := range mapping.Labels {
			if len(values) == 0 {
				delete(mapping.Labels, key)
			}
		}
	}
	for _, pattern := range cfg.Ignore {
		if !validProjectGlob(pattern) {
			return cfg, fmt.Errorf("invalid ignore glob %q", pattern)
		}
	}
	return cfg, nil
}

func validProjectPath(name string) bool {
	return fs.ValidPath(name) && name != "." && !strings.ContainsAny(name, "\\:\x00")
}

func validProjectGlob(pattern string) bool {
	return validProjectPath(pattern) && doublestar.ValidatePattern(pattern)
}

func (cfg ProjectConfig) ignored(name string) bool {
	for current := name; current != "."; current = path.Dir(current) {
		for _, pattern := range cfg.Ignore {
			if doublestar.MatchUnvalidated(pattern, current) {
				return true
			}
		}
	}
	return false
}

func (cfg ProjectConfig) mapping(name string) (*projectMapping, error) {
	var match *projectMapping
	for _, mapping := range cfg.Mappings {
		if !doublestar.MatchUnvalidated(mapping.Match, name) {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("%s matches both %q and %q", name, match.Match, mapping.Match)
		}
		match = &mapping
	}
	return match, nil
}

func (cfg ProjectConfig) matches(name string) bool {
	for _, mapping := range cfg.Mappings {
		if doublestar.MatchUnvalidated(mapping.Match, name) {
			return true
		}
	}
	return false
}

func (cfg ProjectConfig) Load(fsys fs.FS) (*Project, error) {
	if len(cfg.Mappings) == 0 {
		return nil, nil
	}
	project := &Project{}
	ids, paths := map[string]string{}, map[string]string{}
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if cfg.ignored(name) {
			if cfg.matches(name) {
				project.ignored++
			}
			return nil
		}
		mapping, err := cfg.mapping(name)
		if err != nil || mapping == nil {
			return err
		}
		file, err := mapping.resolve(fsys, name, cfg.ignored)
		if errors.Is(err, errIgnoredProjectRead) {
			project.ignored++
			return nil
		}
		if err != nil {
			return fmt.Errorf("source %s: %w", name, err)
		}
		if previous, exists := ids[file.ID]; exists {
			return fmt.Errorf("%s and %s have duplicate source ID %q", previous, name, file.ID)
		}
		if previous, exists := paths[file.bundlePath]; exists {
			return fmt.Errorf("%s and %s map to the same bundle path %q", previous, name, file.bundlePath)
		}
		ids[file.ID], paths[file.bundlePath] = name, name
		project.files = append(project.files, file)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ProjectConfigName, err)
	}
	if len(project.files) == 0 {
		return nil, fmt.Errorf("%s selects no migration sources", ProjectConfigName)
	}
	return project, nil
}

func (project *Project) bundle() (fs.FS, map[string]scripts.Source, []ProjectSource, error) {
	files := make([]File, 0, len(project.files))
	sources := make([]ProjectSource, 0, len(project.files))
	sql := map[string]scripts.Source{}
	for _, file := range project.files {
		files = append(files, File{Path: file.bundlePath, Content: file.content})
		sources = append(sources, file.ProjectSource)
		if file.Kind == SQLContent {
			sql[file.bundlePath] = scripts.Source{ID: file.ID, Name: cmp.Or(file.Name, file.ID), Path: file.Path, Phase: scripts.Phase(file.Phase)}
		}
	}
	fsys, err := BundleFiles(files)
	return fsys, sql, sources, err
}
