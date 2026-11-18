package csvdata

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

const ConfigName = "AS.csv.yaml"

type ValueSettings struct {
	Blank         string                   `yaml:"blank" json:"blank,omitempty"`
	NullTokens    []string                 `yaml:"nullTokens" json:"nullTokens,omitempty"`
	NullTokenCase string                   `yaml:"nullTokenCase" json:"nullTokenCase,omitempty"`
	Columns       map[string]ValueSettings `yaml:"columns" json:"columns,omitempty"`
}

type Settings struct {
	Key           []string      `yaml:"key" json:"key,omitempty"`
	Delimiter     string        `yaml:"delimiter" json:"delimiter,omitempty"`
	IgnoreColumns []string      `yaml:"ignoreColumns" json:"ignoreColumns,omitempty"`
	Phase         string        `yaml:"phase" json:"phase,omitempty"`
	Delete        string        `yaml:"delete" json:"delete,omitempty"`
	Values        ValueSettings `yaml:"values" json:"values"`
}

type tableSettings struct {
	Settings      `yaml:",inline"`
	Sources       []string `yaml:"sources"`
	SnapshotFiles []string `yaml:"snapshotFiles"`
}

type sidecar struct {
	Settings `yaml:",inline"`
	Version  int    `yaml:"version"`
	Table    string `yaml:"table"`
}

type layer struct {
	Values        []string `yaml:"values"`
	Match         []string `yaml:"match"`
	ValuePattern  string   `yaml:"valuePattern"`
	DefaultFilter string   `yaml:"defaultFilter"`
}

type config struct {
	Version  int `yaml:"version"`
	Defaults struct {
		Settings     `yaml:",inline"`
		Schema       string `yaml:"schema"`
		TablePattern string `yaml:"tablePattern"`
	} `yaml:"defaults"`
	Tables map[string]tableSettings `yaml:"tables"`
	Layers map[string]layer         `yaml:"layers"`
}

type File struct {
	Path     string            `json:"path"`
	Table    string            `json:"table"`
	Route    string            `json:"route"`
	Tags     map[string]string `json:"tags"`
	Hash     string            `json:"hash"`
	settings Settings
	contents []byte
}

type Bundle struct {
	Files     []File
	Unmatched []string
	Filters   map[string][]string
	Hash      string
	snapshots map[string][]string
}

var identifier = regexp.MustCompile(`^[\pL_][\pL\pN_@$#]*$`)

func loadYAML(body []byte, destination any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("expected a single YAML document")
	}
	return nil
}

