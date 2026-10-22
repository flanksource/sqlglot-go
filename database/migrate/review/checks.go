package review

import (
	"fmt"
	"github.com/flanksource/sqlglot-go/sqlglot"
	"strings"
)

func sameCheck(left, right, dialect string) (bool, error) {
	if strings.TrimSpace(left) == strings.TrimSpace(right) {
		return true, nil
	}
	a, err := sqlglot.ParseOne("SELECT "+left, dialect)
	if err != nil {
		return false, fmt.Errorf("parse existing check expression: %w", err)
	}
	b, err := sqlglot.ParseOne("SELECT "+right, dialect)
	if err != nil {
		return false, fmt.Errorf("parse desired check expression: %w", err)
	}
	return withoutParens(a).Equal(withoutParens(b)), nil
}

func withoutParens(e *sqlglot.Expression) *sqlglot.Expression {
	if e == nil {
		return nil
	}
	if e.Class == "Paren" {
		return withoutParens(e.This())
	}
	for _, key := range e.Keys {
		switch value := e.Args[key].(type) {
		case *sqlglot.Expression:
			e.Set(key, withoutParens(value))
		case []*sqlglot.Expression:
			for i := range value {
				value[i] = withoutParens(value[i])
			}
		}
	}
	return e
}
