package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (repository *PostgresRepository) ApplyInventory(
	ctx context.Context,
	update InventoryUpdate,
) (InventoryDelta, error) {
	if repository == nil || repository.pool == nil {
		return InventoryDelta{}, ErrInvalidData
	}
	if err := validateInventoryUpdate(update); err != nil {
		return InventoryDelta{}, err
	}

	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return InventoryDelta{}, fmt.Errorf("begin inventory application: %w", err)
	}
	defer transaction.Rollback(ctx)

	var scanRootID, scanStatus string
	if err := transaction.QueryRow(ctx, `
		select root_id, status
		from scan_runs
		where id=$1
		for update
	`, update.ScanRunID).Scan(&scanRootID, &scanStatus); err != nil {
		return InventoryDelta{}, mapNotFound(err, "load inventory scan")
	}
	if scanRootID != update.RootID || scanStatus != "running" {
		return InventoryDelta{}, ErrConflict
	}
	var lockedRoot string
	if err := transaction.QueryRow(ctx, `
		select id from library_roots where id=$1 for update
	`, update.RootID).Scan(&lockedRoot); err != nil {
		return InventoryDelta{}, mapNotFound(err, "lock inventory root")
	}

	storedFiles, err := loadInventorySourceFiles(ctx, transaction, update.RootID)
	if err != nil {
		return InventoryDelta{}, err
	}
	storedByPath := make(map[string]SourceFile, len(storedFiles))
	for _, file := range storedFiles {
		storedByPath[file.RelativePath] = file
	}

	now := repository.now().UTC().Truncate(time.Microsecond)
	scanID := update.ScanRunID
	delta := InventoryDelta{}
	seenPaths := make(map[string]struct{}, len(update.Files))
	regroupScopes := make(map[string]struct{})
	for _, inventoryFile := range update.Files {
		seenPaths[inventoryFile.RelativePath] = struct{}{}
		inventoryFile.ModifiedAt = postgresTime(inventoryFile.ModifiedAt)
		mediaValues := inventoryMedia(inventoryFile)
		mediaJSON, err := json.Marshal(mediaValues)
		if err != nil {
			return InventoryDelta{}, ErrInvalidData
		}
		stored, exists := storedByPath[inventoryFile.RelativePath]
		storedID := inventoryFile.ID
		unchanged := false
		wasMissing := false
		if exists {
			storedID = stored.ID
			unchanged = stored.Size == inventoryFile.Size &&
				postgresTime(stored.ModifiedAt).Equal(inventoryFile.ModifiedAt)
			wasMissing = stored.MissingAt != nil
		}

		var current SourceFile
		if unchanged {
			if _, err := transaction.Exec(ctx, `
				update source_files
				set size=$3,
				    modified_at=$4,
				    scan_run_id=$5,
				    missing_at=null,
				    file_kind=$6,
				    companion_kind=nullif($7,''),
				    updated_at=$8,
				    media=case when $6='cover' then $9::jsonb else media end
				where id=$1 and root_id=$2
			`, storedID, update.RootID, inventoryFile.Size, inventoryFile.ModifiedAt,
				update.ScanRunID, inventoryFile.FileKind, inventoryFile.CompanionKind, now,
				mediaJSON); err != nil {
				return InventoryDelta{}, classifyConstraint(err, "preserve inventory fingerprint")
			}
			current = stored
			current.Size = inventoryFile.Size
			current.ModifiedAt = inventoryFile.ModifiedAt
			current.ScanRunID = &scanID
			current.MissingAt = nil
			current.FileKind = inventoryFile.FileKind
			current.CompanionKind = inventoryFile.CompanionKind
			if inventoryFile.FileKind == FileKindCover {
				storedJSON, _ := json.Marshal(stored.Media)
				current.Media = mediaValues
				if !bytes.Equal(storedJSON, mediaJSON) {
					regroupScopes[inventoryScope(inventoryFile.RelativePath)] = struct{}{}
				}
			}
			delta.UnchangedFiles = append(delta.UnchangedFiles, current)
		} else {
			probeStatus := inventoryProbeStatus(inventoryFile)
			if _, err := transaction.Exec(ctx, `
				insert into source_files(
					id, root_id, relative_path, size, modified_at, sha256, media,
					scan_run_id, missing_at, file_kind, companion_kind,
					probe_status, probe_version, probe_error, probed_at, updated_at
				) values (
					$1,$2,$3,$4,$5,null,$6::jsonb,$7,null,$8,nullif($9,''),
					$10,0,null,null,$11
				)
				on conflict (root_id, relative_path) do update
				set size=excluded.size,
				    modified_at=excluded.modified_at,
				    sha256=null,
				    media=excluded.media,
				    scan_run_id=excluded.scan_run_id,
				    missing_at=null,
				    file_kind=excluded.file_kind,
				    companion_kind=excluded.companion_kind,
				    probe_status=excluded.probe_status,
				    probe_version=0,
				    probe_error=null,
				    probed_at=null,
				    updated_at=excluded.updated_at
			`, storedID, update.RootID, inventoryFile.RelativePath, inventoryFile.Size,
				inventoryFile.ModifiedAt, mediaJSON, update.ScanRunID, inventoryFile.FileKind,
				inventoryFile.CompanionKind, probeStatus, now); err != nil {
				return InventoryDelta{}, classifyConstraint(err, "apply inventory fingerprint")
			}
			current = SourceFile{
				ID: storedID, RootID: update.RootID, RelativePath: inventoryFile.RelativePath,
				Size: inventoryFile.Size, ModifiedAt: inventoryFile.ModifiedAt,
				Media: mediaValues, ScanRunID: &scanID,
				FileKind: inventoryFile.FileKind, CompanionKind: inventoryFile.CompanionKind,
				ProbeStatus: probeStatus,
			}
			switch {
			case inventoryFile.FileKind == FileKindAudio:
				delta.ProbeFiles = append(delta.ProbeFiles, current)
			case inventoryFile.FileKind == FileKindCompanion &&
				parseableCompanion(inventoryFile.CompanionKind):
				delta.InspectCompanions = append(delta.InspectCompanions, current)
			}
			regroupScopes[inventoryScope(inventoryFile.RelativePath)] = struct{}{}
		}
		if wasMissing {
			if err := insertAvailabilityEvent(
				ctx, transaction, update.RootID, "source_file", storedID, "restored", &scanID,
			); err != nil {
				return InventoryDelta{}, err
			}
			regroupScopes[inventoryScope(inventoryFile.RelativePath)] = struct{}{}
		}
	}

	if update.Complete {
		for _, stored := range storedFiles {
			if stored.MissingAt != nil || !inventoryPathInScope(stored.RelativePath, update.ScopePath) {
				continue
			}
			if _, seen := seenPaths[stored.RelativePath]; seen {
				continue
			}
			delta.MissingFileIDs = append(delta.MissingFileIDs, stored.ID)
			regroupScopes[inventoryScope(stored.RelativePath)] = struct{}{}
		}
	}

	delta.RegroupScopes = make([]string, 0, len(regroupScopes))
	for scope := range regroupScopes {
		delta.RegroupScopes = append(delta.RegroupScopes, scope)
	}
	sortInventoryDelta(&delta)
	if err := transaction.Commit(ctx); err != nil {
		return InventoryDelta{}, fmt.Errorf("commit inventory application: %w", err)
	}
	return delta, nil
}

