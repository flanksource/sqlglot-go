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
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/flanksource/commons/collections"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"gopkg.in/yaml.v3"
)

const ProjectConfigName = ".sqlglot-go.yaml"

// Project is the set of sources a config maps. Its files are the ones its
// selectors keep; all holds every mapped file so an explicit Select replaces
// the config's default selection instead of narrowing it.
type Project struct {
	all       []ProjectFile
	files     []ProjectFile
	ignored   int
	selectors []string
}

type ProjectSource struct {
	Path   string              `json:"path"`
	Kind   ContentKind         `json:"kind"`
	ID     string              `json:"id"`
	Name   string              `json:"name,omitempty"`
	Phase  string              `json:"phase,omitempty"`
	Labels map[string][]string `json:"labels,omitempty"`
}

// ProjectFile is a mapped source with the path it takes in the bundle and the
// content the bundle holds (a transform's result, not the file's bytes).
type ProjectFile struct {
	ProjectSource
	BundlePath string
	Content    string
	// Inputs are the project-relative paths whose bytes went into Content: the
	// mapped file first, then each file its transform read, sorted and listed once.
	Inputs []string `json:"inputs,omitempty"`
}

type ProjectConfig struct {
	Version  int              `yaml:"version"`
	Mappings []ProjectMapping `yaml:"mappings"`
	// Select is the default label selector, used when no explicit selectors are given.
	Select      []string          `yaml:"select"`
	Ignore      []string          `yaml:"ignore"`
	Connections map[string]string `yaml:"connections" json:"-"`
}

type ProjectMapping struct {
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
	config, err := ParseProjectConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ProjectConfigName, err)
	}
	return &config, nil
}

var projectAliasName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ParseProjectConfig decodes and validates a .sqlglot-go.yaml document.
func ParseProjectConfig(data []byte) (ProjectConfig, error) {
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
	if _, err := parseLabelSelectors(cfg.Select); err != nil {
		return cfg, fmt.Errorf("select: %w", err)
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

func (cfg ProjectConfig) mapping(name string) (*ProjectMapping, error) {
	var match *ProjectMapping
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

// Load maps the files of fsys. The config's select is applied as the default
// selection: Files and Bundle see only the sources it keeps, and Select can
// replace it.
func (cfg ProjectConfig) Load(fsys fs.FS) (*Project, error) {
	if len(cfg.Mappings) == 0 {
		return nil, nil
	}
	project, err := cfg.mapFiles(fsys)
	if err != nil {
		return nil, err
	}
	return project.withSelectors(cfg.Select)
}

func (cfg ProjectConfig) mapFiles(fsys fs.FS) (*Project, error) {
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
		if previous, exists := paths[file.BundlePath]; exists {
			return fmt.Errorf("%s and %s map to the same bundle path %q", previous, name, file.BundlePath)
		}
		ids[file.ID], paths[file.BundlePath] = name, name
		project.all = append(project.all, file)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ProjectConfigName, err)
	}
	if len(project.all) == 0 {
		return nil, fmt.Errorf("%s selects no migration sources", ProjectConfigName)
	}
	return project, nil
}

// Files lists the selected sources in path order. The result is a copy.
func (project *Project) Files() []ProjectFile {
	files := slices.Clone(project.files)
	for i := range files {
		files[i].Labels = cloneLabels(files[i].Labels)
		files[i].Inputs = slices.Clone(files[i].Inputs)
	}
	return files
}

// Bundle assembles the selected sources into a bundle filesystem, the script
// metadata keyed by bundle path, and the source inventory.
func (project *Project) Bundle() (fs.FS, map[string]scripts.Source, []ProjectSource, error) {
	if err := project.requireSources(); err != nil {
		return nil, nil, nil, err
	}
	files := make([]File, 0, len(project.files))
	sources := make([]ProjectSource, 0, len(project.files))
	sql := map[string]scripts.Source{}
	for _, file := range project.Files() {
		files = append(files, File{Path: file.BundlePath, Content: file.Content})
		sources = append(sources, file.ProjectSource)
		if file.Kind == SQLContent {
			sql[file.BundlePath] = scripts.Source{ID: file.ID, Name: cmp.Or(file.Name, file.ID), Path: file.Path, Phase: scripts.Phase(file.Phase), Labels: cloneLabels(file.Labels)}
		}
	}
	fsys, err := BundleFiles(files)
	return fsys, sql, sources, err
}

func cloneLabels(labels map[string][]string) map[string][]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string][]string, len(labels))
	for key, values := range labels {
		out[key] = slices.Clone(values)
	}
	return out
}

