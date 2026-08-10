package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"bookbox.local/server/internal/media"
)

func (repository *PostgresRepository) GetSourceFile(
	ctx context.Context,
	fileID uuid.UUID,
) (SourceFile, error) {
	if repository == nil || repository.pool == nil || fileID == uuid.Nil {
		return SourceFile{}, ErrInvalidData
	}
	file, err := scanInventorySourceFile(repository.pool.QueryRow(ctx, `
		select id, root_id, relative_path, size, modified_at,
		       coalesce(sha256,''), media, scan_run_id, missing_at,
		       file_kind, coalesce(companion_kind,''), probe_status,
		       probe_version, coalesce(probe_error,''), probed_at
		from source_files where id=$1 and missing_at is null
	`, fileID))
	if err != nil {
		return SourceFile{}, mapNotFound(err, "load staged source file")
	}
	return file, nil
}

func (repository *PostgresRepository) CurrentCatalogGeneration(
	ctx context.Context,
	rootID string,
) (CatalogGeneration, error) {
	if repository == nil || repository.pool == nil || !rootIDPattern.MatchString(rootID) {
		return CatalogGeneration{}, ErrInvalidData
	}
	generation := CatalogGeneration{RootID: rootID}
	if err := repository.pool.QueryRow(ctx, `
		select generation, updated_at from catalog_generations where root_id=$1
	`, rootID).Scan(&generation.Value, &generation.UpdatedAt); err != nil {
		return CatalogGeneration{}, mapNotFound(err, "load current catalog generation")
	}
	return generation, nil
}

