package workflow

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

func BundleFiles(files []File) (fs.FS, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("bundle files are required")
	}
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	seen := map[string]bool{}
	for _, file := range files {
		if !fs.ValidPath(file.Path) || file.Path == "." || strings.Contains(file.Path, "\\") || seen[file.Path] {
			return nil, fmt.Errorf("invalid or duplicate bundle path %q", file.Path)
		}
		seen[file.Path] = true
		entry, err := w.CreateHeader(&zip.FileHeader{Name: file.Path, Method: zip.Store})
		if err != nil {
			return nil, err
		}
		if _, err = io.WriteString(entry, file.Content); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return zip.NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()))
}

func Decode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON value")
	}
	return nil
}

func Variables(values []string) (map[string]cty.Value, error) {
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
