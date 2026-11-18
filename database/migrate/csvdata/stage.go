package csvdata

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const staging = "[#sqlglot_csv]"
const recordColumn = "__sqlglot_csv_record_id"
const recordID = "[" + recordColumn + "]"

type inputRow struct {
	values map[string]any
	tags   map[string]string
	source string
	delete bool
}

type change struct {
	record int
	action string
	fields []string
}

type tableWork struct {
	meta      metadata
	key       []string
	fields    []string
	rows      []inputRow
	plan      TablePlan
	changes   []change
	snapshot  bool
	hasUpsert bool
}

func prepareTable(ctx context.Context, tx *sql.Tx, name string, files []File, snapshotFiles []string) (tableWork, error) {
	meta, err := tableMetadata(ctx, tx, name)
	if err != nil {
		return tableWork{}, err
	}
	work := tableWork{meta: meta, plan: TablePlan{Table: meta.Name, Phase: files[0].settings.Phase}, snapshot: files[0].settings.Delete == "snapshot"}
	columns := make(map[string]column, len(meta.Columns))
	for _, column := range meta.Columns {
		fold := strings.ToLower(column.Name)
		if _, ok := columns[fold]; ok || fold == recordColumn {
			return work, fmt.Errorf("%s: ambiguous or reserved column %s", name, column.Name)
		}
		columns[fold] = column
	}
	work.key, err = resolveKey(columns, files[0].settings.Key)
	if err != nil {
		return work, err
	}
	if work.snapshot {
		if err := checkSnapshot(files, snapshotFiles); err != nil {
			return work, err
		}
	}
	fieldSet := map[string]bool{}
	for _, file := range files {
		key, err := resolveKey(columns, file.settings.Key)
		if err != nil || !slices.Equal(key, work.key) || file.settings.Phase != work.plan.Phase || (file.settings.Delete == "snapshot") != work.snapshot {
			return work, fmt.Errorf("%s: conflicting key, phase or snapshot settings", file.Path)
		}
		records, err := fileRecords(file, columns, work.key, fieldSet)
		if err != nil {
			return work, fmt.Errorf("%s: %w", file.Path, err)
		}
		work.rows = append(work.rows, records...)
	}
	work.fields = sortedKeys(fieldSet)
	work.plan.SchemaHash = jsonDigest(meta)
	return work, nil
}

func resolveKey(columns map[string]column, configured []string) ([]string, error) {
	if configured == nil {
		var primary []column
		for _, column := range columns {
			if column.Primary > 0 {
				primary = append(primary, column)
			}
		}
		slices.SortFunc(primary, func(a, b column) int { return a.Primary - b.Primary })
		configured = make([]string, 0, len(primary))
		for _, c := range primary {
			configured = append(configured, c.Name)
		}
	}
	if len(configured) == 0 {
		return nil, fmt.Errorf("CSV table requires a key or a primary key")
	}
	key := make([]string, 0, len(configured))
	for _, name := range configured {
		column, ok := columns[strings.ToLower(name)]
		if !ok || column.Generated || contains(key, column.Name) {
			return nil, fmt.Errorf("invalid key column %q", name)
		}
		if contains([]string{"xml", "text", "ntext", "image", "geography", "geometry"}, column.Type) {
			return nil, fmt.Errorf("unsupported key type %s for %s", column.Type, name)
		}
		key = append(key, column.Name)
	}
	return key, nil
}

func checkSnapshot(files []File, expected []string) error {
	var base []string
	for _, file := range files {
		if file.untagged() {
			base = append(base, file.Path)
		}
	}
	slices.Sort(base)
	required := slices.Clone(expected)
	slices.Sort(required)
	if !slices.Equal(base, required) {
		return fmt.Errorf("%s: snapshot requires exactly the declared base files", files[0].Table)
	}
	return nil
}

