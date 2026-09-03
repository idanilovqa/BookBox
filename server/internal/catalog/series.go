package catalog

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

// seriesSortSQL encodes a numeric position into a stable text cursor key.
// Unknown positions follow numbered entries; standalone titles follow series.
func seriesSortSQL(prefix string) string {
	return fmt.Sprintf(`(
 case when btrim(coalesce(%[1]sseries,''))='' then '1' else '0' end
 || lower(btrim(coalesce(%[1]sseries,''))) || chr(31)
 || case when btrim(coalesce(%[1]sseries_position,'')) ~ '^[0-9]{1,9}(\.[0-9]{1,6})?$'
 then '0'||to_char(btrim(%[1]sseries_position)::numeric,'FM0000000000.000000')
 else '1' end || chr(31) || lower(%[1]stitle)
 )`, prefix)
}

// FillMissingBookSeries does not overwrite existing values or wait behind a
// scan holding the book lock. Skipped records are revisited by maintenance.
func (r *PostgresRepository) FillMissingBookSeries(ctx context.Context, id uuid.UUID, expected time.Time, series, position string) (bool, error) {
	series, position = strings.TrimSpace(series), strings.TrimSpace(position)
	if series == "" {
		return false, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// Match the scan/publication lock order and skip roots being published.
	var rootID string
	var generation int64
	err = tx.QueryRow(ctx, `select g.root_id,g.generation from catalog_generations g
 join books b on b.root_id=g.root_id where b.id=$1
 for update of g skip locked`, id).Scan(&rootID, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var currentSeries, currentPosition string
	err = tx.QueryRow(ctx, `
 with available as (
 select id from books where id=$1 and updated_at=$2 and missing_at is null
 for update skip locked
 )
 update books b set
 series=case when btrim(coalesce(b.series,''))='' then $3 else b.series end,
 series_position=case when btrim(coalesce(b.series_position,''))='' then nullif($4,'') else b.series_position end,
 updated_at=now()
 from available a where b.id=a.id
 and (btrim(coalesce(b.series,''))='' or lower(btrim(b.series))=lower($3))
 and (btrim(coalesce(b.series,''))='' or (btrim(coalesce(b.series_position,''))='' and $4<>''))
 returning coalesce(b.series,''),coalesce(b.series_position,'')
 `, id, expected, series, position).Scan(&currentSeries, &currentPosition)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = incrementCatalogGenerationTx(ctx, tx, rootID, nil, r.now().UTC()); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// QueuePendingSeriesProjection batches the changed root into one publication.
// Comparing generations also recovers a maintenance pass interrupted by restart.
func (r *PostgresRepository) QueuePendingSeriesProjection(ctx context.Context, rootID string) error {
	var generation int64
	err := r.pool.QueryRow(ctx, `select g.generation from catalog_generations g
 join library_search_snapshots s on s.root_id=g.root_id
 where g.root_id=$1 and g.generation>s.catalog_generation`, rootID).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = r.EnqueueDeduplicated(ctx, WorkerJob{ID: uuid.New(), Kind: JobPublishSearch,
		Payload: map[string]any{"rootId": rootID, "catalogGeneration": generation}}, PublishSearchDedupeKey(rootID, generation))
	return err
}