// Select returns the project narrowed to the sources whose labels satisfy the
// selectors, which replace the config's default select entirely. No selectors
// returns the project as it is. Selecting nothing is an error.
func (project *Project) Select(selectors []string) (*Project, error) {
	if len(selectors) == 0 {
		return project, nil
	}
	selected, err := project.withSelectors(selectors)
	if err != nil {
		return nil, err
	}
	return selected, selected.requireSources()
}

func (project *Project) withSelectors(selectors []string) (*Project, error) {
	parsed, err := parseLabelSelectors(selectors)
	if err != nil {
		return nil, err
	}
	selected := &Project{all: project.all, ignored: project.ignored, selectors: selectors}
	for _, file := range project.all {
		if parsed.keep(file.Labels) {
			selected.files = append(selected.files, file)
		}
	}
	return selected, nil
}

func (project *Project) requireSources() error {
	if len(project.files) > 0 {
		return nil
	}
	quoted := make([]string, len(project.selectors))
	for i, selector := range project.selectors {
		quoted[i] = fmt.Sprintf("%q", selector)
	}
	return fmt.Errorf("label selectors %s select no migration sources: all %d mapped sources are left out", strings.Join(quoted, ", "), len(project.all))
}

// labelSelectors groups the selectors of one label key: patterns holds
// includes as written and excludes with a leading "!", the form
// collections.MatchAny takes.
type labelSelectors map[string]*keySelectors

type keySelectors struct {
	patterns []string
	includes bool
}

// parseLabelSelectors reads "key=pattern" includes and "!key=pattern"
// excludes. A pattern matches case-insensitively and may start or end with
// "*"; lists in one entry are refused so every entry means one thing.
func parseLabelSelectors(entries []string) (labelSelectors, error) {
	parsed := labelSelectors{}
	for _, entry := range entries {
		exclude := strings.HasPrefix(entry, "!")
		key, pattern, ok := strings.Cut(strings.TrimPrefix(entry, "!"), "=")
		key, pattern = strings.TrimSpace(key), strings.TrimSpace(pattern)
		switch {
		case !ok || key == "" || pattern == "":
			return nil, fmt.Errorf("invalid label selector %q: expected key=pattern or !key=pattern", entry)
		case strings.ContainsAny(pattern, ",%") || strings.Contains(strings.Trim(pattern, "*"), "*"):
			return nil, fmt.Errorf("invalid label selector %q: a pattern is one value, optionally starting or ending with *", entry)
		}
		current := parsed[key]
		if current == nil {
			current = &keySelectors{}
			parsed[key] = current
		}
		if exclude {
			pattern = "!" + pattern
		} else {
			current.includes = true
		}
		current.patterns = append(current.patterns, pattern)
	}
	return parsed, nil
}

// keep reports whether labels satisfy every key's selectors: none of the
// key's values matches an exclude and, when the key has includes, one value
// matches an include. A source without the key fails includes and passes
// excludes.
func (selectors labelSelectors) keep(labels map[string][]string) bool {
	for key, selector := range selectors {
		values := labels[key]
		if len(values) == 0 {
			if selector.includes {
				return false
			}
			continue
		}
		if matched, excluded := collections.MatchAny(values, selector.patterns...); excluded || !matched {
			return false
		}
	}
	return true
}
