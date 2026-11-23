package sqlglot

import (
	"fmt"
	"strings"
)

func checkSQLCMD(src []rune, tokens []Token) error {
	for _, token := range tokens {
		if token.Type == TokHINT {
			continue
		}
		offset := token.Start
		if src[offset] == ':' || src[offset] == '!' && offset+1 < len(src) && src[offset+1] == '!' {
			start := offset
			for start > 0 && src[start-1] != '\n' {
				start--
			}
			if strings.TrimSpace(string(src[start:offset])) == "" {
				line, _, _ := strings.Cut(string(src[offset:]), "\n")
				return fmt.Errorf("line %d: sqlcmd command %q is not supported; scripts are plain T-SQL batches separated by GO", token.Line, strings.TrimSpace(line))
			}
		}
		for i := offset; i <= token.End && i+1 < len(src); i++ {
			if src[i] == '$' && src[i+1] == '(' {
				line := 1 + strings.Count(string(src[:i]), "\n")
				return fmt.Errorf("line %d: sqlcmd scripting variables ($(...)) are not supported; sqlcmd would substitute them", line)
			}
		}
	}
	return nil
}
