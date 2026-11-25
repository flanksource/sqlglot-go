package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"github.com/antchfx/xmlquery"
	"github.com/flanksource/gomplate/v4"
)

var errIgnoredProjectRead = errors.New("transform reads an ignored file")

type sourceTransform struct {
	SQL    *string             `json:"sql"`
	HCL    *string             `json:"hcl"`
	ID     *string             `json:"id"`
	Name   string              `json:"name"`
	Phase  string              `json:"phase"`
	Labels map[string][]string `json:"labels"`
}

func (mapping ProjectMapping) resolve(fsys fs.FS, name string, ignored func(string) bool) (ProjectFile, error) {
	data, err := readProjectFile(fsys, name)
	if err != nil {
		return ProjectFile{}, err
	}
	file := ProjectFile{ProjectSource: ProjectSource{Path: name, Kind: mapping.Kind, Labels: maps.Clone(mapping.Labels)}, Content: string(data)}
	if file.Kind == "" {
		switch strings.ToLower(path.Ext(name)) {
		case ".hcl":
			file.Kind = HCLContent
		case ".sql":
			file.Kind = SQLContent
		}
	}
	file.Inputs = []string{name}
	if mapping.Transform != "" {
		transform, reads, err := runProjectTransform(fsys, file, mapping.Transform, ignored)
		if err != nil {
			return file, err
		}
		if err := file.apply(transform); err != nil {
			return file, err
		}
		file.Inputs = append(file.Inputs, reads...)
	}
	if file.Kind == "" {
		return file, fmt.Errorf("cannot infer kind from file extension, set kind or return hcl or sql from a transform")
	}
	file.BundlePath = strings.TrimSuffix(name, path.Ext(name)) + "." + string(file.Kind)
	if file.ID == "" {
		file.ID = file.BundlePath
	}
	if file.Kind == HCLContent {
		if !validProjectPath(file.ID) || !strings.EqualFold(path.Ext(file.ID), ".hcl") {
			return file, fmt.Errorf("HCL ID %q must be a relative .hcl path", file.ID)
		}
		file.BundlePath = file.ID
	}
	return file, nil
}

func (file *ProjectFile) apply(out sourceTransform) error {
	if (out.SQL == nil) == (out.HCL == nil) {
		return fmt.Errorf("transform must return exactly one of hcl or sql")
	}
	kind, content := HCLContent, out.HCL
	if out.SQL != nil {
		kind, content = SQLContent, out.SQL
	}
	if file.Kind != "" && file.Kind != kind {
		return fmt.Errorf("transform returns %s for a %s mapping", kind, file.Kind)
	}
	if (out.Name != "" || out.Phase != "") && kind != SQLContent {
		return fmt.Errorf("transform name and phase require SQL")
	}
	if out.Phase != "" && out.Phase != "pre" && out.Phase != "post" {
		return fmt.Errorf("invalid SQL phase %q", out.Phase)
	}
	if out.ID != nil {
		file.ID = strings.TrimSpace(*out.ID)
		if file.ID == "" {
			return fmt.Errorf("transform ID must be nonempty")
		}
	}
	if err := validateProjectLabels(out.Labels); err != nil {
		return err
	}
	for key, values := range out.Labels {
		if len(values) == 0 {
			delete(file.Labels, key)
			continue
		}
		if file.Labels == nil {
			file.Labels = make(map[string][]string)
		}
		file.Labels[key] = values
	}
	file.Kind, file.Content, file.Name, file.Phase = kind, *content, out.Name, out.Phase
	return nil
}

func validateProjectLabels(labels map[string][]string) error {
	for key, values := range labels {
		if strings.TrimSpace(key) == "" || values == nil {
			return fmt.Errorf("label %q requires a name and a list of strings", key)
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("label %q contains an empty value", key)
			}
		}
	}
	return nil
}

