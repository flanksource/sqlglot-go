package history

import (
	"context"
	"fmt"
)

func (s *Store) Claim(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("run id is required")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO claims (id) SELECT ? WHERE NOT EXISTS (SELECT 1 FROM records WHERE id = ?) ON CONFLICT DO NOTHING`, id, id)
	if err != nil {
		return fmt.Errorf("reserve run %s: %w", id, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("run %s already exists", id)
	}
	return nil
}
