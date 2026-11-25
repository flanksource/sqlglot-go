package migrate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// repeatedSchemaBlocks finds the schema blocks that repeat an identical block of
// an earlier file. A bundle may start every file with its own `schema "dbo" {}`
// (a tool that evaluates one file alone needs it there), and the evaluator
// would read each repetition as a schema of its own, so loadHCL blanks them and
// the first declaration stands for all. A block of the same name that differs
// from the first is an error naming both files. files must be in evaluation order.
func repeatedSchemaBlocks(files []parsedHCLFile) (map[*hclsyntax.Block]bool, error) {
	type declaration struct {
		file      string
		line      int
		signature string
	}
	first := map[string]declaration{}
	repeated := map[*hclsyntax.Block]bool{}
	for _, file := range files {
		for _, block := range file.body.Blocks {
			if block.Type != "schema" || len(block.Labels) != 1 {
				continue
			}
			here := declaration{file: file.name, line: block.Range().Start.Line, signature: blockSignature(file.data, block)}
			earlier, seen := first[block.Labels[0]]
			switch {
			case !seen:
				first[block.Labels[0]] = here
			case earlier.signature != here.signature:
				return nil, fmt.Errorf("schema %q is declared differently in %s:%d and %s:%d: repeat the block identically or declare it once",
					block.Labels[0], earlier.file, earlier.line, here.file, here.line)
			default:
				repeated[block] = true
			}
		}
	}
	return repeated, nil
}

// refuseDuplicateTables fails when one table is declared twice, in two files or
// in one, naming both places: a declaration is the whole truth about its table,
// so two of them (one possibly partial) have no meaning. A table is named by its
// schema and name; files must be in evaluation order.
func refuseDuplicateTables(files []parsedHCLFile) error {
	first := map[string]string{}
	for _, file := range files {
		for _, block := range file.body.Blocks {
			if block.Type != "table" || len(block.Labels) == 0 {
				continue
			}
			name := block.Labels[len(block.Labels)-1]
			schemaName := blockSchema(block, "")
			if len(block.Labels) > 1 {
				schemaName = block.Labels[0]
			}
			key, here := schemaName+"."+name, fmt.Sprintf("%s:%d", file.name, block.Range().Start.Line)
			if earlier, seen := first[key]; seen {
				return fmt.Errorf("table %q is declared twice, in %s and %s: declare it once", key, earlier, here)
			}
			first[key] = here
		}
	}
	return nil
}

// evaluableSource is the file with what the schema evaluator must not see
// blanked out, keeping every line and offset: the role and permission blocks
// (decoded separately) and the repeated schema blocks.
func evaluableSource(file parsedHCLFile, repeated map[*hclsyntax.Block]bool) []byte {
	filtered := slices.Clone(file.data)
	for _, block := range file.body.Blocks {
		if block.Type != "role" && block.Type != "permission" && !repeated[block] {
			continue
		}
		r := block.Range()
		for i := r.Start.Byte; i < r.End.Byte && i < len(filtered); i++ {
			if filtered[i] != '\n' && filtered[i] != '\r' {
				filtered[i] = ' '
			}
		}
	}
	return filtered
}

// blockSignature is the block's labels, attributes (by name, with the source
// text of each expression) and nested blocks, so two blocks that declare the
// same thing have the same signature whatever their layout.
func blockSignature(data []byte, block *hclsyntax.Block) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s %q {", block.Type, block.Labels)
	names := make([]string, 0, len(block.Body.Attributes))
	for name := range block.Body.Attributes {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		r := block.Body.Attributes[name].Expr.Range()
		fmt.Fprintf(&out, "%s=%s;", name, strings.TrimSpace(string(data[r.Start.Byte:r.End.Byte])))
	}
	for _, child := range block.Body.Blocks {
		out.WriteString(blockSignature(data, child))
	}
	out.WriteString("}")
	return out.String()
}