// runProjectTransform evaluates a mapping's transform. It also returns the
// sorted, de-duplicated project paths the transform's read() returned, other
// than the mapped file itself.
func runProjectTransform(fsys fs.FS, file ProjectFile, expression string, ignored func(string) bool) (sourceTransform, []string, error) {
	var out sourceTransform
	var ignoredRead bool
	reads := map[string]bool{}
	read := func(arg ref.Val) ref.Val {
		name := arg.Value().(string)
		if !validProjectPath(name) {
			return types.NewErr("read path %q must be relative to the source directory", name)
		}
		name = path.Join(path.Dir(file.Path), name)
		if ignored(name) {
			ignoredRead = true
			return types.WrapErr(errIgnoredProjectRead)
		}
		data, err := readProjectFile(fsys, name)
		if err != nil {
			return types.WrapErr(err)
		}
		reads[name] = true
		return types.String(data)
	}
	value, err := evaluateSourceTransform(expression, file.Path, file.Content, nil, read)
	if ignoredRead {
		return out, nil, errIgnoredProjectRead
	}
	if err != nil {
		return out, nil, fmt.Errorf("transform: %w", err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return out, nil, fmt.Errorf("encode transform result: %w", err)
	}
	if err := Decode(data, &out); err != nil {
		return out, nil, fmt.Errorf("invalid transform result: %w", err)
	}
	delete(reads, file.Path)
	return out, slices.Sorted(maps.Keys(reads)), nil
}

// TransformSource evaluates a project source expression with the shared file
// and XML helpers. The caller confines reads to the selected source tree.
func TransformSource(expression, filename, content string, captures map[string]string, read func(string) (string, error)) (map[string]any, error) {
	return evaluateSourceTransform(expression, filename, content, captures, func(arg ref.Val) ref.Val {
		content, err := read(arg.Value().(string))
		if err != nil {
			return types.WrapErr(err)
		}
		return types.String(content)
	})
}

func evaluateSourceTransform(expression, filename, content string, captures map[string]string, read func(ref.Val) ref.Val) (map[string]any, error) {
	input := map[string]any{"file": map[string]string{"path": filename, "content": content}}
	if captures != nil {
		input["captures"] = captures
	}
	value, err := gomplate.RunExpression(input, gomplate.Template{Expression: expression, CelEnvs: projectCEL(read)})
	if err != nil {
		return nil, err
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("transform must return a map")
	}
	return fields, nil
}

func projectCEL(read func(ref.Val) ref.Val) []cel.EnvOption {
	return []cel.EnvOption{
		cel.Function("read", cel.Overload("project_read_string", []*cel.Type{cel.StringType}, cel.StringType, cel.UnaryBinding(read))),
		cel.Function("xmlpath", cel.Overload("project_xmlpath_string_string", []*cel.Type{cel.StringType, cel.StringType}, cel.StringType, cel.BinaryBinding(projectXMLPath))),
		cel.Function("lower", cel.MemberOverload("project_string_lower", []*cel.Type{cel.StringType}, cel.StringType,
			cel.UnaryBinding(func(value ref.Val) ref.Val { return types.String(strings.ToLower(value.Value().(string))) }))),
	}
}

func projectXMLPath(xml, selector ref.Val) ref.Val {
	doc, err := xmlquery.Parse(strings.NewReader(xml.Value().(string)))
	if err != nil {
		return types.NewErr("xmlpath: invalid XML: %v", err)
	}
	query := selector.Value().(string)
	nodes, err := xmlquery.QueryAll(doc, query)
	if err != nil {
		return types.NewErr("xmlpath %q: %v", query, err)
	}
	if len(nodes) != 1 {
		return types.NewErr("xmlpath %q: expected one match, got %d", query, len(nodes))
	}
	return types.String(nodes[0].InnerText())
}

func readProjectFile(fsys fs.FS, name string) ([]byte, error) {
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("read %s: expected a regular file", name)
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return data, nil
}
