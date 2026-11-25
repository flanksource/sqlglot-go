package sqlserver

import (
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

// snapshotBatch bounds the rows one snapshot INSERT binds (two parameters
// each), far below SQL Server's 2100-parameter limit.
const snapshotBatch = 500

// observationSnapshot is the record of every script input the plan file
// observed, replaced at the start of each apply and updated when apply
// re-observes an input after its upstream writer.
type observationSnapshot struct {
	scope, planHash string
	values          map[string]string
}

func newSnapshot(scope string, file PlanFile) observationSnapshot {
	return observationSnapshot{scope: scope, planHash: file.Hash, values: maps.Clone(file.Observations)}
}

// write replaces the scope's snapshot with this plan's.
func (o observationSnapshot) write(ctx context.Context, db sessionDB) error {
	keys := slices.Sorted(maps.Keys(o.values))
	statement := `DELETE FROM dbo.sqlglot_observations WHERE scope=@p1;`
	if len(keys) == 0 {
		_, err := db.ExecContext(ctx, statement, o.scope)
		return err
	}
	for chunk := range slices.Chunk(keys, snapshotBatch) {
		args := []any{o.scope, o.planHash}
		rows := make([]string, len(chunk))
		for i, key := range chunk {
			if len(key) > 512 {
				return fmt.Errorf("observation key exceeds 512 characters: %s", key)
			}
			rows[i] = fmt.Sprintf("(@p1,@p2,@p%d,@p%d)", len(args)+1, len(args)+2)
			args = append(args, key, o.values[key])
		}
		statement += `INSERT INTO dbo.sqlglot_observations(scope,plan_hash,[key],hash) VALUES` + strings.Join(rows, ",")
		if _, err := db.ExecContext(ctx, statement, args...); err != nil {
			return fmt.Errorf("record plan observations: %w", err)
		}
		statement = ""
	}
	return nil
}

// update records re-observed inputs.
func (o observationSnapshot) update(ctx context.Context, db sessionDB, observed map[string]scripts.Observation) error {
	for _, key := range slices.Sorted(maps.Keys(observed)) {
		o.values[key] = observed[key].Hash
		if _, err := db.ExecContext(ctx, `UPDATE dbo.sqlglot_observations SET hash=@p4,observed_at=SYSUTCDATETIME() WHERE scope=@p1 AND plan_hash=@p2 AND [key]=@p3`,
			o.scope, o.planHash, key, observed[key].Hash); err != nil {
			return fmt.Errorf("record re-observed input %s: %w", key, err)
		}
	}
	return nil
}

// restoreState puts back the ledger record an in-progress marker replaced,
// for a script that never ran: none, a legacy content-only row, or a state.
func restoreState(ctx context.Context, db sessionDB, scope, id string, previous scripts.State) error {
	var err error
	switch {
	case previous.Hash == "":
		_, err = db.ExecContext(ctx, `DELETE FROM dbo.sqlglot_migrations WHERE scope=@p1 AND path=@p2`, scope, id)
	case previous.Version == 1:
		var hash []byte
		if hash, err = hex.DecodeString(previous.Hash); err == nil {
			_, err = db.ExecContext(ctx, `UPDATE dbo.sqlglot_migrations SET hash=@p3,state=NULL,updated_at=SYSUTCDATETIME() WHERE scope=@p1 AND path=@p2`, scope, id, hash)
		}
	default:
		err = recordState(ctx, db, scope, id, previous)
	}
	if err != nil {
		return fmt.Errorf("restore ledger record of %s: %w", id, err)
	}
	return nil
}
