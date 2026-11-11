package main

import (
	"fmt"
	"log"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

func main() {
	tree, err := sqlglot.ParseOne("SELECT DB_NAME(), CURRENT_USER, SESSION_CONTEXT(N'tenant'), GETDATE(), NEWID()", "tsql")
	if err != nil {
		log.Fatal(err)
	}
	tree.Walk(func(node *sqlglot.Expression) bool {
		if builtin, ok := node.Builtin("tsql"); ok {
			fmt.Printf("%s: %s\n", builtin.Name, builtin.Dependency)
		}
		return true
	})
}