func (repository *PostgresRepository) IncrementCatalogGeneration(
	ctx context.Context,
	rootID string,
) (CatalogGeneration, error) {
	if repository == nil || repository.pool == nil || !rootIDPattern.MatchString(rootID) {
		return CatalogGeneration{}, ErrInvalidData
	}
	updatedAt := repository.now().UTC().Truncate(time.Microsecond)
	generation := CatalogGeneration{RootID: rootID, UpdatedAt: updatedAt}
	err := repository.pool.QueryRow(ctx, `
		update catalog_generations
		set generation=generation+1, updated_at=$2
		where root_id=$1
		returning generation
	`, rootID, updatedAt).Scan(&generation.Value)
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogGeneration{}, ErrNotFound
	}
	if err != nil {
		return CatalogGeneration{}, fmt.Errorf("increment catalog generation: %w", err)
	}
	return generation, nil
}

func validateInventoryUpdate(update InventoryUpdate) error {
	if update.ScanRunID == uuid.Nil || !rootIDPattern.MatchString(update.RootID) ||
		ValidateSourceFolder(update.ScopePath) != nil {
		return ErrInvalidData
	}
	paths := make(map[string]struct{}, len(update.Files))
	ids := make(map[uuid.UUID]struct{}, len(update.Files))
	for _, file := range update.Files {
		if file.ID == uuid.Nil || file.RootID != update.RootID || file.Size < 0 ||
			file.ModifiedAt.IsZero() || !validFingerprintTime(file.ModifiedAt) {
			return ErrInvalidData
		}
		if err := ValidateRelativePath(file.RelativePath); err != nil {
			return err
		}
		if !inventoryPathInScope(file.RelativePath, update.ScopePath) {
			return ErrInvalidData
		}
		if _, exists := paths[file.RelativePath]; exists {
			return ErrInvalidData
		}
		if _, exists := ids[file.ID]; exists {
			return ErrInvalidData
		}
		paths[file.RelativePath] = struct{}{}
		ids[file.ID] = struct{}{}
		if !validInventoryClassification(file) {
			return ErrInvalidData
		}
	}
	return nil
}

