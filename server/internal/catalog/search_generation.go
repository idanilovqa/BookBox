package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var groupingEvidenceReasons = map[string]bool{
	"folder_default": true, "consistent_subtree": true,
	"distinct_identity": true, "conflicting_title": true,
	"conflicting_author": true, "weak_loose_files": true,
	"probe_failed": true, "manual": true,
	"reorganization_incomplete": true,
}

func (repository *PostgresRepository) PublishSearchProjection(
	ctx context.Context,
	projection SearchProjection,
) error {
	if repository == nil || repository.pool == nil ||
		!rootIDPattern.MatchString(projection.Snapshot.RootID) ||
		projection.Snapshot.CatalogGeneration <= 0 {
		return ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin search publication: %w", err)
	}
	defer transaction.Rollback(ctx)
	if err := requireCatalogGenerationTx(
		ctx, transaction, projection.Snapshot.RootID,
		projection.Snapshot.CatalogGeneration,
	); err != nil {
		return err
	}
	if err := publishSearchProjectionTx(ctx, transaction, projection); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit search publication: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) FinalizeInventoryAndPublish(
	ctx context.Context,
	finalization InventoryFinalization,
) (CatalogGeneration, error) {
	if repository == nil || repository.pool == nil || finalization.ScanRunID == uuid.Nil ||
		!rootIDPattern.MatchString(finalization.RootID) {
		return CatalogGeneration{}, ErrInvalidData
	}
	if err := validateGroupingEvidenceMap(finalization.Evidence); err != nil {
		return CatalogGeneration{}, err
	}
	if err := validateEvidenceForBooks(finalization.Books, finalization.Evidence); err != nil {
		return CatalogGeneration{}, err
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return CatalogGeneration{}, fmt.Errorf("begin inventory finalization: %w", err)
	}
	defer transaction.Rollback(ctx)
	if err := lockRunningScanTx(ctx, transaction, finalization.ScanRunID, finalization.RootID); err != nil {
		return CatalogGeneration{}, err
	}
	if _, err := lockCatalogGenerationTx(ctx, transaction, finalization.RootID); err != nil {
		return CatalogGeneration{}, err
	}
	completedAt := repository.now().UTC().Truncate(time.Microsecond)
	if err := markUnseenSourceFilesMissingTx(
		ctx, transaction, finalization.RootID, finalization.ScanRunID, completedAt,
	); err != nil {
		return CatalogGeneration{}, err
	}
	if err := replaceInferredBooksTx(ctx, transaction, finalization.RootID, finalization.Books); err != nil {
		return CatalogGeneration{}, err
	}
	if err := replaceGroupingEvidenceTx(
		ctx, transaction, finalization.RootID, "", finalization.Evidence,
	); err != nil {
		return CatalogGeneration{}, err
	}
	generation, err := incrementCatalogGenerationTx(
		ctx, transaction, finalization.RootID, &finalization.ScanRunID, completedAt,
	)
	if err != nil {
		return CatalogGeneration{}, err
	}
	projectionInput, err := loadSearchProjectionInputTx(
		ctx, transaction, finalization.RootID, finalization.ScanRunID, completedAt,
	)
	if err != nil {
		return CatalogGeneration{}, err
	}
	projection, err := BuildSearchProjection(projectionInput)
	if err != nil {
		return CatalogGeneration{}, fmt.Errorf("build inventory search projection: %w", err)
	}
	if err := publishSearchProjectionTx(ctx, transaction, projection); err != nil {
		return CatalogGeneration{}, err
	}
	if _, err := transaction.Exec(ctx, `
		update library_roots set scanned_at=$2, updated_at=now() where id=$1
	`, finalization.RootID, completedAt); err != nil {
		return CatalogGeneration{}, fmt.Errorf("update inventory root: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		update scan_runs set finished_at=$2, status='succeeded', error=null where id=$1
	`, finalization.ScanRunID, completedAt); err != nil {
		return CatalogGeneration{}, fmt.Errorf("finish inventory scan: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return CatalogGeneration{}, fmt.Errorf("commit inventory finalization: %w", err)
	}
	return generation, nil
}

func (repository *PostgresRepository) ApplyGroupingRefinement(
	ctx context.Context,
	refinement GroupingRefinement,
) (CatalogGeneration, error) {
	if repository == nil || repository.pool == nil ||
		!rootIDPattern.MatchString(refinement.RootID) ||
		ValidateSourceFolder(refinement.ScopePath) != nil ||
		refinement.ExpectedGeneration <= 0 {
		return CatalogGeneration{}, ErrInvalidData
	}
	if err := validateGroupingEvidenceMap(refinement.Evidence); err != nil {
		return CatalogGeneration{}, err
	}
	if err := validateEvidenceForBooks(refinement.Books, refinement.Evidence); err != nil {
		return CatalogGeneration{}, err
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return CatalogGeneration{}, fmt.Errorf("begin grouping refinement: %w", err)
	}
	defer transaction.Rollback(ctx)
	if err := requireCatalogGenerationTx(
		ctx, transaction, refinement.RootID, refinement.ExpectedGeneration,
	); err != nil {
		return CatalogGeneration{}, err
	}
	preserved, err := loadInferredBooksOutsideScopeTx(
		ctx, transaction, refinement.RootID, refinement.ScopePath,
	)
	if err != nil {
		return CatalogGeneration{}, err
	}
	allBooks := append(preserved, refinement.Books...)
	if err := replaceInferredBooksTx(ctx, transaction, refinement.RootID, allBooks); err != nil {
		return CatalogGeneration{}, err
	}
	if err := replaceGroupingEvidenceTx(
		ctx, transaction, refinement.RootID, refinement.ScopePath, refinement.Evidence,
	); err != nil {
		return CatalogGeneration{}, err
	}
	completedAt := repository.now().UTC().Truncate(time.Microsecond)
	generation, err := incrementCatalogGenerationTx(
		ctx, transaction, refinement.RootID, nil, completedAt,
	)
	if err != nil {
		return CatalogGeneration{}, err
	}
	var scanID uuid.UUID
	if err := transaction.QueryRow(ctx, `
		select coalesce(g.inventory_scan_run_id, s.scan_run_id)
		from catalog_generations g
		left join library_search_snapshots s on s.root_id=g.root_id
		where g.root_id=$1
	`, refinement.RootID).Scan(&scanID); err != nil {
		return CatalogGeneration{}, mapNotFound(err, "load refinement scan")
	}
	projectionInput, err := loadSearchProjectionInputTx(
		ctx, transaction, refinement.RootID, scanID, completedAt,
	)
	if err != nil {
		return CatalogGeneration{}, err
	}
	projection, err := BuildSearchProjection(projectionInput)
	if err != nil {
		return CatalogGeneration{}, fmt.Errorf("build refined search projection: %w", err)
	}
	if err := publishSearchProjectionTx(ctx, transaction, projection); err != nil {
		return CatalogGeneration{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return CatalogGeneration{}, fmt.Errorf("commit grouping refinement: %w", err)
	}
	return generation, nil
}

func loadInferredBooksOutsideScopeTx(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	scopePath string,
) ([]InferredBook, error) {
	if scopePath == "" {
		return nil, nil
	}
	rows, err := transaction.Query(ctx, `
		select `+bookColumns("b")+`
		from books b
		where b.root_id=$1
		  and not (b.source_folder=$2 or left(b.source_folder,length($2)+1)=$2||'/')
		  and b.missing_at is null
		order by b.id
	`, rootID, scopePath)
	if err != nil {
		return nil, fmt.Errorf("load books outside refinement scope: %w", err)
	}
	var books []Book
	for rows.Next() {
		book, scanErr := scanBook(rows)
		if scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("scan book outside refinement scope: %w", scanErr)
		}
		books = append(books, book)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate books outside refinement scope: %w", err)
	}
	rows.Close()

	result := make([]InferredBook, 0, len(books))
	for _, book := range books {
		fileRows, err := transaction.Query(ctx, `
			select file_id, role from book_files
			where book_id=$1 and root_id=$2
			order by file_id
		`, book.ID, rootID)
		if err != nil {
			return nil, fmt.Errorf("load assignments outside refinement scope: %w", err)
		}
		ids := make([]uuid.UUID, 0)
		roles := make(map[uuid.UUID]string)
		for fileRows.Next() {
			var id uuid.UUID
			var role string
			if err := fileRows.Scan(&id, &role); err != nil {
				fileRows.Close()
				return nil, fmt.Errorf("scan assignment outside refinement scope: %w", err)
			}
			ids = append(ids, id)
			roles[id] = role
		}
		if err := fileRows.Err(); err != nil {
			fileRows.Close()
			return nil, fmt.Errorf("iterate assignments outside refinement scope: %w", err)
		}
		fileRows.Close()
		result = append(result, InferredBook{
			RootID: book.RootID, SourceFolder: book.SourceFolder,
			GroupingKey: book.GroupingKey, Title: book.Title, Subtitle: book.Subtitle,
			Authors: book.Authors, Narrators: book.Narrators, Series: book.Series,
			SeriesPosition: book.SeriesPosition, Publisher: book.Publisher,
			PublishedDate: book.PublishedDate, PublicationYear: book.PublicationYear,
			Genres: book.Genres, ProviderSubjects: book.ProviderSubjects,
			Identifiers: book.Identifiers, MetadataSource: book.MetadataSource,
			Confidence: book.Confidence, ReviewState: book.ReviewState,
			SourceFileIDs: ids, FileRoles: roles,
		})
	}
	return result, nil
}

func lockRunningScanTx(
	ctx context.Context,
	transaction pgx.Tx,
	scanID uuid.UUID,
	rootID string,
) error {
	var lockedRoot, status string
	if err := transaction.QueryRow(ctx, `
		select root_id, status from scan_runs where id=$1 for update
	`, scanID).Scan(&lockedRoot, &status); err != nil {
		return mapNotFound(err, "load inventory scan")
	}
	if lockedRoot != rootID || status != "running" {
		return ErrConflict
	}
	var locked string
	if err := transaction.QueryRow(ctx, `
		select id from library_roots where id=$1 for update
	`, rootID).Scan(&locked); err != nil {
		return mapNotFound(err, "lock inventory root")
	}
	return nil
}

func requireCatalogGenerationTx(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	expected int64,
) error {
	current, err := lockCatalogGenerationTx(ctx, transaction, rootID)
	if err != nil {
		return err
	}
	if current != expected {
		return ErrConflict
	}
	return nil
}

func lockCatalogGenerationTx(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
) (int64, error) {
	var current int64
	if err := transaction.QueryRow(ctx, `
		select generation from catalog_generations where root_id=$1 for update
	`, rootID).Scan(&current); err != nil {
		return 0, mapNotFound(err, "lock catalog generation")
	}
	return current, nil
}

func incrementCatalogGenerationTx(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	scanID *uuid.UUID,
	updatedAt time.Time,
) (CatalogGeneration, error) {
	generation := CatalogGeneration{RootID: rootID, UpdatedAt: updatedAt}
	var scanValue any
	if scanID != nil {
		scanValue = *scanID
	}
	err := transaction.QueryRow(ctx, `
		update catalog_generations
		set generation=generation+1,
		    inventory_scan_run_id=coalesce($2, inventory_scan_run_id),
		    updated_at=$3
		where root_id=$1
		returning generation
	`, rootID, scanValue, updatedAt).Scan(&generation.Value)
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogGeneration{}, ErrNotFound
	}
	if err != nil {
		return CatalogGeneration{}, fmt.Errorf("increment catalog generation: %w", err)
	}
	return generation, nil
}

func validateGroupingEvidenceMap(values map[string]GroupingEvidence) error {
	for groupingKey, evidence := range values {
		if strings.TrimSpace(groupingKey) == "" || !groupingEvidenceReasons[evidence.ReasonCode] ||
			!reviewStates[evidence.ReviewState] || math.IsNaN(evidence.Confidence) ||
			evidence.Confidence < 0 || evidence.Confidence > 1 {
			return ErrInvalidData
		}
		if _, err := json.Marshal(evidence.Values); err != nil {
			return ErrInvalidData
		}
	}
	return nil
}

func validateEvidenceForBooks(
	books []InferredBook,
	values map[string]GroupingEvidence,
) error {
	reviewByKey := make(map[string]string, len(books))
	for _, book := range books {
		if _, duplicate := reviewByKey[book.GroupingKey]; duplicate {
			return ErrInvalidData
		}
		reviewByKey[book.GroupingKey] = book.ReviewState
	}
	for groupingKey, evidence := range values {
		if review, exists := reviewByKey[groupingKey]; !exists || review != evidence.ReviewState {
			return ErrInvalidData
		}
	}
	return nil
}

func replaceGroupingEvidenceTx(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	scopePath string,
	evidenceByKey map[string]GroupingEvidence,
) error {
	if scopePath == "" {
		if _, err := transaction.Exec(ctx, `
			delete from book_grouping_evidence where root_id=$1
		`, rootID); err != nil {
			return fmt.Errorf("clear grouping evidence: %w", err)
		}
	} else if _, err := transaction.Exec(ctx, `
		delete from book_grouping_evidence evidence
		using books b
		where evidence.book_id=b.id and evidence.root_id=$1
		  and (b.source_folder=$2 or left(b.source_folder,length($2)+1)=$2||'/')
	`, rootID, scopePath); err != nil {
		return fmt.Errorf("clear scoped grouping evidence: %w", err)
	}
	for groupingKey, evidence := range evidenceByKey {
		var bookID uuid.UUID
		query := `
			select id from books
			where root_id=$1 and grouping_key=$2 and missing_at is null`
		arguments := []any{rootID, groupingKey}
		if scopePath != "" {
			query += ` and (source_folder=$3 or left(source_folder,length($3)+1)=$3||'/')`
			arguments = append(arguments, scopePath)
		}
		if err := transaction.QueryRow(ctx, query, arguments...).Scan(&bookID); err != nil {
			return mapNotFound(err, "match grouping evidence book")
		}
		values := evidence.Values
		if values == nil {
			values = map[string][]string{}
		}
		payload, err := json.Marshal(values)
		if err != nil {
			return ErrInvalidData
		}
		if _, err := transaction.Exec(ctx, `
			insert into book_grouping_evidence(book_id,root_id,reason_code,evidence,updated_at)
			values ($1,$2,$3,$4,now())
			on conflict (book_id) do update
			set reason_code=excluded.reason_code, evidence=excluded.evidence, updated_at=now()
		`, bookID, rootID, evidence.ReasonCode, payload); err != nil {
			return classifyConstraint(err, "store grouping evidence")
		}
	}
	return nil
}
