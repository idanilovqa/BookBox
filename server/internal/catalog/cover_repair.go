package catalog

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

// FillConfirmedCoverURL only supplements the existing confirmation. Lock the
// book before its match, as confirmation does, and reject stale search results.
func (r *PostgresRepository) FillConfirmedCoverURL(ctx context.Context, bookID, matchID uuid.UUID, expected time.Time, url string) error {
	if strings.TrimSpace(url) == "" {
		return ErrInvalidData
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id uuid.UUID
	err = tx.QueryRow(ctx, `select id from books where id=$1 and confirmed_match_id=$2 and updated_at=$3 and missing_at is null for update`, bookID, matchID, expected).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `update metadata_matches set cover_url=$3 where id=$1 and book_id=$2 and confirmed=true and btrim(coalesce(cover_url,''))=''`, matchID, bookID, url)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}
