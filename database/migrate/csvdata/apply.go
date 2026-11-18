package csvdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	nativesql "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	mssql "github.com/microsoft/go-mssqldb"
)

type Options struct {
	Files   fs.FS
	Schema  string
	Filters map[string]string
	Phase   string
}

type ApplyOptions struct {
	Options
	Artifact    Artifact
	Hash        string
	Acks        []string
	LockTimeout time.Duration
}

func Plan(ctx context.Context, db *sql.DB, options Options) (_ Artifact, returnErr error) {
	input, err := loadOptions(options)
	if err != nil {
		return Artifact{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Artifact{}, err
	}
	defer rollback(tx, &returnErr)
	artifact, _, err := inspect(ctx, tx, input, options)
	if err != nil {
		return Artifact{}, err
	}
	return artifact, tx.Rollback()
}

func loadOptions(options Options) (Bundle, error) {
	if options.Files == nil {
		return Bundle{}, fmt.Errorf("CSV migrations require captured source files")
	}
	if options.Phase != "" && !contains([]string{"pre-rules", "post-rules"}, options.Phase) {
		return Bundle{}, fmt.Errorf("unknown CSV phase %q", options.Phase)
	}
	return Load(options.Files, options.Schema, options.Filters)
}

func inspect(ctx context.Context, tx *sql.Tx, input Bundle, options Options) (Artifact, []tableWork, error) {
	artifact := Artifact{Kind: ArtifactKind, Version: 1, Schema: options.Schema, Phase: options.Phase, Source: input.Hash,
		Unmatched: input.Unmatched, Filters: input.Filters, Files: []File{}, Tables: []TablePlan{}}
	var target string
	if err := tx.QueryRowContext(ctx, `SELECT DB_NAME(),CONCAT(CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')),N'/',DB_ID(),N'/',d.create_date)
FROM sys.databases d WHERE d.database_id=DB_ID()`).Scan(&artifact.Database, &target); err != nil {
		return artifact, nil, err
	}
	artifact.Target = digest([]byte(target))
	byTable := map[string][]File{}
	phases := map[string]string{}
	for _, file := range input.Files {
		if previous, ok := phases[file.Table]; ok && previous != file.settings.Phase {
			return artifact, nil, fmt.Errorf("%s: all sources must agree on phase", file.Table)
		}
		phases[file.Table] = file.settings.Phase
		if options.Phase != "" && options.Phase != file.settings.Phase {
			continue
		}
		public := file
		public.contents = nil
		artifact.Files = append(artifact.Files, public)
		byTable[file.Table] = append(byTable[file.Table], file)
	}
	var works []tableWork
	physicalTables := map[int]bool{}
	for _, name := range sortedKeys(byTable) {
		work, err := prepareTable(ctx, tx, name, byTable[name], input.snapshots[name])
		if err != nil {
			return artifact, nil, err
		}
		if physicalTables[work.meta.Object] {
			return artifact, nil, fmt.Errorf("%s: multiple routes target the same SQL Server table", name)
		}
		physicalTables[work.meta.Object] = true
		if err := work.stage(ctx, tx); err != nil {
			return artifact, nil, err
		}
		if err := work.observe(ctx, tx); err != nil {
			return artifact, nil, err
		}
		if _, err := tx.ExecContext(ctx, "DROP TABLE "+staging); err != nil {
			return artifact, nil, err
		}
		works = append(works, work)
		artifact.Tables = append(artifact.Tables, work.plan)
	}
	artifact.Hash = artifact.fingerprint()
	return artifact, works, nil
}

func Apply(ctx context.Context, db *sql.DB, request ApplyOptions) ([]TablePlan, error) {
	if err := request.Artifact.Validate(request.Hash, request.Acks); err != nil {
		return nil, err
	}
	if request.Schema != request.Artifact.Schema || request.Phase != request.Artifact.Phase {
		return nil, fmt.Errorf("CSV artifact scope does not match request")
	}
	if request.LockTimeout < 0 || request.LockTimeout > math.MaxInt32*time.Millisecond {
		return nil, fmt.Errorf("invalid CSV lock timeout")
	}
	input, err := loadOptions(request.Options)
	if err != nil {
		return nil, err
	}
	if input.Hash != request.Artifact.Source || !reflect.DeepEqual(input.Filters, request.Artifact.Filters) {
		return nil, fmt.Errorf("CSV inputs or filters changed; generate a fresh preview")
	}
	var results []TablePlan
	for _, phase := range []string{"pre-rules", "post-rules"} {
		if request.Phase != "" && request.Phase != phase {
			continue
		}
		if err := applyPhase(ctx, db, request, input, phase); err != nil {
			return results, err
		}
		for _, table := range request.Artifact.Tables {
			if table.Phase == phase {
				results = append(results, table)
			}
		}
	}
	return results, nil
}

func applyPhase(ctx context.Context, db *sql.DB, request ApplyOptions, input Bundle, phase string) (returnErr error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer rollback(tx, &returnErr)
	var status int
	err = tx.QueryRowContext(ctx, `DECLARE @status int;
EXEC @status=sys.sp_getapplock @Resource=@p1,@LockMode='Exclusive',@LockOwner='Transaction',@LockTimeout=@p2;
SELECT @status`, nativesql.LockResource, request.LockTimeout.Milliseconds()).Scan(&status)
	if err != nil {
		return err
	}
	if status < 0 {
		return fmt.Errorf("CSV migration lock returned %d", status)
	}
	options := request.Options
	options.Phase = phase
	actual, works, err := inspect(ctx, tx, input, options)
	if err != nil {
		return err
	}
	expected := []TablePlan{}
	for _, table := range request.Artifact.Tables {
		if table.Phase == phase {
			expected = append(expected, table)
		}
	}
	if actual.Target != request.Artifact.Target || !slices.Equal(actual.Tables, expected) {
		return fmt.Errorf("CSV target, schema, or row state changed in %s; generate a fresh preview", phase)
	}
	if err := executeWork(ctx, tx, works); err != nil {
		return err
	}
	return tx.Commit()
}

func rollback(tx *sql.Tx, cause *error) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		*cause = errors.Join(*cause, err)
	}
}

