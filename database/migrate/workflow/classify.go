package workflow

import (
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ContentKind is what a bundle file holds: SQL scripts or HCL schema.
type ContentKind string

const (
	SQLContent ContentKind = "sql"
	HCLContent ContentKind = "hcl"
)

// sqlStatements are the words a SQL script opens with once its comments are
// skipped.
var sqlStatements = map[string]bool{
	"ALTER": true, "ANALYZE": true, "BEGIN": true, "CALL": true, "COMMENT": true, "COMMIT": true,
	"CREATE": true, "DECLARE": true, "DELETE": true, "DENY": true, "DO": true, "DROP": true,
	"EXEC": true, "EXECUTE": true, "GO": true, "GRANT": true, "IF": true, "INSERT": true,
	"MERGE": true, "PRAGMA": true, "PRINT": true, "RAISERROR": true, "REPLACE": true, "REVOKE": true,
	"ROLLBACK": true, "SELECT": true, "SET": true, "THROW": true, "TRUNCATE": true, "UPDATE": true,
	"USE": true, "VACUUM": true, "WHILE": true, "WITH": true,
}

// DetectContent is the kind of the file name holding content. A .sql or .hcl
// name is that kind whatever it holds, so its loader reports what is wrong
// with it. Any other file is HCL when it parses as an HCL body declaring
// something, SQL when its first word past comments opens a SQL statement,
// and neither otherwise.
func DetectContent(name, content string) (ContentKind, bool) {
	switch strings.ToLower(path.Ext(name)) {
	case ".sql":
		return SQLContent, true
	case ".hcl":
		return HCLContent, true
	}
	content = strings.TrimPrefix(content, "\xef\xbb\xbf")
	if file, diags := hclsyntax.ParseConfig([]byte(content), name, hcl.InitialPos); !diags.HasErrors() {
		if body := file.Body.(*hclsyntax.Body); len(body.Attributes)+len(body.Blocks) > 0 {
			return HCLContent, true
		}
	}
	if sqlStatements[strings.ToUpper(firstWord(content))] {
		return SQLContent, true
	}
	return "", false
}

// firstWord is the first run of letters in content past whitespace and SQL
// comments.
func firstWord(content string) string {
	for {
		content = strings.TrimLeftFunc(content, unicode.IsSpace)
		switch {
		case strings.HasPrefix(content, "--"):
			_, rest, _ := strings.Cut(content, "\n")
			content = rest
		case strings.HasPrefix(content, "/*"):
			_, rest, found := strings.Cut(content[2:], "*/")
			if !found {
				return ""
			}
			content = rest
		default:
			end := strings.IndexFunc(content, func(r rune) bool { return !unicode.IsLetter(r) })
			if end < 0 {
				end = len(content)
			}
			return content[:end]
		}
	}
}

// ClassifyFiles names every file by the kind DetectContent finds, since the
// bundle loaders select files by extension: a .sql or .hcl path is kept, any
// other path's extension becomes the kind's. A file of neither kind, or two
// files named alike once classified, is an error.
func ClassifyFiles(files []File) ([]File, error) {
	out := make([]File, 0, len(files))
	named := map[string]string{}
	for _, file := range files {
		kind, ok := DetectContent(file.Path, file.Content)
		if !ok {
			return nil, fmt.Errorf("%s holds neither SQL nor HCL", file.Path)
		}
		name := strings.TrimSuffix(file.Path, path.Ext(file.Path)) + "." + string(kind)
		if strings.EqualFold(path.Ext(file.Path), "."+string(kind)) {
			name = file.Path
		}
		if earlier, taken := named[name]; taken {
			return nil, fmt.Errorf("%s and %s are both read as %s", file.Path, earlier, name)
		}
		named[name] = file.Path
		out = append(out, File{Path: name, Content: file.Content})
	}
	return out, nil
}
