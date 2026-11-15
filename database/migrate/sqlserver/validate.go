package sqlserver

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

func validate(request Request) error {
	if err := planner.ValidateArtifact(request.Artifact); err != nil {
		return err
	}
	if request.Hash != request.Artifact.Hash {
		return fmt.Errorf("reviewed hash does not match migration artifact")
	}
	if request.LockTimeout < 0 || request.LockTimeout > math.MaxInt32*time.Millisecond {
		return fmt.Errorf("migration lock timeout must be between zero and %s", math.MaxInt32*time.Millisecond)
	}
	return planner.ValidateAcknowledgements(request.Artifact, request.Acknowledgements)
}

// Validate checks reviewed inputs without opening a database or starting a transaction.
func Validate(request Request) error { return validate(request) }

func lock(ctx context.Context, tx *sql.Tx, timeout time.Duration) error {
	var status int
	if err := tx.QueryRowContext(ctx, `DECLARE @status int;
		EXEC @status = sys.sp_getapplock @Resource=N'`+planner.LockResource+`',
		@LockMode='Exclusive', @LockOwner='Transaction', @LockTimeout=@p1;
		SELECT @status`, timeout.Milliseconds()).Scan(&status); err != nil {
		return fmt.Errorf("lock migration transaction: %w", err)
	}
	if status < 0 {
		return fmt.Errorf("lock migration transaction: sp_getapplock returned %d", status)
	}
	return nil
}