func validInventoryClassification(file InventoryFile) bool {
	switch file.FileKind {
	case FileKindAudio:
		return file.CompanionKind == "" && file.ImageWidth == 0 &&
			file.ImageHeight == 0 && file.ImageFormat == ""
	case FileKindCover:
		if file.CompanionKind != "" {
			return false
		}
		if file.ImageWidth == 0 && file.ImageHeight == 0 && file.ImageFormat == "" {
			return true
		}
		return file.ImageWidth > 0 && file.ImageHeight > 0 &&
			(file.ImageFormat == "jpeg" || file.ImageFormat == "png" || file.ImageFormat == "webp")
	case FileKindCompanion:
		if file.ImageWidth != 0 || file.ImageHeight != 0 || file.ImageFormat != "" {
			return false
		}
		switch file.CompanionKind {
		case CompanionCue, CompanionNFO, CompanionOPF, CompanionPlaylist,
			CompanionEbook, CompanionOther:
			return true
		}
	}
	return false
}

func inventoryMedia(file InventoryFile) map[string]any {
	if file.FileKind != FileKindCover || file.ImageWidth <= 0 || file.ImageHeight <= 0 ||
		file.ImageFormat == "" {
		return map[string]any{}
	}
	return map[string]any{
		"width": file.ImageWidth, "height": file.ImageHeight, "imageFormat": file.ImageFormat,
	}
}

func inventoryProbeStatus(file InventoryFile) string {
	if file.FileKind == FileKindAudio ||
		(file.FileKind == FileKindCompanion && parseableCompanion(file.CompanionKind)) {
		return ProbePending
	}
	return ProbeNotApplicable
}

func parseableCompanion(kind string) bool {
	switch kind {
	case CompanionCue, CompanionNFO, CompanionOPF, CompanionPlaylist:
		return true
	default:
		return false
	}
}

func inventoryPathInScope(relativePath, scopePath string) bool {
	return scopePath == "" || strings.HasPrefix(relativePath, scopePath+"/")
}

func inventoryScope(relativePath string) string {
	scope := path.Dir(relativePath)
	if scope == "." {
		return ""
	}
	return scope
}

func postgresTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func loadInventorySourceFiles(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
) ([]SourceFile, error) {
	rows, err := transaction.Query(ctx, `
		select id, root_id, relative_path, size, modified_at,
		       coalesce(sha256,''), media, scan_run_id, missing_at,
		       file_kind, coalesce(companion_kind,''), probe_status,
		       probe_version, coalesce(probe_error,''), probed_at
		from source_files
		where root_id=$1
		order by relative_path, id
		for update
	`, rootID)
	if err != nil {
		return nil, fmt.Errorf("load inventory source files: %w", err)
	}
	defer rows.Close()
	var files []SourceFile
	for rows.Next() {
		file, err := scanInventorySourceFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inventory source files: %w", err)
	}
	return files, nil
}

func scanInventorySourceFile(row scanner) (SourceFile, error) {
	var file SourceFile
	var media []byte
	if err := row.Scan(
		&file.ID, &file.RootID, &file.RelativePath, &file.Size, &file.ModifiedAt,
		&file.SHA256, &media, &file.ScanRunID, &file.MissingAt,
		&file.FileKind, &file.CompanionKind, &file.ProbeStatus,
		&file.ProbeVersion, &file.ProbeError, &file.ProbedAt,
	); err != nil {
		return SourceFile{}, err
	}
	if err := json.Unmarshal(media, &file.Media); err != nil {
		return SourceFile{}, fmt.Errorf("decode inventory source media: %w", err)
	}
	return file, nil
}

func sortInventoryDelta(delta *InventoryDelta) {
	sortFiles := func(files []SourceFile) {
		sort.Slice(files, func(left, right int) bool {
			if files[left].RelativePath == files[right].RelativePath {
				return files[left].ID.String() < files[right].ID.String()
			}
			return files[left].RelativePath < files[right].RelativePath
		})
	}
	sortFiles(delta.ProbeFiles)
	sortFiles(delta.InspectCompanions)
	sortFiles(delta.UnchangedFiles)
	sort.Strings(delta.RegroupScopes)
	sort.Slice(delta.MissingFileIDs, func(left, right int) bool {
		return delta.MissingFileIDs[left].String() < delta.MissingFileIDs[right].String()
	})
}
