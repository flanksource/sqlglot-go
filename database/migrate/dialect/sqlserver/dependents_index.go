package sqlserver

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// indexHeader is one index found on a requested column, as the catalog holds it.
type indexHeader struct {
	ord                    int
	table                  migrate.Name
	objectID, indexID      int
	name, typeDesc         string
	typ                    int
	unique, primary        bool
	uniqueConstraint       bool
	disabled, hypothetical bool
	fillFactor             int
	padded, ignoreDupKey   bool
	rowLocks, pageLocks    bool
	filter                 string
	spaceType, spaceName   string
	defaultSpace           bool
	compLeast, compMost    string
	partitions             int
	noRecompute            bool
	fullTextKey            bool
}

func scanIndexHeader(rows *sql.Rows) (indexHeader, error) {
	var h indexHeader
	err := rows.Scan(&h.ord, &h.table.Schema, &h.table.Name, &h.objectID, &h.indexID, &h.name, &h.typ, &h.typeDesc,
		&h.unique, &h.primary, &h.uniqueConstraint, &h.disabled, &h.hypothetical, &h.fillFactor,
		&h.padded, &h.ignoreDupKey, &h.rowLocks, &h.pageLocks, &h.filter,
		&h.spaceType, &h.spaceName, &h.defaultSpace, &h.compLeast, &h.compMost, &h.partitions, &h.noRecompute, &h.fullTextKey)
	return h, err
}

type indexKey struct{ objectID, indexID int }

type indexPart struct {
	column               string
	included, descending bool
}

func (r *reader) indexDependents(ctx context.Context) ([]engine.Dependent, error) {
	headers, err := collectRows(ctx, r.q, r.cte+indexHeadersQuery, r.args, scanIndexHeader)
	if err != nil {
		return nil, fmt.Errorf("read indexes: %w", err)
	}
	if len(headers) == 0 {
		return nil, nil
	}
	parts, err := r.indexParts(ctx, headers)
	if err != nil {
		return nil, err
	}
	out := make([]engine.Dependent, len(headers))
	for i, h := range headers {
		out[i] = r.indexDependent(h, parts[indexKey{h.objectID, h.indexID}])
	}
	return out, nil
}

func (r *reader) indexParts(ctx context.Context, headers []indexHeader) (map[indexKey][]indexPart, error) {
	var pairs [][2]int
	seen := map[indexKey]bool{}
	for _, h := range headers {
		if k := (indexKey{h.objectID, h.indexID}); !seen[k] {
			seen[k] = true
			pairs = append(pairs, [2]int{h.objectID, h.indexID})
		}
	}
	parts := map[indexKey][]indexPart{}
	for len(pairs) > 0 {
		n := min(len(pairs), maxPairsPerQuery)
		rows, err := collectRows(ctx, r.q, fmt.Sprintf(indexColumnsQuery, idPairs(pairs[:n])), nil, func(rows *sql.Rows) (struct {
			key  indexKey
			part indexPart
		}, error) {
			var row struct {
				key  indexKey
				part indexPart
			}
			err := rows.Scan(&row.key.objectID, &row.key.indexID, &row.part.column, &row.part.included, &row.part.descending)
			return row, err
		})
		if err != nil {
			return nil, fmt.Errorf("read index columns: %w", err)
		}
		for _, row := range rows {
			parts[row.key] = append(parts[row.key], row.part)
		}
		pairs = pairs[n:]
	}
	return parts, nil
}

// indexDependent builds the dependent for an index, primary key or unique
// constraint with every option a re-creation needs, or says why it cannot be.
func (r *reader) indexDependent(h indexHeader, parts []indexPart) engine.Dependent {
	idx := migrate.Index{Name: h.name, Unique: h.unique, Primary: h.primary, Clustered: h.typ == 1,
		Constraint: h.uniqueConstraint, Filter: normalizeExpression(h.filter), FillFactor: normalizeFillFactor(h.fillFactor),
		PadIndex: h.padded, IgnoreDupKey: h.ignoreDupKey, NoRowLocks: !h.rowLocks, NoPageLocks: !h.pageLocks, NoRecompute: h.noRecompute}
	for _, p := range parts {
		switch {
		case p.included:
			idx.Included = append(idx.Included, p.column)
		default:
			idx.Columns = append(idx.Columns, migrate.IndexColumn{Name: p.column, Descending: p.descending})
		}
	}
	idx.Included = sortedColumns(idx.Included)
	var reasons []string
	var reason string
	idx.Compression, reason = compressionOf(h.compLeast, h.compMost, h.partitions)
	reasons = append(reasons, reason)
	idx.Filegroup, reason = filegroupOf(h.spaceType, h.spaceName, h.defaultSpace)
	reasons = append(reasons, reason)
	reasons = append(reasons, indexStateReasons(h)...)
	kind := engine.DependentIndex
	if h.primary {
		kind = engine.DependentPrimaryKey
	}
	return engine.Dependent{Kind: kind, Table: h.table, Name: h.name, On: []engine.ColumnRef{r.requested[h.ord]}, Index: &idx,
		Refusal: refusalText("index "+h.name+" on "+qualified(h.table), reasons)}
}

// indexStateReasons names what stops an index from being re-created: it is not
// a plain clustered or nonclustered rowstore index, or it is not in use.
func indexStateReasons(h indexHeader) []string {
	var reasons []string
	if h.disabled {
		reasons = append(reasons, "is disabled")
	}
	if h.hypothetical {
		reasons = append(reasons, "is hypothetical")
	}
	if h.typ != 1 && h.typ != 2 {
		reasons = append(reasons, "is a "+h.typeDesc+" index")
	}
	if h.fullTextKey {
		reasons = append(reasons, "is the key index of a full-text index")
	}
	return reasons
}

// refusalText joins the reasons an object cannot be reproduced, naming the
// object; it is empty when there are none.
func refusalText(subject string, reasons []string) string {
	var kept []string
	for _, r := range reasons {
		if r != "" {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return subject + " " + strings.Join(kept, " and ")
}
