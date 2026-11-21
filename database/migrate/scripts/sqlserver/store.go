package sqlserver

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

func readStates(ctx context.Context, q queryer, ledger ledgerScope) (_ map[string]scripts.State, cause error) {
	rows, err := q.QueryContext(ctx, `SELECT CASE WHEN OBJECT_ID(N'dbo.sqlglot_migrations',N'U') IS NULL THEN 0 ELSE 1 END,
CASE WHEN COL_LENGTH('dbo.sqlglot_migrations','state') IS NULL THEN 0 ELSE 1 END`)
	if err != nil {
		return nil, err
	}
	var exists, extended int
	if !rows.Next() {
		err := rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("inspect script ledger returned no row")
	}
	if err := rows.Scan(&exists, &extended); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	states := map[string]scripts.State{}
	if exists == 0 {
		return states, nil
	}
	query, args := ledger.query(extended == 1)
	rows, err = q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { cause = errors.Join(cause, rows.Close()) }()
	for rows.Next() {
		var id string
		var hash []byte
		var state sql.NullString
		if err := rows.Scan(&id, &hash, &state); err != nil {
			return nil, err
		}
		s := scripts.State{Version: 1, Hash: hex.EncodeToString(hash), SourceHash: hex.EncodeToString(hash)}
		if state.Valid {
			if err := json.Unmarshal([]byte(state.String), &s); err != nil {
				return nil, fmt.Errorf("invalid ledger state for %s: %w", id, err)
			}
			// A version 2 state lacks only EffectsHash, which never selects a
			// script, so it reads as current.
			if (s.Version != scripts.Version && s.Version != 2) || s.Hash != hex.EncodeToString(hash) {
				return nil, fmt.Errorf("inconsistent ledger state for %s", id)
			}
		}
		states[id] = s
	}
	return states, rows.Err()
}

func ensureStore(ctx context.Context, db sessionDB) error {
	_, err := db.ExecContext(ctx, `IF OBJECT_ID(N'dbo.sqlglot_migrations',N'U') IS NULL
CREATE TABLE dbo.sqlglot_migrations(scope nvarchar(256) NOT NULL,path nvarchar(512) NOT NULL,hash varbinary(32) NOT NULL,
updated_at datetime2 NOT NULL DEFAULT SYSUTCDATETIME(),state nvarchar(max) NULL,
CONSTRAINT PK_sqlglot_migrations PRIMARY KEY NONCLUSTERED(scope,path));
IF COL_LENGTH('dbo.sqlglot_migrations','state') IS NULL ALTER TABLE dbo.sqlglot_migrations ADD state nvarchar(max) NULL;
IF OBJECT_ID(N'dbo.sqlglot_migration_attempts',N'U') IS NULL
CREATE TABLE dbo.sqlglot_migration_attempts(id uniqueidentifier NOT NULL DEFAULT NEWID() PRIMARY KEY,scope nvarchar(256) NOT NULL,
path nvarchar(512) NOT NULL,plan_hash varchar(64) NOT NULL,outcome nvarchar(32) NOT NULL,detail nvarchar(max) NULL,
updated_at datetime2 NOT NULL DEFAULT SYSUTCDATETIME());
IF OBJECT_ID(N'dbo.sqlglot_observations',N'U') IS NULL
CREATE TABLE dbo.sqlglot_observations(scope nvarchar(256) NOT NULL,plan_hash varchar(64) NOT NULL,[key] nvarchar(512) NOT NULL,
hash varchar(64) NOT NULL,observed_at datetime2 NOT NULL DEFAULT SYSUTCDATETIME());`)
	return err
}

func recordState(ctx context.Context, db sessionDB, scope, id string, state scripts.State) error {
	hash, err := hex.DecodeString(state.Hash)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `MERGE dbo.sqlglot_migrations AS t USING (SELECT @p1 AS scope,@p2 AS path) AS s
ON t.scope=s.scope AND t.path=s.path WHEN MATCHED THEN UPDATE SET hash=@p3,state=@p4,updated_at=SYSUTCDATETIME()
WHEN NOT MATCHED THEN INSERT(scope,path,hash,state) VALUES(@p1,@p2,@p3,@p4);`, scope, id, hash, string(encoded))
	return err
}
