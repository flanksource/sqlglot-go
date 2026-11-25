// Package sqlserver is the pure half of the SQL Server dialect: it compares two
// schemas, lowers the changes to statements and renders reviewed artifacts as
// scripts, all without a database connection.
package sqlserver

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
)

// described is a diff of two snapshots with every change named.
type described struct {
	diff     []schema.Change
	set      *changeset.Set
	warnings []string
	runtime  map[migrate.Name][]migrate.RuntimeDependency
}

// describeChanges diffs the snapshots and names, orders and flags each change.
// Names are exact catalog identifiers.
func describeChanges(current, desired migrate.Schema) (*described, error) {
	d := &described{runtime: map[migrate.Name][]migrate.RuntimeDependency{}}
	warned := map[migrate.Name]bool{}
	warn := func(def migrate.Definition, unparsed *atlas.UnparsedError) {
		if !warned[def.Name] {
			warned[def.Name] = true
			d.warnings = append(d.warnings, fmt.Sprintf("%s %s.%s is compared as written: %s", strings.ToLower(def.Kind), def.Name.Schema, def.Name.Name, unparsed))
		}
	}
	from, to, err := realms(current, desired, warn)
	if err != nil {
		return nil, err
	}
	if d.diff, err = atlas.DefaultDiff.RealmDiff(from, to); err != nil {
		return nil, err
	}
	if d.set, err = changeset.Describe(d.diff, dialect); err != nil {
		return nil, err
	}
	requireDefinitionDependencies(d.set, desired)
	for _, def := range desired.Definitions {
		d.runtime[def.Name] = def.RuntimeDependencies
	}
	for i := range d.set.Changes {
		flagRuntimeDependencies(&d.set.Changes[i], d.runtime)
	}
	return d, nil
}

func flagRuntimeDependencies(change *migrate.Change, runtime map[migrate.Name][]migrate.RuntimeDependency) {
	if change.Action != "drop" && slices.Contains([]string{"procedure", "function", "view", "trigger"}, change.Kind) {
		change.RuntimeDependencies = runtime[change.Object]
	}
}

func realms(current, desired migrate.Schema, warn warnUnparsed) (*schema.Realm, *schema.Realm, error) {
	var namespaces []string
	for _, snapshot := range []migrate.Schema{current, desired} {
		for _, t := range snapshot.Tables {
			namespaces = append(namespaces, t.Name.Schema)
		}
		for _, d := range snapshot.Definitions {
			namespaces = append(namespaces, d.Name.Schema)
		}
		for _, q := range snapshot.Sequences {
			namespaces = append(namespaces, q.Name.Schema)
		}
		for _, t := range snapshot.TableTypes {
			namespaces = append(namespaces, t.Name.Schema)
		}
	}
	slices.Sort(namespaces)
	namespaces = slices.Compact(namespaces)
	if slices.Contains(namespaces, "") {
		return nil, nil, fmt.Errorf("sqlserver: all objects require an explicit schema")
	}
	from, err := realm(current, namespaces, warn)
	if err != nil {
		return nil, nil, fmt.Errorf("current snapshot: %w", err)
	}
	to, err := realm(desired, namespaces, warn)
	if err != nil {
		return nil, nil, fmt.Errorf("desired snapshot: %w", err)
	}
	return from, to, nil
}
