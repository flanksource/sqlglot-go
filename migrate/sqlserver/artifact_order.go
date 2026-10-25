package sqlserver

import (
	"cmp"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
)

func compareName(a, b migrate.Name) int {
	if c := cmp.Compare(a.Schema, b.Schema); c != 0 {
		return c
	}
	return cmp.Compare(a.Name, b.Name)
}

func compareRuntimeDependency(a, b migrate.RuntimeDependency) int {
	return cmp.Or(cmp.Compare(a.Server, b.Server), cmp.Compare(a.Database, b.Database), cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
}

func orderSnapshot(snapshot *migrate.Schema) {
	if snapshot.Tables == nil {
		snapshot.Tables = []migrate.Table{}
	}
	if snapshot.Definitions == nil {
		snapshot.Definitions = []migrate.Definition{}
	}
	slices.SortFunc(snapshot.Tables, func(a, b migrate.Table) int { return compareName(a.Name, b.Name) })
	slices.SortFunc(snapshot.Definitions, func(a, b migrate.Definition) int { return compareName(a.Name, b.Name) })
	slices.SortFunc(snapshot.Sequences, func(a, b migrate.Sequence) int { return compareName(a.Name, b.Name) })
	slices.SortFunc(snapshot.TableTypes, func(a, b migrate.TableType) int { return compareName(a.Name, b.Name) })
	for i := range snapshot.Tables {
		t := &snapshot.Tables[i]
		slices.SortFunc(t.Indexes, func(a, b migrate.Index) int { return cmp.Compare(a.Name, b.Name) })
		slices.SortFunc(t.ForeignKeys, func(a, b migrate.ForeignKey) int { return cmp.Compare(a.Name, b.Name) })
		slices.SortFunc(t.Checks, func(a, b migrate.Check) int { return cmp.Compare(a.Name, b.Name) })
		for j := range t.Indexes {
			slices.Sort(t.Indexes[j].Included)
		}
	}
	for i := range snapshot.Definitions {
		slices.SortFunc(snapshot.Definitions[i].Dependencies, compareName)
		slices.SortFunc(snapshot.Definitions[i].RuntimeDependencies, compareRuntimeDependency)
	}
}
