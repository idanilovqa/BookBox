package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const fingerprintDispatchLimit = 256

func validFingerprintTime(value time.Time) bool {
	value = postgresTime(value)
	nanos := value.UnixNano()
	return time.Unix(0, nanos).UTC().Equal(value)
}

func fingerprintDedupeKey(kind string, fileID uuid.UUID, size, modifiedNanos int64) string {
	return kind + ":" + fileID.String() + ":" + strconv.FormatInt(size, 10) +
		":" + strconv.FormatInt(modifiedNanos, 10)
}

func activateFingerprintOutboxTx(
	ctx context.Context,
	tx pgx.Tx,
	rootID string,
	scanID uuid.UUID,
	scopePath string,
	readyAt time.Time,
) error {
	if _, err := tx.Exec(ctx, `
		delete from fingerprint_job_outbox o
		using source_files sf
		where o.file_id=sf.id and o.root_id=sf.root_id and sf.root_id=$1
		  and ($2='' or left(sf.relative_path,length($2)+1)=$2||'/')
		  and (sf.missing_at is not null or sf.probe_status<>'pending'
		       or not (sf.file_kind='audio' or
		               (sf.file_kind='companion' and sf.companion_kind in ('cue','nfo','opf','playlist'))))
	`, rootID, scopePath); err != nil {
		return fmt.Errorf("prune fingerprint outbox: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into fingerprint_job_outbox(
			file_id,root_id,job_kind,relative_path,expected_size,
			expected_modified_at,expected_modified_nanos,scan_run_id,
			dispatch_attempts,next_dispatch_at,last_dispatched_at,updated_at
		)
		select sf.id,sf.root_id,
		       case when sf.file_kind='audio' then 'probe_file' else 'inspect_companion' end,
		       sf.relative_path,sf.size,sf.modified_at,
		       (extract(epoch from sf.modified_at)*1000000000)::bigint,
		       $2,0,$4,null,$4
		from source_files sf
		where sf.root_id=$1 and sf.scan_run_id=$2 and sf.missing_at is null
		  and sf.probe_status='pending'
		  and ($3='' or left(sf.relative_path,length($3)+1)=$3||'/')
		  and (sf.file_kind='audio' or
		       (sf.file_kind='companion' and sf.companion_kind in ('cue','nfo','opf','playlist')))
		on conflict(file_id) do update set
			job_kind=excluded.job_kind,relative_path=excluded.relative_path,
			expected_size=excluded.expected_size,
			expected_modified_at=excluded.expected_modified_at,
			expected_modified_nanos=excluded.expected_modified_nanos,
			scan_run_id=excluded.scan_run_id,dispatch_attempts=0,
			next_dispatch_at=excluded.next_dispatch_at,last_dispatched_at=null,
			updated_at=excluded.updated_at
	`, rootID, scanID, scopePath, readyAt); err != nil {
		return classifyConstraint(err, "activate fingerprint outbox")
	}
	return nil
}

// DrainReadyFingerprintOutbox dispatches a bounded set of durable fingerprint
// intents. The intent remains until the matching result is committed.
func (repository *PostgresRepository) DrainReadyFingerprintOutbox(
	ctx context.Context,
	now time.Time,
	limit int,
) (int, error) {
	if repository == nil || repository.pool == nil {
		return 0, ErrInvalidData
	}
	if limit <= 0 || limit > fingerprintDispatchLimit {
		limit = fingerprintDispatchLimit
	}
	now = now.UTC().Truncate(time.Microsecond)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin fingerprint dispatch: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		select o.file_id,o.root_id,o.job_kind,o.relative_path,o.expected_size,
		       o.expected_modified_nanos,o.dispatch_attempts
		from fingerprint_job_outbox o
		join source_files sf on sf.id=o.file_id and sf.root_id=o.root_id
		where o.next_dispatch_at<=$1 and sf.missing_at is null and sf.probe_status='pending'
		  and sf.relative_path=o.relative_path and sf.size=o.expected_size
		  and sf.modified_at=o.expected_modified_at
		  and not exists (
			select 1 from worker_jobs j
			where j.kind=o.job_kind and j.status in ('queued','running')
			  and j.dedupe_key=o.job_kind||':'||o.file_id::text||':'||
			      o.expected_size::text||':'||o.expected_modified_nanos::text
		  )
		order by o.next_dispatch_at,o.file_id
		for update of o skip locked
		limit $2
	`, now, limit)
	if err != nil {
		return 0, fmt.Errorf("select fingerprint outbox: %w", err)
	}
	type intent struct {
		fileID                     uuid.UUID
		rootID, kind, relativePath string
		size, nanos                int64
		attempts                   int
	}
	var intents []intent
	for rows.Next() {
		var item intent
		if err := rows.Scan(&item.fileID, &item.rootID, &item.kind, &item.relativePath,
			&item.size, &item.nanos, &item.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		intents = append(intents, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	dispatched := 0
	for _, item := range intents {
		key := fingerprintDedupeKey(item.kind, item.fileID, item.size, item.nanos)
		payload, err := json.Marshal(map[string]any{
			"fileId": item.fileID.String(), "rootId": item.rootID,
			"relativePath":  item.relativePath,
			"size":          strconv.FormatInt(item.size, 10),
			"modifiedNanos": strconv.FormatInt(item.nanos, 10),
		})
		if err != nil {
			return 0, ErrInvalidData
		}
		result, err := tx.Exec(ctx, `
			insert into worker_jobs(id,kind,dedupe_key,payload,run_after,max_attempts)
			values($1,$2,$3,$4,$5,5)
			on conflict(kind,dedupe_key)
				where dedupe_key is not null and status in ('queued','running')
			do nothing
		`, uuid.New(), item.kind, key, payload, now)
		if err != nil {
			return 0, classifyConstraint(err, "dispatch fingerprint job")
		}
		if result.RowsAffected() == 0 {
			continue
		}
		delay := time.Minute << min(item.attempts, 8)
		if delay > 6*time.Hour {
			delay = 6 * time.Hour
		}
		if _, err := tx.Exec(ctx, `
			update fingerprint_job_outbox
			set dispatch_attempts=dispatch_attempts+1,last_dispatched_at=$2,
			    next_dispatch_at=$3,updated_at=$2
			where file_id=$1
		`, item.fileID, now, now.Add(delay)); err != nil {
			return 0, fmt.Errorf("advance fingerprint outbox: %w", err)
		}
		dispatched++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit fingerprint dispatch: %w", err)
	}
	return dispatched, nil
}

func acknowledgeFingerprintOutboxTx(
	ctx context.Context,
	tx pgx.Tx,
	expected SourceFile,
	kind string,
) error {
	if _, err := tx.Exec(ctx, `
		delete from fingerprint_job_outbox
		where file_id=$1 and root_id=$2 and job_kind=$3
		  and expected_size=$4 and expected_modified_at=$5
	`, expected.ID, expected.RootID, kind, expected.Size, postgresTime(expected.ModifiedAt)); err != nil {
		return fmt.Errorf("acknowledge fingerprint outbox: %w", err)
	}
	return nil
}
