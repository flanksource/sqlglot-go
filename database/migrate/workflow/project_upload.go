package workflow

import (
	"fmt"
	"path"
	"strings"
)

type ProjectUpload struct {
	Required []string `json:"required"`
	Plain    bool     `json:"plain,omitempty"`
}

// ProjectPreview counts the sources the selectors keep (Mapped), the matched
// files an ignore rule dropped, and the mapped sources the selectors left out.
type ProjectPreview struct {
	Mapped     int `json:"mapped"`
	Ignored    int `json:"ignored"`
	Deselected int `json:"deselected,omitempty"`
}

func (project *Project) Preview() ProjectPreview {
	return ProjectPreview{Mapped: len(project.files), Ignored: project.ignored, Deselected: len(project.all) - len(project.files)}
}

// ProjectUploadPaths finds the files needed to run the same mapper used by the
// CLI. A transform may read files under the matched file's directory.
func ProjectUploadPaths(config string, paths []string) (ProjectUpload, error) {
	var result ProjectUpload
	cfg, err := ParseProjectConfig([]byte(config))
	if err != nil {
		return result, fmt.Errorf("%s: %w", ProjectConfigName, err)
	}
	result = ProjectUpload{Required: []string{}, Plain: len(cfg.Mappings) == 0}
	seen, matched, companions := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, name := range paths {
		if !validProjectPath(name) || seen[name] {
			return result, fmt.Errorf("invalid or duplicate bundle path %q", name)
		}
		seen[name] = true
		if name == ProjectConfigName {
			matched[name] = true
			continue
		}
		if hiddenProjectPath(name) || !cfg.matches(name) {
			continue
		}
		matched[name] = true
		if cfg.ignored(name) {
			continue
		}
		mapping, err := cfg.mapping(name)
		if err != nil {
			return result, err
		}
		if mapping.Transform != "" {
			companions[path.Dir(name)] = true
		}
	}
	if !seen[ProjectConfigName] {
		return result, fmt.Errorf("%s is required", ProjectConfigName)
	}
	for _, name := range paths {
		if result.Plain {
			if name != ProjectConfigName {
				result.Required = append(result.Required, name)
			}
			continue
		}
		if matched[name] || (!cfg.ignored(name) && inProjectDirectories(name, companions)) {
			result.Required = append(result.Required, name)
		}
	}
	return result, nil
}

func hiddenProjectPath(name string) bool {
	for part := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func inProjectDirectories(name string, dirs map[string]bool) bool {
	for dir := path.Dir(name); ; dir = path.Dir(dir) {
		if dirs[dir] {
			return true
		}
		if dir == "." {
			return false
		}
	}
}

// PreviewProject validates uploaded files without opening a database.
func PreviewProject(files []File) (ProjectPreview, error) {
	fsys, err := BundleFiles(files)
	if err != nil {
		return ProjectPreview{}, err
	}
	project, err := LoadProject(fsys)
	if err != nil {
		return ProjectPreview{}, err
	}
	if project == nil {
		return ProjectPreview{}, fmt.Errorf("%s is required", ProjectConfigName)
	}
	return project.Preview(), nil
}
