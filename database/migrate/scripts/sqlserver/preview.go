package sqlserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

func prepare(opts Options) (Options, string, error) {
	if opts.DB == nil || strings.TrimSpace(opts.Scope) == "" || len(opts.Scope) > 256 {
		return Options{}, "", fmt.Errorf("script database and scope (up to 256 characters) are required")
	}
	encoded, err := json.Marshal(struct {
		Scripts   []scripts.Script
		Functions []Function
	}{opts.Scripts, opts.Functions})
	if err != nil {
		return Options{}, "", err
	}
	var copy struct {
		Scripts   []scripts.Script
		Functions []Function
	}
	if err := json.Unmarshal(encoded, &copy); err != nil {
		return Options{}, "", err
	}
	opts.Scripts, opts.Functions = copy.Scripts, copy.Functions
	slices.SortFunc(opts.Functions, func(a, b Function) int { return strings.Compare(a.Name, b.Name) })
	for i := range opts.Functions {
		slices.Sort(opts.Functions[i].Prerequisites)
	}
	registryJSON, err := json.Marshal(opts.Functions)
	if err != nil {
		return Options{}, "", err
	}
	registered := map[string]bool{}
	for _, f := range opts.Functions {
		if _, err := scripts.QuoteName(f.Name); err != nil {
			return Options{}, "", err
		}
		if f.Type == "" || registered[strings.ToLower(f.Name)] {
			return Options{}, "", fmt.Errorf("invalid or duplicate registered function %q", f.Name)
		}
		registered[strings.ToLower(f.Name)] = true
	}
	for i := range opts.Scripts {
		s := &opts.Scripts[i]
		s.ContextPolicy = scripts.SourceHash(string(registryJSON))
		if len(s.ID) > 512 {
			return Options{}, "", fmt.Errorf("script ID exceeds 512 characters")
		}
		calls := scripts.Analyze(scripts.AnalysisOptions{SQL: s.SQL}).Calls
		for _, d := range s.Dependencies {
			if d.Kind == "function-result" {
				calls = append(calls, d.Name)
			}
		}
		for _, f := range opts.Functions {
			for _, call := range calls {
				if strings.EqualFold(call, f.Name) {
					for _, id := range f.Prerequisites {
						if !slices.Contains(s.DependsOn, id) && id != s.ID {
							s.DependsOn = append(s.DependsOn, id)
						}
					}
				}
			}
		}
		slices.Sort(s.DependsOn)
		slices.SortFunc(s.Dependencies, func(a, b scripts.Dependency) int {
			if order := strings.Compare(a.Kind, b.Kind); order != 0 {
				return order
			}
			return strings.Compare(a.Name, b.Name)
		})
	}
	ordered, err := scripts.Ordered(opts.Scripts)
	if err != nil {
		return Options{}, "", err
	}
	opts.Scripts = ordered
	canonical, err := json.Marshal(struct {
		Scripts   []scripts.Script
		Functions []Function
	}{opts.Scripts, opts.Functions})
	if err != nil {
		return Options{}, "", err
	}
	return opts, scripts.SourceHash(string(canonical)), nil
}

// Inspect reads current inputs and records without creating metadata or running scripts.
func Inspect(ctx context.Context, opts Options, phase scripts.Phase) (Preview, error) {
	prepared, hash, err := prepare(opts)
	if err != nil {
		return Preview{}, err
	}
	return inspect(ctx, prepared.DB, prepared, phase, hash)
}

func inspect(ctx context.Context, q queryer, opts Options, phase scripts.Phase, bundleHash string) (Preview, error) {
	rows, err := q.QueryContext(ctx, `SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')),DB_NAME()`)
	if err != nil {
		return Preview{}, err
	}
	var server, database string
	if !rows.Next() {
		_ = rows.Close()
		return Preview{}, fmt.Errorf("target identity returned no row")
	}
	if err := rows.Scan(&server, &database); err != nil {
		_ = rows.Close()
		return Preview{}, err
	}
	if err := rows.Close(); err != nil {
		return Preview{}, err
	}
	states, err := readStates(ctx, q, opts.Scope)
	if err != nil {
		return Preview{}, err
	}
	observations := map[string]map[string]scripts.Observation{}
	for _, s := range opts.Scripts {
		observations[s.ID] = observe(ctx, q, s, opts.Functions)
	}
	plan, err := scripts.Build(scripts.PlanOptions{Target: server + "/" + database + "/" + opts.Scope, Phase: phase, Scripts: opts.Scripts, Recorded: states, Observations: observations})
	if err != nil {
		return Preview{}, err
	}
	plan.Hash = scripts.Digest(plan.Hash, bundleHash)
	return Preview{Plan: plan, Database: database, BundleHash: bundleHash}, nil
}