func fileRecords(file File, columns map[string]column, key []string, fields map[string]bool) ([]inputRow, error) {
	delimiter, _ := utf8.DecodeRuneInString(file.settings.Delimiter)
	records, err := readRecords(file.contents, delimiter)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(records[0]))
	deleteAt := -1
	seen := map[string]bool{}
	for i, value := range records[0] {
		name := strings.ToLower(strings.TrimSpace(value.text))
		if name == "" || seen[name] {
			return nil, fmt.Errorf("empty or duplicate header %q", name)
		}
		seen[name] = true
		if name == "$delete" {
			deleteAt = i
			continue
		}
		if slices.ContainsFunc(file.settings.IgnoreColumns, func(ignored string) bool { return strings.EqualFold(ignored, name) }) {
			continue
		}
		column, ok := columns[name]
		if !ok || column.Generated || (column.Identity && !contains(key, column.Name)) {
			return nil, fmt.Errorf("unknown or unwritable column %s", name)
		}
		names[i] = column.Name
		fields[column.Name] = true
	}
	for _, name := range key {
		if !contains(names, name) {
			return nil, fmt.Errorf("key column %s missing from header", name)
		}
	}
	result := make([]inputRow, 0, len(records)-1)
	for i, record := range records[1:] {
		source := fmt.Sprintf("%s record %d", file.Path, i+2)
		row, err := decodeRow(file.settings, names, key, deleteAt, record, columns)
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", i+2, err)
		}
		row.source, row.tags = source, file.Tags
		result = append(result, row)
	}
	return result, nil
}

func decodeRow(settings Settings, names, key []string, deleteAt int, record []cell, columns map[string]column) (inputRow, error) {
	row := inputRow{values: make(map[string]any)}
	if deleteAt >= 0 {
		switch strings.ToLower(strings.TrimSpace(record[deleteAt].text)) {
		case "", "false":
		case "true":
			if settings.Delete == "disabled" {
				return row, fmt.Errorf("$delete is disabled")
			}
			row.delete = true
		default:
			return row, fmt.Errorf("invalid $delete flag")
		}
	}
	for i, name := range names {
		if name == "" || (row.delete && !contains(key, name)) {
			continue
		}
		policy := settings.Values
		policy.merge(settings.Values.Columns[strings.ToLower(name)])
		value, err := policy.decode(record[i])
		if err != nil {
			return row, fmt.Errorf("%s: %w", name, err)
		}
		if contains(key, name) && (value == nil || value == "") {
			return row, fmt.Errorf("key %s is empty or NULL", name)
		}
		row.values[name], err = columns[strings.ToLower(name)].bind(value)
		if err != nil {
			return row, fmt.Errorf("%s: %w", name, err)
		}
	}
	return row, nil
}

func (w tableWork) stage(ctx context.Context, tx *sql.Tx) error {
	columns := []string{"CAST(0 AS int) AS " + recordID}
	for _, field := range w.fields {
		columns = append(columns, "t."+bracket(field))
	}
	// #nosec G202 -- All variable identifiers are quoted catalog names.
	query := "SELECT TOP(0) " + strings.Join(columns, ",") + " INTO " + staging +
		" FROM (VALUES(0)) AS seed(i) LEFT JOIN " + qualified(w.meta.Name) + " AS t ON 1=0"
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("stage CSV schema for %s: %w", w.meta.Name, err)
	}
	if len(w.rows) == 0 {
		return nil
	}
	return w.bulkStage(ctx, tx)
}

func quoted(names []string) []string {
	result := make([]string, len(names))
	for i, name := range names {
		result[i] = bracket(name)
	}
	return result
}

func parameters(count int) string {
	var result []string
	for i := 1; i <= count; i++ {
		result = append(result, fmt.Sprintf("@p%d", i))
	}
	return strings.Join(result, ",")
}

func keyJoin(key []string) string {
	result := make([]string, len(key))
	for i, name := range key {
		result[i] = "s." + bracket(name) + "=t." + bracket(name)
	}
	return strings.Join(result, " AND ")
}
