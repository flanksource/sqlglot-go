package csvdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	mssql "github.com/microsoft/go-mssqldb"
)

const bulkTable = "#sqlglot_csv_raw"
const bulkStaging = "[" + bulkTable + "]"

func (w tableWork) bulkStage(ctx context.Context, tx *sql.Tx) error {
	// Convert on SQL Server to preserve its date, numeric and collation semantics.
	types := make(map[string]string, len(w.meta.Columns))
	for _, column := range w.meta.Columns {
		types[column.Name] = column.Type
	}
	definitions := []string{recordID + " int NOT NULL"}
	for _, field := range w.fields {
		raw := "nvarchar(max)"
		switch types[field] {
		case "binary", "varbinary", "image":
			raw = "varbinary(max)"
		case "bit":
			raw = "bit"
		}
		definitions = append(definitions, bracket(field)+" "+raw+" NULL")
	}
	// #nosec G202 -- The definitions use quoted catalog names and fixed SQL types.
	query := "CREATE TABLE " + bulkStaging + " (" + strings.Join(definitions, ",") + ")"
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("%s: create CSV bulk staging: %s", w.meta.Name, databaseError(err))
	}
	if err := w.copyRows(ctx, tx); err != nil {
		return err
	}
	columns := strings.Join(append([]string{recordID}, quoted(w.fields)...), ",")
	// #nosec G202 -- Columns are quoted catalog names and values stay in SQL Server.
	result, err := tx.ExecContext(ctx, "INSERT INTO "+staging+" ("+columns+") SELECT "+columns+" FROM "+bulkStaging)
	if err != nil {
		return fmt.Errorf("%s: convert staged CSV values: %s", w.meta.Name, databaseError(err))
	}
	if err := checkStageCount(result, len(w.rows)); err != nil {
		return fmt.Errorf("%s: %w", w.meta.Name, err)
	}
	_, err = tx.ExecContext(ctx, "DROP TABLE "+bulkStaging)
	return err
}

func (w tableWork) copyRows(ctx context.Context, tx *sql.Tx) (returnErr error) {
	columns := append([]string{recordColumn}, w.fields...)
	stmt, err := tx.PrepareContext(ctx, mssql.CopyIn(bulkTable, mssql.BulkOptions{KeepNulls: true, Tablock: true, RowsPerBatch: len(w.rows)}, columns...))
	if err != nil {
		return fmt.Errorf("%s: prepare CSV bulk copy: %s", w.meta.Name, databaseError(err))
	}
	defer func() { returnErr = errors.Join(returnErr, stmt.Close()) }()
	values := make([]any, len(columns))
	for i, row := range w.rows {
		values[0] = i + 1
		for j, field := range w.fields {
			values[j+1] = row.values[field]
		}
		if _, err := stmt.ExecContext(ctx, values...); err != nil {
			// Finish the in-flight bulk stream before the caller rolls back.
			_, finishErr := stmt.ExecContext(context.WithoutCancel(ctx))
			if finishErr != nil {
				return fmt.Errorf("%s: CSV bulk copy: %s; finish stream: %s", row.source, databaseError(err), databaseError(finishErr))
			}
			return fmt.Errorf("%s: CSV bulk copy: %s", row.source, databaseError(err))
		}
	}
	// The driver keeps the PrepareContext context for the stream. Run its finish
	// call even if the request is cancelled after the last row was buffered.
	result, err := stmt.ExecContext(context.WithoutCancel(ctx))
	if err != nil {
		return fmt.Errorf("%s: finish CSV bulk copy: %s", w.meta.Name, databaseError(err))
	}
	if err := checkStageCount(result, len(w.rows)); err != nil {
		return fmt.Errorf("%s: %w", w.meta.Name, err)
	}
	return nil
}

func checkStageCount(result sql.Result, expected int) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read staged CSV row count: %w", err)
	}
	if count != int64(expected) {
		return fmt.Errorf("staged %d CSV rows, expected %d", count, expected)
	}
	return nil
}