func Load(root fs.FS, schema string, filters map[string]string) (Bundle, error) {
	var cfg config
	var result Bundle
	global, err := fs.ReadFile(root, ConfigName)
	if err == nil {
		if err := loadYAML(global, &cfg); err != nil {
			return result, fmt.Errorf("%s: %w", ConfigName, err)
		}
		if cfg.Version != 1 {
			return result, fmt.Errorf("%s: expected version 1", ConfigName)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return result, err
	}
	if cfg.Defaults.Schema != "" {
		schema = cfg.Defaults.Schema
	}
	var pattern *regexp.Regexp
	if cfg.Defaults.TablePattern != "" {
		pattern, err = regexp.Compile("^(?:" + cfg.Defaults.TablePattern + ")$")
		if err != nil {
			return result, fmt.Errorf("invalid tablePattern: %w", err)
		}
		names := pattern.SubexpNames()
		index := pattern.SubexpIndex("table")
		named := 0
		for _, name := range names {
			if name != "" {
				named++
			}
		}
		if index < 0 || named != 1 || !identifier.MatchString(schema) {
			return result, fmt.Errorf("tablePattern requires one table capture and a valid schema")
		}
	}
	for table, settings := range cfg.Tables {
		if !validTable(table) {
			return result, fmt.Errorf("invalid qualified table %q", table)
		}
		for _, pattern := range settings.Sources {
			if !fs.ValidPath(pattern) || strings.Contains(pattern, "\\") || !doublestar.ValidatePattern(pattern) {
				return result, fmt.Errorf("%s: invalid source pattern %q", table, pattern)
			}
		}
		if settings.Delete != "snapshot" && settings.SnapshotFiles != nil {
			return result, fmt.Errorf("%s: snapshotFiles requires snapshot deletion", table)
		}
		if settings.Delete == "snapshot" && len(settings.SnapshotFiles) == 0 {
			return result, fmt.Errorf("%s: snapshot requires snapshotFiles", table)
		}
		for _, source := range settings.SnapshotFiles {
			if !fs.ValidPath(source) || strings.ContainsAny(source, "\\*?[]{}") || !strings.HasSuffix(source, ".csv") {
				return result, fmt.Errorf("%s: invalid snapshot path %q", table, source)
			}
		}
	}
	if cfg.Defaults.Delete == "snapshot" {
		return result, fmt.Errorf("snapshot deletion requires a table declaration")
	}
	layers, err := compileLayers(cfg.Layers, filters)
	if err != nil {
		return result, err
	}
	result.Filters = make(map[string][]string, len(layers))
	for name, layer := range layers {
		result.Filters[name] = layer.selected
	}
	result.snapshots = make(map[string][]string)
	for table, settings := range cfg.Tables {
		result.snapshots[table] = settings.SnapshotFiles
	}
	inputHashes := map[string]string{ConfigName: digest(global)}
	var paths []string
	err = fs.WalkDir(root, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("CSV root contains symbolic link %s", name)
		}
		if entry.Name() == ConfigName && name != ConfigName {
			return fmt.Errorf("nested %s", name)
		}
		if name != ConfigName && strings.HasSuffix(name, ".csv.yaml") {
			info, err := fs.Stat(root, strings.TrimSuffix(name, ".yaml"))
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("orphan CSV sidecar %s", name)
			}
		}
		if strings.HasSuffix(name, ".csv") {
			if !entry.Type().IsRegular() {
				return fmt.Errorf("CSV source %s is not a regular file", name)
			}
			paths = append(paths, name)
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	for _, name := range paths {
		file, err := loadFile(root, cfg, name, schema, pattern, layers, inputHashes)
		if err != nil {
			return result, fmt.Errorf("%s: %w", name, err)
		}
		if file.Table == "" {
			result.Unmatched = append(result.Unmatched, name)
		}
		if file.contents != nil {
			result.Files = append(result.Files, file)
		}
	}
	result.Hash = jsonDigest(inputHashes)
	return result, nil
}

func loadFile(root fs.FS, cfg config, name, schema string, pattern *regexp.Regexp, layers map[string]compiledLayer, hashes map[string]string) (File, error) {
	file := File{Path: name}
	var local sidecar
	body, err := fs.ReadFile(root, name+".yaml")
	if err == nil {
		if err := loadYAML(body, &local); err != nil {
			return file, err
		}
		if local.Version != 1 {
			return file, fmt.Errorf("sidecar expects version 1")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return file, err
	}
	for table, settings := range cfg.Tables {
		for _, glob := range settings.Sources {
			if !doublestar.MatchUnvalidated(glob, name) {
				continue
			}
			if file.Table != "" && file.Table != table {
				return file, fmt.Errorf("conflicting table mappings")
			}
			file.Table, file.Route = table, "sources"
		}
	}
	if local.Table != "" {
		if !validTable(local.Table) || (file.Table != "" && local.Table != file.Table) {
			return file, fmt.Errorf("invalid or conflicting sidecar table")
		}
		file.Table, file.Route = local.Table, "sidecar"
	}
	if file.Table == "" && pattern != nil {
		if match := pattern.FindStringSubmatch(name); match != nil {
			table := match[pattern.SubexpIndex("table")]
			if !identifier.MatchString(table) {
				return file, fmt.Errorf("invalid inferred table")
			}
			file.Table, file.Route = schema+"."+table, "tablePattern"
		}
	}
	if file.Table == "" {
		if body != nil {
			return file, fmt.Errorf("sidecar requires a target table")
		}
		return file, nil
	}
	file.Tags, err = classify(name, layers)
	if err != nil {
		return file, err
	}
	for name, value := range file.Tags {
		if !contains(layers[name].selected, value) {
			return file, nil
		}
	}
	settings := cfg.Tables[file.Table].Settings
	if local.Delete == "snapshot" || (settings.Delete == "snapshot" && local.Delete != "") {
		return file, fmt.Errorf("snapshot deletion can only be set at table scope")
	}
	file.settings = Settings{Delimiter: ",", Delete: "explicit", Phase: "pre-rules", Values: ValueSettings{Blank: "empty-string", NullTokens: []string{`\N`}, NullTokenCase: "sensitive"}}
	file.settings.merge(cfg.Defaults.Settings)
	file.settings.merge(settings)
	file.settings.merge(local.Settings)
	if err := file.settings.validate(); err != nil {
		return file, err
	}
	if body != nil {
		hashes[name+".yaml"] = digest(body)
	}
	file.contents, err = fs.ReadFile(root, name)
	if err != nil {
		return file, err
	}
	if len(file.contents) == 0 {
		return file, fmt.Errorf("missing CSV header")
	}
	file.Hash = digest(file.contents)
	hashes[name] = file.Hash
	return file, nil
}

func validTable(table string) bool {
	schema, name, ok := strings.Cut(table, ".")
	return ok && identifier.MatchString(schema) && identifier.MatchString(name)
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func jsonDigest(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return digest(body)
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (f File) untagged() bool {
	for _, tag := range f.Tags {
		if tag != "none" {
			return false
		}
	}
	return true
}

func IsConfigPath(name string) bool {
	return path.Base(name) == ConfigName || strings.HasSuffix(name, ".csv.yaml")
}
