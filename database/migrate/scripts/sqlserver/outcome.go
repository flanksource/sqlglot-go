package sqlserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"github.com/flanksource/sqlglot-go/sqlglot"
	mssql "github.com/microsoft/go-mssqldb"
)

// setLockTimeout bounds every wait of the session; a script may set its own.
const setLockTimeout = "SET LOCK_TIMEOUT 15000;"

// countState is the RAISERROR state a transaction count check fails with.
const countState = 251

// outcome is what a script's run records: its ledger row and its attempt.
type outcome struct {
	script, planHash, detail string
	state                    scripts.State
}

// statement records o in one request. Its values are literals because a
// parameter would run whatever it is appended to inside sp_executesql, a scope
// temporary tables and SET options do not outlive; its tables are qualified
// because a script may have changed the database.
func (s *session) statement(o outcome) (string, error) {
	encoded, err := json.Marshal(o.state)
	if err != nil {
		return "", err
	}
	if strings.Trim(o.state.Hash, "0123456789abcdef") != "" {
		return "", fmt.Errorf("script %s state hash %q is not hexadecimal", o.script, o.state.Hash)
	}
	schema := s.quotedDatabase() + ".dbo."
	scope, id := literal(s.opts.Scope), literal(o.script)
	return fmt.Sprintf(`MERGE %[1]ssqlglot_migrations AS t USING (SELECT %[2]s AS scope,%[3]s AS path,0x%[4]s AS hash,%[5]s AS state) AS s
ON t.scope=s.scope AND t.path=s.path WHEN MATCHED THEN UPDATE SET hash=s.hash,state=s.state,updated_at=SYSUTCDATETIME()
WHEN NOT MATCHED THEN INSERT(scope,path,hash,state) VALUES(s.scope,s.path,s.hash,s.state);
INSERT INTO %[1]ssqlglot_migration_attempts(scope,path,plan_hash,outcome,detail) VALUES(%[2]s,%[3]s,%[6]s,%[7]s,%[8]s);
%[9]s`, schema, scope, id, o.state.Hash, literal(string(encoded)), literal(o.planHash), literal(o.state.Outcome), literal(o.detail), s.reset()), nil
}

func (s *session) quotedDatabase() string {
	return "[" + strings.ReplaceAll(s.database, "]", "]]") + "]"
}

// reset undoes what a script may have changed of the session for the next one.
func (s *session) reset() string { return setLockTimeout + " USE " + s.quotedDatabase() + ";" }

func literal(value string) string { return "N'" + strings.ReplaceAll(value, "'", "''") + "'" }

// record writes o on the session, outside any script's transaction.
func (s *session) record(ctx context.Context, o outcome) error {
	statement, err := s.statement(o)
	if err != nil {
		return err
	}
	if _, err := s.conn.ExecContext(ctx, statement); err != nil {
		return err
	}
	s.states[o.script] = o.state
	return nil
}

// completion records o in the script's own transaction, once its last batch
// left exactly the migration's transaction open.
func (s *session) completion(o outcome) (string, error) {
	statement, err := s.statement(o)
	if err != nil {
		return "", err
	}
	return countCheck("<>1") + "\nELSE BEGIN\n" + statement + "\nEND;", nil
}

// countCheck fails when @@TRANCOUNT is condition.
func countCheck(condition string) string {
	return fmt.Sprintf("IF @@TRANCOUNT%s RAISERROR(N'script changed the migration transaction count to %%d',16,%d,@@TRANCOUNT)", condition, countState)
}

// countError is err as a failed transaction count check, if it is one: the
// script ended the migration's transaction or left one of its own open.
func countError(err error) error {
	var server mssql.Error
	if !errors.As(err, &server) || server.State != countState || !strings.HasPrefix(server.Message, "script changed the migration transaction count") {
		return err
	}
	return &ExecutionError{Outcome: "commit-unknown", Err: errors.New(server.Message)}
}

// request is batch followed by after in as few requests as its tail allows. A
// module batch only stores its definition, so it needs no check of its own.
func request(batch string, tail sqlglot.BatchTail, after string, last bool) []string {
	switch {
	case tail == sqlglot.TailOpen:
		return []string{batch + "\n;" + after}
	case tail == sqlglot.TailModule && !last:
		return []string{batch}
	}
	return []string{batch, after}
}
