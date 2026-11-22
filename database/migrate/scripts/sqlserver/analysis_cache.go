package sqlserver

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

// cachedBodies returns what compute derives from each body, keyed by its
// content-addressed cache key: one MGet over opts.Analysis, then compute and
// Set only for the misses. A failing compute is returned per key and never
// cached.
func cachedBodies[T any](ctx context.Context, opts Options, bodies map[string]string, compute func(sql string) (T, error)) (map[string]T, map[string]error, error) {
	out := make(map[string]T, len(bodies))
	if opts.Analysis != nil && len(bodies) > 0 {
		for entry, err := range opts.Analysis.MGet(ctx, maps.Keys(bodies)) {
			if err != nil {
				return nil, nil, fmt.Errorf("read script analysis cache: %w", err)
			}
			if !entry.Found {
				continue
			}
			var cached T
			if err := json.Unmarshal(entry.Value, &cached); err != nil {
				return nil, nil, fmt.Errorf("decode cached script analysis %s: %w", entry.Key, err)
			}
			out[entry.Key] = cached
		}
	}
	failed := map[string]error{}
	for key, sql := range bodies {
		if _, hit := out[key]; hit {
			continue
		}
		computed, err := compute(sql)
		if err != nil {
			failed[key] = err
			continue
		}
		out[key] = computed
		if opts.Analysis == nil {
			continue
		}
		value, err := json.Marshal(computed)
		if err != nil {
			return nil, nil, err
		}
		if err := opts.Analysis.Set(ctx, key, value, opts.AnalysisTTL); err != nil {
			return nil, nil, fmt.Errorf("write script analysis cache %s: %w", key, err)
		}
	}
	return out, failed, nil
}

func analyseScript(sql string) (scripts.CachedAnalysis, error) {
	batches, problem := splitBatches(sql)
	if problem != "" {
		return scripts.CachedAnalysis{BatchError: problem}, nil
	}
	analysis := scripts.Analyze(scripts.AnalysisOptions{SQL: sql})
	for _, batch := range batches {
		tail, err := sqlglot.BatchTailOf(batch)
		if err != nil {
			return scripts.CachedAnalysis{}, err
		}
		analysis.Tails = append(analysis.Tails, tail)
	}
	return scripts.CachedAnalysis{Analysis: analysis}, nil
}

func analyseModule(definition string) (scripts.CachedAnalysis, error) {
	canonical, err := sqlglot.CanonicalSQL(definition, "tsql")
	if err != nil {
		return scripts.CachedAnalysis{}, err
	}
	return scripts.CachedAnalysis{Canonical: canonical, Analysis: scripts.Analyze(scripts.AnalysisOptions{SQL: definition, InvokedModule: true})}, nil
}

// scriptAnalyses analyses every script body once per distinct content and
// fails on the first script, in input order, whose body does not split.
func (opts Options) scriptAnalyses(ctx context.Context) (map[string]scripts.Analysis, error) {
	bodies := make(map[string]string, len(opts.Scripts))
	for _, s := range opts.Scripts {
		bodies[scripts.AnalysisKey(s.SQL, false)] = s.SQL
	}
	cached, failed, err := cachedBodies(ctx, opts, bodies, analyseScript)
	if err != nil {
		return nil, err
	}
	out := make(map[string]scripts.Analysis, len(opts.Scripts))
	for _, s := range opts.Scripts {
		if len(s.ID) > 512 {
			return nil, fmt.Errorf("script ID exceeds 512 characters")
		}
		key := scripts.AnalysisKey(s.SQL, false)
		if err := failed[key]; err != nil {
			return nil, fmt.Errorf("analyse script %s: %w", s.ID, err)
		}
		entry := cached[key]
		if entry.BatchError != "" {
			return nil, batchError(s.ID, entry.BatchError)
		}
		out[s.ID] = entry.Analysis
	}
	return out, nil
}