func executeWork(ctx context.Context, tx *sql.Tx, works []tableWork) error {
	writes, err := workOrder(works, false)
	if err != nil {
		return err
	}
	deletes, err := workOrder(works, true)
	if err != nil {
		return err
	}
	for _, work := range writes {
		if err := work.execute(ctx, tx, false); err != nil {
			return err
		}
	}
	for _, work := range deletes {
		if err := work.execute(ctx, tx, true); err != nil {
			return err
		}
	}
	return nil
}

func workOrder(works []tableWork, deletes bool) ([]tableWork, error) {
	tables := map[string]*tableWork{}
	for i := range works {
		w := &works[i]
		if deletes && w.plan.ExplicitDeletes+w.plan.SnapshotDeletes > 0 || !deletes && w.plan.Inserts+w.plan.Updates > 0 {
			tables[w.meta.Name] = w
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var order []tableWork
	var visit func(string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("CSV foreign key cycle involving %s", name)
		}
		visiting[name] = true
		work := tables[name]
		for _, relation := range work.meta.Relations {
			if !deletes && relation.Child == name && relation.Parent != name && tables[relation.Parent] != nil {
				if err := visit(relation.Parent); err != nil {
					return err
				}
			}
			if deletes && relation.Parent == name && relation.Child != name && tables[relation.Child] != nil {
				if err := visit(relation.Child); err != nil {
					return err
				}
			}
		}
		visiting[name], visited[name] = false, true
		order = append(order, *work)
		return nil
	}
	for _, name := range sortedKeys(tables) {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func (w tableWork) execute(ctx context.Context, tx *sql.Tx, deleting bool) error {
	if err := w.stage(ctx, tx); err != nil {
		return err
	}
	if deleting && w.snapshot {
		if err := w.stageOmissions(ctx, tx); err != nil {
			return err
		}
	}
	groups := map[string][]change{}
	for _, change := range w.changes {
		if (change.action == "delete") != deleting {
			continue
		}
		group := change.action + "\x00" + strings.Join(change.fields, "\x00")
		groups[group] = append(groups[group], change)
	}
	for _, group := range sortedKeys(groups) {
		for batch := range slices.Chunk(groups[group], 1000) {
			if err := w.executeBatch(ctx, tx, batch); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, "DROP TABLE "+staging)
	return err
}

func (w tableWork) executeBatch(ctx context.Context, tx *sql.Tx, batch []change) error {
	first := batch[0]
	table := qualified(w.meta.Name)
	fields := quoted(first.fields)
	where := " WHERE s." + recordID + " IN (" + parameters(len(batch)) + ")"
	join := " FROM " + table + " AS t JOIN " + staging + " AS s ON " + keyJoin(w.key)
	var query string
	switch first.action {
	case "delete":
		query = "DELETE t" + join + where
	case "update":
		assign := make([]string, len(fields))
		for i, field := range fields {
			assign[i] = "t." + field + "=s." + field
		}
		query = "UPDATE t SET " + strings.Join(assign, ",") + join + where
	case "insert":
		query = "INSERT INTO " + table + " (" + strings.Join(fields, ",") + ") SELECT " + strings.Join(fields, ",") + " FROM " + staging + " AS s" + where
	default:
		return fmt.Errorf("unknown CSV action %q", first.action)
	}
	args := make([]any, len(batch))
	for i, change := range batch {
		args[i] = change.record
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s: CSV %s rejected by SQL Server: %s", w.meta.Name, first.action, databaseError(err))
	}
	count, err := result.RowsAffected()
	if err != nil || count != int64(len(batch)) {
		return fmt.Errorf("%s: CSV %s affected %d rows, expected %d", w.meta.Name, first.action, count, len(batch))
	}
	return nil
}

func databaseError(err error) string {
	var dbErr mssql.Error
	if errors.As(err, &dbErr) {
		return fmt.Sprintf("SQL Server error %d", dbErr.Number)
	}
	return fmt.Sprintf("%T", err)
}
