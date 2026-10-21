package main

import (
	"fmt"
	"log"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

func main() {
	plan, err := sqlserver.Plan(migrate.Request{
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
	for _, step := range plan.Steps {
		fmt.Println(step.SQL + ";")
	}
}
