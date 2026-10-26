package compare

import "github.com/flanksource/sqlglot-go/migrate"

var Label = label

func SQLServerObjects(current, desired migrate.Schema, changes []migrate.Change) ([]Object, error) {
	return (&sides{dialect: "sqlserver", current: current, desired: desired}).objects(changes)
}
