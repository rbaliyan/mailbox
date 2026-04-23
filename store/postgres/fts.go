package postgres

import (
	"context"
	"fmt"
)

// ensureFTS sets up the full-text search infrastructure:
//   - adds search_vector tsvector column (idempotent)
//   - creates a GIN index on search_vector (idempotent)
//   - creates a trigger function + trigger to keep search_vector up to date
//   - backfills existing rows with no search_vector value
func (s *Store) ensureFTS(ctx context.Context) error {
	// 1. Add the column — safe on existing tables.
	addCol := fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS search_vector tsvector`,
		s.opts.table,
	)
	if _, err := s.db.ExecContext(ctx, addCol); err != nil {
		return fmt.Errorf("add search_vector column: %w", err)
	}

	// 2. GIN index for efficient FTS queries.
	ginIdx := fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_%s_fts ON %s USING GIN(search_vector)`,
		s.opts.table, s.opts.table,
	)
	if _, err := s.db.ExecContext(ctx, ginIdx); err != nil {
		s.logger.Warn("failed to create GIN index for FTS", "error", err)
	}

	// 3. Trigger function — CREATE OR REPLACE is idempotent.
	// Each table gets its own function name to avoid cross-table conflicts.
	trigFn := fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION mailbox_fts_update_%s()
		RETURNS trigger AS $$
		BEGIN
			NEW.search_vector :=
				to_tsvector('english',
					coalesce(NEW.subject, '') || ' ' || coalesce(NEW.body, '')
				);
			RETURN NEW;
		END
		$$ LANGUAGE plpgsql`,
		s.opts.table,
	)
	if _, err := s.db.ExecContext(ctx, trigFn); err != nil {
		return fmt.Errorf("create FTS trigger function: %w", err)
	}

	// 4. Trigger — drop-then-create is the portable idiom (CREATE OR REPLACE
	//    TRIGGER requires PostgreSQL 14+).
	dropTrig := fmt.Sprintf(
		`DROP TRIGGER IF EXISTS trg_%s_fts ON %s`,
		s.opts.table, s.opts.table,
	)
	if _, err := s.db.ExecContext(ctx, dropTrig); err != nil {
		s.logger.Warn("failed to drop old FTS trigger", "error", err)
	}
	createTrig := fmt.Sprintf(`
		CREATE TRIGGER trg_%s_fts
		BEFORE INSERT OR UPDATE ON %s
		FOR EACH ROW EXECUTE FUNCTION mailbox_fts_update_%s()`,
		s.opts.table, s.opts.table, s.opts.table,
	)
	if _, err := s.db.ExecContext(ctx, createTrig); err != nil {
		return fmt.Errorf("create FTS trigger: %w", err)
	}

	// 5. Backfill rows that predate the trigger.
	// This runs inline (still within the background goroutine from Connect).
	backfill := fmt.Sprintf(`
		UPDATE %s
		SET search_vector = to_tsvector('english', coalesce(subject,'') || ' ' || coalesce(body,''))
		WHERE search_vector IS NULL`,
		s.opts.table,
	)
	res, err := s.db.ExecContext(ctx, backfill)
	if err != nil {
		s.logger.Warn("FTS backfill failed", "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		s.logger.Info("FTS backfill complete", "rows", n)
	}

	return nil
}
