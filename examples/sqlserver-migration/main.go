package main

import (
	"fmt"
	"log"

	_ "github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

func main() {
	artifact, err := engine.Plan(engine.PlanRequest{
		Dialect: "sqlserver",
		Scope:   engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{"dbo"}},
		Desired: migrate.Schema{Tables: []migrate.Table{{
			Name: migrate.Name{Schema: "dbo", Name: "orders"},
			Columns: []migrate.Column{{Name: "id", Type: "int", Identity: &migrate.Identity{Seed: 1, Increment: 1}},
				{Name: "status", Type: "nvarchar(20)", Default: &migrate.Default{Name: "DF_orders_status", Expression: "N'open'"}}},
			Indexes: []migrate.Index{{Name: "PK_orders", Primary: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}}},
		}}},
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, unit := range artifact.Units {
		for _, op := range unit.Ops {
			for _, statement := range op.Statements {
				fmt.Println(statement.SQL + ";")
			}
		}
	}
}
