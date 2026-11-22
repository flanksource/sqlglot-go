package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

const (
	// readBatch bounds the reads one round trip answers.
	readBatch = 200
	// readValueLimit bounds the value a one-value answer is recorded with.
	readValueLimit = 200

	readMarker = "sqlglot_read"
	readError  = "sqlglot_error"
)

// comparableReads are the reads whose answer the ledger recorded with the
// script's present source: only those can select it.
func (opts Options) comparableReads(states map[string]scripts.State) []string {
	queries := map[string]bool{}
	for _, s := range opts.Scripts {
		recorded := states[s.ID]
		if recorded.SourceHash != scripts.SourceHash(s.SQL) {
			continue
		}
		for _, read := range opts.analysis[s.ID].Reads {
			if _, ok := recorded.Reads[read.Key()]; ok {
				queries[read.SQL] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(queries))
}

// answerReads runs each query on its own and answers it by its text. A query
// SQL Server refuses answers its error; a failed connection is an error.
func answerReads(ctx context.Context, q queryer, queries []string) (map[string]scripts.ReadAnswer, error) {
	answers := make(map[string]scripts.ReadAnswer, len(queries))
	for chunk := range slices.Chunk(queries, readBatch) {
		if err := answerBatch(ctx, q, chunk, answers); err != nil {
			return nil, fmt.Errorf("answer what the scripts read: %w", err)
		}
	}
	return answers, nil
}

func answerBatch(ctx context.Context, q queryer, queries []string, answers map[string]scripts.ReadAnswer) (cause error) {
	var batch strings.Builder
	batch.WriteString("SET NOCOUNT ON;\n")
	for i, query := range queries {
		fmt.Fprintf(&batch, "SELECT %d AS %s;\nBEGIN TRY EXEC sys.sp_executesql N'%s' END TRY BEGIN CATCH SELECT ERROR_MESSAGE() AS %s END CATCH;\n",
			i, readMarker, strings.ReplaceAll(query, "'", "''"), readError)
	}
	rows, err := q.QueryContext(ctx, batch.String())
	if err != nil {
		return err
	}
	defer func() { cause = errors.Join(cause, rows.Close()) }()
	current := -1
	for more := true; more; more = rows.NextResultSet() {
		columns, err := rows.Columns()
		if err != nil {
			return err
		}
		if slices.Equal(columns, []string{readMarker}) {
			// The driver finds the next result set only once this one is drained.
			for rows.Next() {
				if err := rows.Scan(&current); err != nil {
					return err
				}
			}
			if err := rows.Err(); err != nil {
				return err
			}
			continue
		}
		if current < 0 || current >= len(queries) {
			return fmt.Errorf("a result set belongs to no read")
		}
		if len(columns) == 0 {
			continue
		}
		answer, err := readAnswer(rows, columns)
		if err != nil {
			return err
		}
		answers[queries[current]] = answer
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i, query := range queries {
		if _, ok := answers[query]; !ok {
			return fmt.Errorf("read %d answered no result set", i)
		}
	}
	return nil
}

// readAnswer is the rows of one result set, in any order.
func readAnswer(rows *sql.Rows, columns []string) (scripts.ReadAnswer, error) {
	values, targets := make([]any, len(columns)), make([]any, len(columns))
	for i := range values {
		targets[i] = &values[i]
	}
	var answer scripts.ReadAnswer
	var digests []string
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return answer, err
		}
		fields := make([]string, len(values))
		for i, value := range values {
			fields[i] = fmt.Sprintf("%T:%v", value, value)
		}
		digests = append(digests, scripts.Digest(fields...))
	}
	if err := rows.Err(); err != nil {
		return answer, err
	}
	if slices.Equal(columns, []string{readError}) {
		return scripts.ReadAnswer{Error: scalarText(values[0])}, nil
	}
	answer.Rows = len(digests)
	if len(digests) == 1 && len(columns) == 1 {
		text := scalarText(values[0])
		answer.Value = &text
	}
	slices.Sort(digests)
	answer.Digest = scripts.Digest(scripts.Digest(columns...), scripts.Digest(digests...))
	return answer, nil
}

func scalarText(value any) string {
	text := fmt.Sprint(value)
	if bytes, ok := value.([]byte); ok {
		text = string(bytes)
	}
	if value == nil {
		text = "NULL"
	}
	if runes := []rune(text); len(runes) > readValueLimit {
		text = string(runes[:readValueLimit]) + "…"
	}
	return text
}
