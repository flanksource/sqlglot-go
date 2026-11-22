package sqlserver

import (
	"fmt"
	"slices"
	"strings"

	catalog "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

var routineKinds = []string{"FN", "IF", "TF", "P", "FS", "FT", "PC"}

// moduleEffects combines each loaded routine's parsed reads, writes and calls
// with catalog references. For a table or view without a reliable classification,
// the reference implies a possible read and, for a procedure, a possible write.
func (e *observationEpoch) moduleEffects() map[scripts.ObjectName]scripts.Effects {
	out := make(map[scripts.ObjectName]scripts.Effects, len(e.loaded))
	for key, loaded := range e.loaded {
		if loaded.err != nil {
			out[objectName(key)] = scripts.Effects{Unknown: []string{key + ": " + loaded.err.Error()}}
			continue
		}
		catalogued, analysed := e.catalogEffects(key, e.names[key], loaded), loaded.analysis.Analysis.Effects
		out[objectName(key)] = scripts.Effects{Reads: append(catalogued.Reads, analysed.Reads...), Writes: append(catalogued.Writes, analysed.Writes...),
			Calls: append(catalogued.Calls, analysed.Calls...), Unknown: append(catalogued.Unknown, prefixed(key, analysed.Unknown)...)}.Normalized()
	}
	return out
}

func prefixed(key string, lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = key + ": " + line
	}
	return out
}

func (e *observationEpoch) catalogEffects(key string, m catalog.Module, loaded loadedModule) scripts.Effects {
	var effects scripts.Effects
	procedure := m.Kind == "P" || m.Kind == "PC"
	analysed := loaded.analysis.Analysis.Effects
	for _, ref := range loaded.refs {
		switch {
		case strings.HasPrefix(ref.Name, "#"):
		case ref.Server != "" || ref.Database != "":
			effects.Unknown = append(effects.Unknown, fmt.Sprintf("%s: external reference %s.%s.%s.%s", key, ref.Server, ref.Database, ref.Schema, ref.Name))
		case ref.Schema == "":
			effects.Unknown = append(effects.Unknown, fmt.Sprintf("%s: unresolved reference %s", key, ref.Name))
		case slices.Contains(routineKinds, ref.Kind):
			effects.Calls = append(effects.Calls, scripts.NewObjectName(ref.Schema, ref.Name))
		default:
			object := scripts.NewObjectName(ref.Schema, ref.Name)
			if ref.Bound && len(analysed.Unknown) == 0 && (slices.Contains(analysed.Reads, object) || slices.Contains(analysed.Writes, object)) {
				continue
			}
			effects.Reads = append(effects.Reads, object)
			if procedure {
				effects.Writes = append(effects.Writes, object)
			}
		}
	}
	for _, call := range loaded.analysis.Analysis.Calls {
		if called, ok := e.names[call]; ok {
			effects.Calls = append(effects.Calls, scripts.NewObjectName(called.Schema, called.Name))
		} else {
			effects.Calls = append(effects.Calls, objectName(call))
		}
	}
	return effects
}

func objectName(key string) scripts.ObjectName {
	schema, name, _ := strings.Cut(key, ".")
	return scripts.NewObjectName(schema, name)
}
