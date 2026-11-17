package migrate

import "github.com/flanksource/sqlglot-go/database/migrate/scripts"

func (s *script) bundleScript() scripts.Script {
	return scripts.Script{ID: s.path, Path: s.path, Name: s.path, SQL: s.content, Phase: scripts.Phase(s.phase), Always: s.always, NonTransactional: !s.transactional, DependsOn: s.dependsOn}
}

func scriptBundle(items map[string]*script) []scripts.Script {
	bundle := make([]scripts.Script, 0, len(items))
	for _, item := range items {
		bundle = append(bundle, item.bundleScript())
	}
	return bundle
}