func (repository *PostgresRepository) FinishScopedInventory(
	ctx context.Context,
	scanID uuid.UUID,
	rootID string,
) error {
	if repository == nil || repository.pool == nil || scanID == uuid.Nil ||
		!rootIDPattern.MatchString(rootID) {
		return ErrInvalidData
	}
	result, err := repository.pool.Exec(ctx, `
		update scan_runs
		set finished_at=$3, status='succeeded', error=null
		where id=$1 and root_id=$2 and status='running'
	`, scanID, rootID, repository.now().UTC())
	if err != nil {
		return fmt.Errorf("finish scoped inventory: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (repository *PostgresRepository) SaveProbeResult(
	ctx context.Context,
	expected SourceFile,
	metadata media.Metadata,
	failure string,
) (CatalogGeneration, bool, error) {
	values := probeMediaValues(metadata, failure)
	return repository.saveStagedFileResult(ctx, expected, values, failure, JobProbeFile)
}

func (repository *PostgresRepository) SaveCompanionInspection(
	ctx context.Context,
	expected SourceFile,
	evidence map[string]any,
	failure string,
) (CatalogGeneration, bool, error) {
	values := make(map[string]any, len(evidence)+2)
	for key, value := range evidence {
		values[key] = value
	}
	values["role"] = FileRoleSidecar
	if failure != "" {
		values["inspectionError"] = failure
	}
	return repository.saveStagedFileResult(ctx, expected, values, failure, JobInspectCompanion)
}

func (repository *PostgresRepository) saveStagedFileResult(
	ctx context.Context,
	expected SourceFile,
	values map[string]any,
	failure string,
	jobKind string,
) (CatalogGeneration, bool, error) {
	if repository == nil || repository.pool == nil || expected.ID == uuid.Nil ||
		!rootIDPattern.MatchString(expected.RootID) || expected.Size < 0 || expected.ModifiedAt.IsZero() {
		return CatalogGeneration{}, false, ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return CatalogGeneration{}, false, fmt.Errorf("begin staged file update: %w", err)
	}
	defer transaction.Rollback(ctx)
	var storedSize int64
	var storedModified time.Time
	var storedMedia []byte
	var storedPath, storedStatus, storedFailure string
	if err := transaction.QueryRow(ctx, `
		select relative_path, size, modified_at, media, probe_status, coalesce(probe_error,'')
		from source_files
		where id=$1 and root_id=$2 and missing_at is null
		for update
	`, expected.ID, expected.RootID).Scan(
		&storedPath, &storedSize, &storedModified, &storedMedia, &storedStatus, &storedFailure,
	); err != nil {
		return CatalogGeneration{}, false, mapNotFound(err, "lock staged source file")
	}
	if storedSize != expected.Size || !postgresTime(storedModified).Equal(postgresTime(expected.ModifiedAt)) {
		return CatalogGeneration{}, false, ErrConflict
	}
	// Successful work may have completed through an already-open descriptor while
	// a reorganization moved the path. A failure must still refer to the current
	// path; otherwise an old-path open race would consume the refreshed intent.
	if failure != "" && storedPath != expected.RelativePath {
		return CatalogGeneration{}, false, ErrConflict
	}
	var previous map[string]any
	if err := json.Unmarshal(storedMedia, &previous); err != nil {
		return CatalogGeneration{}, false, fmt.Errorf("decode prior staged media: %w", err)
	}
	status := ProbeSucceeded
	if failure != "" {
		status = ProbeFailed
	}
	changed := false
	raw, err := json.Marshal(values)
	if err != nil {
		return CatalogGeneration{}, false, ErrInvalidData
	}
	var normalizedValues map[string]any
	if err := json.Unmarshal(raw, &normalizedValues); err != nil {
		return CatalogGeneration{}, false, ErrInvalidData
	}
	changed = storedStatus != status || storedFailure != failure ||
		!reflect.DeepEqual(previous, normalizedValues)
	result, err := transaction.Exec(ctx, `
		update source_files
		set media=$4, probe_status=$5, probe_version=probe_version+1,
		    probe_error=nullif($6,''), probed_at=$7, updated_at=$7
		where id=$1 and root_id=$2 and size=$3 and modified_at=$8 and missing_at is null
	`, expected.ID, expected.RootID, expected.Size, raw, status, failure,
		repository.now().UTC(), postgresTime(expected.ModifiedAt))
	if err != nil {
		return CatalogGeneration{}, false, classifyConstraint(err, "save staged file result")
	}
	if result.RowsAffected() != 1 {
		return CatalogGeneration{}, false, ErrConflict
	}
	if err := acknowledgeFingerprintOutboxTx(ctx, transaction, expected, jobKind); err != nil {
		return CatalogGeneration{}, false, err
	}
	generation := CatalogGeneration{RootID: expected.RootID}
	if err := transaction.QueryRow(ctx, `
		select generation, updated_at from catalog_generations where root_id=$1
	`, expected.RootID).Scan(&generation.Value, &generation.UpdatedAt); err != nil {
		return CatalogGeneration{}, false, mapNotFound(err, "load staged catalog generation")
	}
	if err := transaction.Commit(ctx); err != nil {
		return CatalogGeneration{}, false, fmt.Errorf("commit staged file update: %w", err)
	}
	return generation, changed, nil
}

func probeMediaValues(metadata media.Metadata, failure string) map[string]any {
	values := map[string]any{"role": FileRoleAudio}
	if failure != "" {
		values["probeError"] = failure
		return values
	}
	values["durationMs"] = metadata.Duration.Milliseconds()
	values["title"] = metadata.Title
	values["album"] = metadata.Album
	values["albumArtist"] = metadata.AlbumArtist
	values["author"] = metadata.Author
	values["artist"] = metadata.Artist
	values["narrator"] = metadata.Narrator
	values["genre"] = metadata.Genre
	values["date"] = metadata.Date
	values["series"] = metadata.Series
	values["track"] = metadata.Track
	values["disc"] = metadata.Disc
	values["identifiers"] = metadata.Identifiers
	values["audioCodec"] = metadata.AudioCodec
	values["width"] = metadata.Width
	values["height"] = metadata.Height
	return values
}

func (repository *PostgresRepository) LoadGroupingScope(
	ctx context.Context,
	rootID string,
	scopePath string,
) ([]SourceFile, []GroupingCorrection, error) {
	if repository == nil || repository.pool == nil || !rootIDPattern.MatchString(rootID) ||
		ValidateSourceFolder(scopePath) != nil {
		return nil, nil, ErrInvalidData
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, fmt.Errorf("begin grouping scope read: %w", err)
	}
	defer transaction.Rollback(ctx)
	rows, err := transaction.Query(ctx, `
		select id, root_id, relative_path, size, modified_at,
		       coalesce(sha256,''), media, scan_run_id, missing_at,
		       file_kind, coalesce(companion_kind,''), probe_status,
		       probe_version, coalesce(probe_error,''), probed_at
		from source_files
		where root_id=$1 and missing_at is null
		  and ($2='' or left(relative_path,length($2)+1)=$2||'/')
		order by relative_path, id
	`, rootID, scopePath)
	if err != nil {
		return nil, nil, fmt.Errorf("load grouping scope files: %w", err)
	}
	var files []SourceFile
	for rows.Next() {
		file, scanErr := scanInventorySourceFile(rows)
		if scanErr != nil {
			rows.Close()
			return nil, nil, scanErr
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	correctionsByFolder, err := loadGroupingCorrections(ctx, transaction, rootID, false)
	if err != nil {
		return nil, nil, err
	}
	corrections := make([]GroupingCorrection, 0)
	for folder, correction := range correctionsByFolder {
		if scopePath == "" || folder == scopePath ||
			(len(folder) > len(scopePath) && folder[:len(scopePath)+1] == scopePath+"/") {
			corrections = append(corrections, correction)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return files, corrections, nil
}

func (repository *PostgresRepository) LoadSearchProjection(
	ctx context.Context,
	rootID string,
	expectedGeneration int64,
) (SearchProjection, error) {
	if repository == nil || repository.pool == nil || !rootIDPattern.MatchString(rootID) ||
		expectedGeneration < 0 {
		return SearchProjection{}, ErrInvalidData
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return SearchProjection{}, err
	}
	defer transaction.Rollback(ctx)
	var generation int64
	var scanID *uuid.UUID
	if err := transaction.QueryRow(ctx, `
		select generation, inventory_scan_run_id
		from catalog_generations where root_id=$1
	`, rootID).Scan(&generation, &scanID); err != nil {
		return SearchProjection{}, mapNotFound(err, "load publication generation")
	}
	if expectedGeneration > 0 && generation != expectedGeneration {
		return SearchProjection{}, ErrConflict
	}
	if scanID == nil {
		if err := transaction.QueryRow(ctx, `
			select scan_run_id from library_search_snapshots where root_id=$1
		`, rootID).Scan(&scanID); err != nil {
			return SearchProjection{}, mapNotFound(err, "load publication scan")
		}
	}
	input, err := loadSearchProjectionInputTx(ctx, transaction, rootID, *scanID, repository.now().UTC())
	if err != nil {
		return SearchProjection{}, err
	}
	projection, err := BuildSearchProjection(input)
	if err != nil {
		return SearchProjection{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return SearchProjection{}, err
	}
	return projection, nil
}
