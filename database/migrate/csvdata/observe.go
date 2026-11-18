package csvdata

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

func (w *tableWork) observe(ctx context.Context, tx *sql.Tx) (returnErr error) {
	if w.snapshot {
		if err := w.stageOmissions(ctx, tx); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, w.compareSQL())
	if err != nil {
		return fmt.Errorf("compare CSV data for %s: %w", w.meta.Name, err)
	}
	defer closeRows(rows, &returnErr)
	var group []rowState
	var observed, deleted []string
	for rows.Next() {
		state, err := w.scanState(rows)
		if err != nil {
			return err
		}
		if len(group) > 0 && group[0].rank != state.rank {
			if err := w.recordGroup(group, &observed, &deleted); err != nil {
				return err
			}
			group = group[:0]
		}
		group = append(group, state)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(group) > 0 {
		if err := w.recordGroup(group, &observed, &deleted); err != nil {
			return err
		}
	}
	if w.snapshot && !w.hasUpsert {
		return fmt.Errorf("%s: snapshot requires an effective upsert", w.meta.Name)
	}
	w.plan.StateHash = jsonDigest(observed)
	if len(deleted) > 0 {
		w.plan.DeleteHash = jsonDigest(deleted)
	}
	return w.checkEffects()
}

type rowState struct {
	record   int
	rank     int64
	found    bool
	desired  map[string]any
	existing map[string]any
}

func (w tableWork) compareSQL() string {
	order := strings.Join(quoted(w.key), ",")
	selects := []string{"s." + recordID, "s.[__csv_rank]", "CAST(CASE WHEN t." + bracket(w.key[0]) + " IS NULL THEN 0 ELSE 1 END AS bit)"}
	for _, field := range w.fields {
		selects = append(selects, "s."+bracket(field), "t."+bracket(field))
	}
	return "WITH staged AS (SELECT *,DENSE_RANK() OVER (ORDER BY " + order + ") AS [__csv_rank] FROM " + staging +
		") SELECT " + strings.Join(selects, ",") + " FROM staged AS s LEFT JOIN " + qualified(w.meta.Name) +
		" AS t ON " + keyJoin(w.key) + " ORDER BY s.[__csv_rank],s." + recordID
}

func (w tableWork) scanState(rows *sql.Rows) (rowState, error) {
	state := rowState{desired: map[string]any{}, existing: map[string]any{}}
	destination := []any{&state.record, &state.rank, &state.found}
	values := make([]any, len(w.fields)*2)
	for i := range values {
		destination = append(destination, &values[i])
	}
	if err := rows.Scan(destination...); err != nil {
		return state, err
	}
	for i, field := range w.fields {
		state.desired[field], state.existing[field] = normalized(values[i*2]), normalized(values[i*2+1])
	}
	return state, nil
}

func normalized(value any) any {
	if raw, ok := value.([]byte); ok {
		return slices.Clone(raw)
	}
	return value
}

func (w *tableWork) recordGroup(group []rowState, observed, deleted *[]string) error {
	if group[0].record < 0 {
		if len(group) != 1 {
			return fmt.Errorf("%s: snapshot key matches multiple target rows", w.meta.Name)
		}
		w.plan.SnapshotDeletes++
		w.changes = append(w.changes, change{record: group[0].record, action: "delete"})
		*deleted = append(*deleted, jsonDigest(group[0].existing))
		return nil
	}
	seen := map[string]bool{}
	for _, state := range group {
		if seen[w.rows[state.record-1].source] {
			return fmt.Errorf("%s: key matches multiple target rows", w.rows[state.record-1].source)
		}
		seen[w.rows[state.record-1].source] = true
	}
	winner, err := w.winner(group)
	if err != nil {
		return err
	}
	row := w.rows[winner.record-1]
	*observed = append(*observed, jsonDigest([]any{winner.record, winner.found, winner.existing}))
	if row.delete {
		if !winner.found {
			w.plan.Unchanged++
			return nil
		}
		w.plan.ExplicitDeletes++
		w.changes = append(w.changes, change{record: winner.record, action: "delete"})
		*deleted = append(*deleted, jsonDigest(winner.existing))
		return nil
	}
	w.hasUpsert = true
	return w.recordUpsert(winner, row)
}

func (w *tableWork) recordUpsert(state rowState, row inputRow) error {
	var fields []string
	for _, col := range w.meta.Columns {
		_, supplied := row.values[col.Name]
		if supplied && state.desired[col.Name] == nil && !col.Nullable {
			return fmt.Errorf("%s: %s requires a non-NULL value", row.source, col.Name)
		}
		if !state.found && !supplied && !col.Nullable && col.Default == "" && !col.Identity && !col.Generated {
			return fmt.Errorf("%s: missing required column %s", row.source, col.Name)
		}
		if !supplied || (state.found && contains(w.key, col.Name)) {
			continue
		}
		if col.Identity {
			return fmt.Errorf("%s: cannot insert an explicit identity value for %s", row.source, col.Name)
		}
		if !state.found || !reflect.DeepEqual(state.desired[col.Name], state.existing[col.Name]) {
			fields = append(fields, col.Name)
		}
	}
	if !state.found {
		w.plan.Inserts++
		w.changes = append(w.changes, change{record: state.record, action: "insert", fields: fields})
	} else if len(fields) > 0 {
		w.plan.Updates++
		w.changes = append(w.changes, change{record: state.record, action: "update", fields: fields})
	} else {
		w.plan.Unchanged++
	}
	return nil
}

func (w tableWork) winner(group []rowState) (rowState, error) {
	var possible []rowState
	for _, state := range group {
		row := w.rows[state.record-1]
		dominated := false
		for _, other := range group {
			if state.record == other.record {
				continue
			}
			competing := w.rows[other.record-1]
			if reflect.DeepEqual(competing.tags, row.tags) {
				return state, fmt.Errorf("duplicate CSV key in %s and %s", row.source, competing.source)
			}
			dominated = dominated || supersedes(competing.tags, row.tags)
		}
		if !dominated {
			possible = append(possible, state)
		}
	}
	if len(possible) != 1 {
		return rowState{}, fmt.Errorf("conflicting CSV layers in %s and %s", w.rows[possible[0].record-1].source, w.rows[possible[1].record-1].source)
	}
	return possible[0], nil
}

func supersedes(a, b map[string]string) bool {
	additional := false
	for axis, value := range b {
		if value != "none" && a[axis] != value {
			return false
		}
		additional = additional || (value == "none" && a[axis] != "none")
	}
	return additional
}

func (w tableWork) stageOmissions(ctx context.Context, tx *sql.Tx) error {
	fields := quoted(w.key)
	columns := make([]string, len(fields))
	for i, field := range fields {
		columns[i] = "t." + field
	}
	// NULL keys cannot represent a complete snapshot identity.
	nulls := make([]string, len(columns))
	for i, field := range columns {
		nulls[i] = field + " IS NULL"
	}
	var invalid bool
	query := "SELECT CAST(CASE WHEN EXISTS (SELECT 1 FROM " + qualified(w.meta.Name) + " AS t WHERE " + strings.Join(nulls, " OR ") + ") THEN 1 ELSE 0 END AS bit)"
	if err := tx.QueryRowContext(ctx, query).Scan(&invalid); err != nil {
		return err
	}
	if invalid {
		return fmt.Errorf("%s: snapshot contains target NULL keys", w.meta.Name)
	}
	// #nosec G202 -- The dynamic SQL contains only quoted catalog identifiers.
	_, err := tx.ExecContext(ctx, "INSERT INTO "+staging+" ("+recordID+","+strings.Join(fields, ",")+") SELECT -ROW_NUMBER() OVER (ORDER BY "+
		strings.Join(columns, ",")+"),"+strings.Join(columns, ",")+" FROM "+qualified(w.meta.Name)+" AS t WHERE NOT EXISTS (SELECT 1 FROM "+staging+" AS s WHERE "+keyJoin(w.key)+")")
	return err
}

func (w tableWork) checkEffects() error {
	if len(w.changes) > 0 && len(w.meta.Triggers) > 0 {
		return fmt.Errorf("%s: enabled triggers could cause unreviewed writes", w.meta.Name)
	}
	for _, relation := range w.meta.Relations {
		if relation.Parent != w.meta.Name {
			continue
		}
		if relation.DeleteAction != 0 && w.plan.ExplicitDeletes+w.plan.SnapshotDeletes > 0 {
			return fmt.Errorf("%s: foreign key %s could cause unreviewed deletes or updates", w.meta.Name, relation.Name)
		}
		if relation.UpdateAction != 0 && w.plan.Updates > 0 {
			return fmt.Errorf("%s: foreign key %s could cause unreviewed updates", w.meta.Name, relation.Name)
		}
	}
	return nil
}
