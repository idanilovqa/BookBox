package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPageSize = 50
	maxPageSize     = 100
	jobLease        = 15 * time.Minute
)

var (
	rootIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	metadataSources = map[string]bool{
		MetadataInferred: true, MetadataLocal: true, MetadataOpenLibrary: true,
		MetadataGoogleBooks: true, MetadataConfirmed: true, MetadataMixed: true,
	}
	reviewStates = map[string]bool{
		ReviewUnreviewed: true, ReviewNeedsReview: true, ReviewConfirmed: true,
	}
	jobKinds = map[string]bool{
		JobScanRoot: true, JobProbeFile: true, JobInspectCompanion: true,
		JobRegroupScope: true, JobPublishSearch: true, JobEnrichBook: true,
		JobPublishCover: true, JobReorganizeFiles: true,
	}
)

type PostgresRepository struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{
		pool: pool,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (repository *PostgresRepository) SyncRoots(
	ctx context.Context,
	roots []LibraryRoot,
) error {
	if repository == nil || repository.pool == nil {
		return ErrInvalidData
	}
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if !rootIDPattern.MatchString(root.ID) || strings.TrimSpace(root.DisplayName) == "" {
			return ErrInvalidData
		}
		if _, exists := seen[root.ID]; exists {
			return ErrInvalidData
		}
		seen[root.ID] = struct{}{}
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin root sync: %w", err)
	}
	defer transaction.Rollback(ctx)
	for _, root := range roots {
		if _, err := transaction.Exec(ctx, `
			insert into library_roots(id, display_name, scanned_at)
			values ($1, $2, $3)
			on conflict (id) do update
			set display_name=excluded.display_name,
			    scanned_at=coalesce(excluded.scanned_at, library_roots.scanned_at),
			    updated_at=now()
		`, root.ID, root.DisplayName, root.ScannedAt); err != nil {
			return fmt.Errorf("sync root %q: %w", root.ID, err)
		}
		if _, err := transaction.Exec(ctx, `
			insert into catalog_generations(root_id, generation)
			values ($1, 1)
			on conflict (root_id) do nothing
		`, root.ID); err != nil {
			return fmt.Errorf("initialize catalog generation %q: %w", root.ID, err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit root sync: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) BeginScan(
	ctx context.Context,
	rootID string,
) (ScanRun, error) {
	if !rootIDPattern.MatchString(rootID) {
		return ScanRun{}, ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return ScanRun{}, fmt.Errorf("begin scan transaction: %w", err)
	}
	defer transaction.Rollback(ctx)
	var lockedRoot string
	if err := transaction.QueryRow(
		ctx,
		`select id from library_roots where id=$1 for update`,
		rootID,
	).Scan(&lockedRoot); err != nil {
		return ScanRun{}, mapNotFound(err, "lock scan root")
	}
	run := ScanRun{
		ID:        uuid.New(),
		RootID:    rootID,
		StartedAt: repository.now(),
		Status:    "running",
	}
	if _, err := transaction.Exec(ctx, `
		update scan_runs
		set status='failed',
		    finished_at=$2,
		    error='superseded by a newer scan'
		where root_id=$1 and status='running'
	`, rootID, run.StartedAt); err != nil {
		return ScanRun{}, fmt.Errorf("supersede running scan: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		insert into scan_runs(id, root_id, started_at, status)
		values ($1, $2, $3, $4)
	`, run.ID, run.RootID, run.StartedAt, run.Status); err != nil {
		return ScanRun{}, classifyConstraint(err, "begin scan")
	}
	if err := transaction.Commit(ctx); err != nil {
		return ScanRun{}, fmt.Errorf("commit scan start: %w", err)
	}
	return run, nil
}

func (repository *PostgresRepository) UpsertSourceFiles(
	ctx context.Context,
	scanID uuid.UUID,
	files []SourceFile,
) error {
	if scanID == uuid.Nil {
		return ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin source file upsert: %w", err)
	}
	defer transaction.Rollback(ctx)
	var rootID, status string
	if err := transaction.QueryRow(ctx, `
		select root_id, status from scan_runs where id=$1 for update
	`, scanID).Scan(&rootID, &status); err != nil {
		return mapNotFound(err, "load scan")
	}
	if status != "running" {
		return ErrConflict
	}
	seenPaths := make(map[string]struct{}, len(files))
	for _, file := range files {
		if err := validateSourceFile(file, rootID); err != nil {
			return err
		}
		if _, exists := seenPaths[file.RelativePath]; exists {
			return ErrInvalidData
		}
		seenPaths[file.RelativePath] = struct{}{}
		media, err := marshalObject(file.Media)
		if err != nil {
			return err
		}
		var restoredID uuid.UUID
		restoreError := transaction.QueryRow(ctx, `
			select id from source_files
			where root_id=$1 and relative_path=$2 and missing_at is not null
			for update
		`, rootID, file.RelativePath).Scan(&restoredID)
		restored := restoreError == nil
		if restoreError != nil && !errors.Is(restoreError, pgx.ErrNoRows) {
			return fmt.Errorf("check restored source file: %w", restoreError)
		}
		if _, err := transaction.Exec(ctx, `
			insert into source_files(
				id, root_id, relative_path, size, modified_at, sha256, media, scan_run_id
			)
			values ($1,$2,$3,$4,$5,nullif($6,''),$7,$8)
			on conflict (root_id, relative_path) do update
			set size=excluded.size,
			    modified_at=excluded.modified_at,
			    sha256=excluded.sha256,
			    media=excluded.media,
			    scan_run_id=excluded.scan_run_id,
			    missing_at=null,
			    updated_at=now()
		`, file.ID, rootID, file.RelativePath, file.Size, file.ModifiedAt,
			file.SHA256, media, scanID); err != nil {
			return classifyConstraint(err, "upsert source file")
		}
		if restored {
			if err := insertAvailabilityEvent(
				ctx,
				transaction,
				rootID,
				"source_file",
				restoredID,
				"restored",
				&scanID,
			); err != nil {
				return err
			}
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit source file upsert: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) FinishScan(
	ctx context.Context,
	scanID uuid.UUID,
	result ScanResult,
) error {
	if scanID == uuid.Nil || (result.Success && result.Partial) {
		return ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin scan finish: %w", err)
	}
	defer transaction.Rollback(ctx)
	var rootID, currentStatus string
	if err := transaction.QueryRow(ctx, `
		select root_id, status from scan_runs where id=$1 for update
	`, scanID).Scan(&rootID, &currentStatus); err != nil {
		return mapNotFound(err, "load scan")
	}
	if currentStatus != "running" {
		return ErrConflict
	}
	status := "failed"
	if result.Success {
		status = "succeeded"
	} else if result.Partial {
		status = "partial"
	}
	finishedAt := repository.now()
	if result.Success {
		rows, err := transaction.Query(ctx, `
			update source_files
			set missing_at=$3, updated_at=now()
			where root_id=$1
			  and scan_run_id is distinct from $2
			  and missing_at is null
			returning id
		`, rootID, scanID, finishedAt)
		if err != nil {
			return fmt.Errorf("mark missing source files: %w", err)
		}
		var missingIDs []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("read missing source file: %w", err)
			}
			missingIDs = append(missingIDs, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("read missing source files: %w", err)
		}
		rows.Close()
		for _, id := range missingIDs {
			if err := insertAvailabilityEvent(
				ctx,
				transaction,
				rootID,
				"source_file",
				id,
				"missing",
				&scanID,
			); err != nil {
				return err
			}
		}
		if _, err := transaction.Exec(ctx, `
			update library_roots set scanned_at=$2, updated_at=now() where id=$1
		`, rootID, finishedAt); err != nil {
			return fmt.Errorf("update root scan time: %w", err)
		}
	}
	if _, err := transaction.Exec(ctx, `
		update scan_runs
		set finished_at=$2, status=$3, error=nullif($4,'')
		where id=$1
	`, scanID, finishedAt, status, result.Error); err != nil {
		return fmt.Errorf("finish scan: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit scan finish: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) ReplaceInferredBooks(
	ctx context.Context,
	rootID string,
	inferred []InferredBook,
) error {
	if !rootIDPattern.MatchString(rootID) {
		return ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin inferred book replacement: %w", err)
	}
	defer transaction.Rollback(ctx)
	if err := replaceInferredBooksTx(ctx, transaction, rootID, inferred); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit inferred book replacement: %w", err)
	}
	return nil
}

func replaceInferredBooksTx(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	inferred []InferredBook,
) error {
	var rootExists bool
	if err := transaction.QueryRow(
		ctx,
		`select exists(select 1 from library_roots where id=$1)`,
		rootID,
	).Scan(&rootExists); err != nil {
		return fmt.Errorf("check inferred root: %w", err)
	}
	if !rootExists {
		return ErrNotFound
	}
	if _, err := transaction.Exec(
		ctx,
		`select id from books where root_id=$1 for update`,
		rootID,
	); err != nil {
		return fmt.Errorf("lock root books: %w", err)
	}
	corrections, err := loadGroupingCorrections(ctx, transaction, rootID)
	if err != nil {
		return err
	}
	claimedFiles, correctedBooksByFolder, correctedBookIDs, err := applyGroupingCorrections(
		ctx,
		transaction,
		rootID,
		corrections,
	)
	if err != nil {
		return err
	}
	prepared, err := prepareInferredBooks(
		ctx,
		transaction,
		rootID,
		inferred,
		corrections,
		claimedFiles,
		correctedBooksByFolder,
	)
	if err != nil {
		return err
	}
	seenKeys := make(map[string]struct{}, len(prepared))
	presentBookIDs := append(make([]uuid.UUID, 0, len(prepared)+len(correctedBookIDs)),
		correctedBookIDs...)
	for _, candidate := range prepared {
		naturalKey := candidate.SourceFolder + "\x00" + candidate.GroupingKey
		if _, exists := seenKeys[naturalKey]; exists {
			return ErrInvalidData
		}
		seenKeys[naturalKey] = struct{}{}
		if err := verifyAssignmentFiles(ctx, transaction, rootID, candidate.SourceFileIDs); err != nil {
			return err
		}
		bookID, locked, confirmed, missing, err := findExistingBook(
			ctx,
			transaction,
			rootID,
			candidate.SourceFolder,
			candidate.GroupingKey,
		)
		if err != nil {
			return err
		}
		if bookID == uuid.Nil {
			bookID = uuid.New()
			if err := insertInferredBook(ctx, transaction, bookID, candidate, false); err != nil {
				return err
			}
		} else {
			assignedMissing, err := bookHasMissingAssignments(ctx, transaction, bookID)
			if err != nil {
				return err
			}
			if !confirmed && (!locked || assignedMissing) {
				if err := updateInferredBook(ctx, transaction, bookID, candidate); err != nil {
					return err
				}
			}
			if missing {
				if _, err := transaction.Exec(
					ctx,
					`update books set missing_at=null, updated_at=now() where id=$1`,
					bookID,
				); err != nil {
					return fmt.Errorf("restore inferred book: %w", err)
				}
				if err := insertAvailabilityEvent(
					ctx, transaction, rootID, "book", bookID, "restored", nil,
				); err != nil {
					return err
				}
			}
			if !locked || assignedMissing {
				if err := replaceBookFiles(
					ctx,
					transaction,
					bookID,
					rootID,
					candidate.SourceFileIDs,
					candidate.FileRoles,
				); err != nil {
					return err
				}
			}
		}
		presentBookIDs = append(presentBookIDs, bookID)
	}
	missingRows, err := transaction.Query(ctx, `
		update books
		set missing_at=now(), updated_at=now()
		where root_id=$1
		  and missing_at is null
		  and (cardinality($2::uuid[]) = 0 or not (id = any($2::uuid[])))
		returning id
	`, rootID, presentBookIDs)
	if err != nil {
		return fmt.Errorf("soft-mark absent books: %w", err)
	}
	var missingBookIDs []uuid.UUID
	for missingRows.Next() {
		var id uuid.UUID
		if err := missingRows.Scan(&id); err != nil {
			missingRows.Close()
			return fmt.Errorf("read absent book: %w", err)
		}
		missingBookIDs = append(missingBookIDs, id)
	}
	if err := missingRows.Err(); err != nil {
		missingRows.Close()
		return fmt.Errorf("read absent books: %w", err)
	}
	missingRows.Close()
	for _, id := range missingBookIDs {
		if err := insertAvailabilityEvent(
			ctx, transaction, rootID, "book", id, "missing", nil,
		); err != nil {
			return err
		}
	}
	return nil
}

func (repository *PostgresRepository) GetBook(
	ctx context.Context,
	id uuid.UUID,
) (BookDetail, error) {
	if id == uuid.Nil {
		return BookDetail{}, ErrInvalidData
	}
	book, err := scanBook(repository.pool.QueryRow(ctx, `
		select `+bookColumns("b")+` from books b where b.id=$1
	`, id))
	if err != nil {
		return BookDetail{}, mapNotFound(err, "get book")
	}
	detail := BookDetail{Book: book}
	fileRows, err := repository.pool.Query(ctx, `
		select f.id, f.root_id, f.relative_path, f.size, f.modified_at,
		       coalesce(f.sha256,''), f.media, f.scan_run_id, f.missing_at
		from book_files bf
		join source_files f on f.id=bf.file_id and f.root_id=bf.root_id
		where bf.book_id=$1
		order by f.relative_path, f.id
	`, id)
	if err != nil {
		return BookDetail{}, fmt.Errorf("list book files: %w", err)
	}
	for fileRows.Next() {
		file, err := scanSourceFile(fileRows)
		if err != nil {
			fileRows.Close()
			return BookDetail{}, err
		}
		detail.Files = append(detail.Files, file)
	}
	if err := fileRows.Err(); err != nil {
		fileRows.Close()
		return BookDetail{}, fmt.Errorf("iterate book files: %w", err)
	}
	fileRows.Close()
	matchRows, err := repository.pool.Query(ctx, `
		select id, book_id, provider, provider_id, score::float8, payload,
		       coalesce(cover_url,''), confirmed, created_at
		from metadata_matches
		where book_id=$1
		order by score desc, id
	`, id)
	if err != nil {
		return BookDetail{}, fmt.Errorf("list metadata matches: %w", err)
	}
	defer matchRows.Close()
	for matchRows.Next() {
		match, err := scanMetadataMatch(matchRows)
		if err != nil {
			return BookDetail{}, err
		}
		detail.Matches = append(detail.Matches, match)
	}
	if err := matchRows.Err(); err != nil {
		return BookDetail{}, fmt.Errorf("iterate metadata matches: %w", err)
	}
	return detail, nil
}

func (repository *PostgresRepository) GetGroupingContext(
	ctx context.Context,
	bookID uuid.UUID,
) (GroupingContext, error) {
	if repository == nil || repository.pool == nil || bookID == uuid.Nil {
		return GroupingContext{}, ErrInvalidData
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return GroupingContext{}, fmt.Errorf("begin grouping context: %w", err)
	}
	defer transaction.Rollback(ctx)

	result := GroupingContext{BookID: bookID, Evidence: map[string][]string{}}
	var encodedEvidence []byte
	if err := transaction.QueryRow(ctx, `
		select b.root_id, b.source_folder, coalesce(e.reason_code,''),
		       coalesce(e.evidence,'{}'::jsonb)
		from books b
		left join book_grouping_evidence e on e.book_id=b.id and e.root_id=b.root_id
		where b.id=$1 and b.missing_at is null
	`, bookID).Scan(&result.RootID, &result.ScopePath, &result.ReviewReason, &encodedEvidence); err != nil {
		return GroupingContext{}, mapNotFound(err, "load grouping context book")
	}
	result.SourceFolder = result.ScopePath
	if err := json.Unmarshal(encodedEvidence, &result.Evidence); err != nil {
		var wrapped struct {
			Values map[string][]string `json:"values"`
		}
		if wrappedErr := json.Unmarshal(encodedEvidence, &wrapped); wrappedErr != nil {
			return GroupingContext{}, fmt.Errorf("decode grouping evidence: %w", err)
		}
		result.Evidence = wrapped.Values
	}
	if result.Evidence == nil {
		result.Evidence = map[string][]string{}
	}

	fileRows, err := transaction.Query(ctx, `
		select id, root_id, relative_path, size, modified_at,
		       coalesce(sha256,''), media, scan_run_id, missing_at,
		       file_kind, coalesce(companion_kind,''), probe_status,
		       probe_version, coalesce(probe_error,''), probed_at
		from source_files
		where root_id=$1
		  and missing_at is null
		  and ($2='' or left(relative_path, length($2)+1)=$2 || '/')
		  and file_kind in ('audio','cover','companion')
		order by relative_path, id
	`, result.RootID, result.ScopePath)
	if err != nil {
		return GroupingContext{}, fmt.Errorf("list grouping context files: %w", err)
	}
	for fileRows.Next() {
		var file GroupingContextFile
		var media []byte
		if scanErr := fileRows.Scan(
			&file.ID, &file.RootID, &file.RelativePath, &file.Size,
			&file.ModifiedAt, &file.SHA256, &media, &file.ScanRunID,
			&file.MissingAt, &file.FileKind, &file.SourceFile.CompanionKind,
			&file.ProbeStatus, &file.ProbeVersion, &file.ProbeError, &file.ProbedAt,
		); scanErr != nil {
			fileRows.Close()
			return GroupingContext{}, fmt.Errorf("scan grouping context file: %w", scanErr)
		}
		if err := json.Unmarshal(media, &file.Media); err != nil {
			fileRows.Close()
			return GroupingContext{}, fmt.Errorf("decode grouping context media: %w", err)
		}
		file.CompanionKind = file.SourceFile.CompanionKind
		switch file.FileKind {
		case FileKindAudio:
			file.Role = FileRoleAudio
		case FileKindCover:
			file.Role, file.Shared = FileRoleCover, true
		case FileKindCompanion:
			file.Role = FileRoleSidecar
		}
		result.Files = append(result.Files, file)
	}
	if err := fileRows.Err(); err != nil {
		fileRows.Close()
		return GroupingContext{}, fmt.Errorf("iterate grouping context files: %w", err)
	}
	fileRows.Close()

	bookRows, err := transaction.Query(ctx, `
		select id, title, grouping_key
		from books
		where root_id=$1 and source_folder=$2 and missing_at is null
		order by lower(title), title, id
	`, result.RootID, result.ScopePath)
	if err != nil {
		return GroupingContext{}, fmt.Errorf("list grouping context books: %w", err)
	}
	bookIndexes := make(map[uuid.UUID]int)
	for bookRows.Next() {
		var sibling GroupingContextBook
		if err := bookRows.Scan(
			&sibling.BookID,
			&sibling.Title,
			&sibling.GroupingKey,
		); err != nil {
			bookRows.Close()
			return GroupingContext{}, fmt.Errorf("scan grouping context book: %w", err)
		}
		sibling.FileIDs = []uuid.UUID{}
		bookIndexes[sibling.BookID] = len(result.Books)
		result.Books = append(result.Books, sibling)
	}
	if err := bookRows.Err(); err != nil {
		bookRows.Close()
		return GroupingContext{}, fmt.Errorf("iterate grouping context books: %w", err)
	}
	bookRows.Close()

	availableFiles := make(map[uuid.UUID]struct{}, len(result.Files))
	for _, file := range result.Files {
		availableFiles[file.ID] = struct{}{}
	}
	assignmentRows, err := transaction.Query(ctx, `
		select bf.book_id, f.id, f.relative_path
		from books b
		join book_files bf on bf.book_id=b.id and bf.root_id=b.root_id
		join source_files f on f.id=bf.file_id and f.root_id=bf.root_id
		where b.root_id=$1
		  and b.source_folder=$2
		  and b.missing_at is null
		  and f.missing_at is null
		order by b.id, f.relative_path, f.id
	`, result.RootID, result.ScopePath)
	if err != nil {
		return GroupingContext{}, fmt.Errorf("list grouping context assignments: %w", err)
	}
	for assignmentRows.Next() {
		var siblingID, fileID uuid.UUID
		var relativePath string
		if err := assignmentRows.Scan(&siblingID, &fileID, &relativePath); err != nil {
			assignmentRows.Close()
			return GroupingContext{}, fmt.Errorf("scan grouping context assignment: %w", err)
		}
		index, exists := bookIndexes[siblingID]
		_, available := availableFiles[fileID]
		if exists && available {
			result.Books[index].FileIDs = append(result.Books[index].FileIDs, fileID)
		}
	}
	if err := assignmentRows.Err(); err != nil {
		assignmentRows.Close()
		return GroupingContext{}, fmt.Errorf("iterate grouping context assignments: %w", err)
	}
	assignmentRows.Close()

	if err := transaction.Commit(ctx); err != nil {
		return GroupingContext{}, fmt.Errorf("commit grouping context: %w", err)
	}
	if result.Files == nil {
		result.Files = []GroupingContextFile{}
	}
	if result.Books == nil {
		result.Books = []GroupingContextBook{}
	}
	return result, nil
}

func (repository *PostgresRepository) ListCatalogFilters(
	ctx context.Context,
) (FilterSnapshot, error) {
	var encoded []byte
	if err := repository.pool.QueryRow(ctx, `
		select jsonb_build_object(
			'authors', coalesce((
				select jsonb_agg(value order by lower(value),value)
				from (
					select min(btrim(author) collate "C") as value
					from books b cross join lateral unnest(b.authors) author
					where b.missing_at is null and btrim(author)<>''
					group by lower(btrim(author))
				) snapshot_values
			),'[]'::jsonb),
			'genres', coalesce((
				select jsonb_agg(value order by lower(value),value)
				from (
					select min(btrim(genre) collate "C") as value
					from books b cross join lateral unnest(b.genres) genre
					where b.missing_at is null and btrim(genre)<>''
					group by lower(btrim(genre))
				) snapshot_values
			),'[]'::jsonb),
			'years', coalesce((
				select jsonb_agg(value order by value)
				from (
					select distinct publication_year as value
					from books
					where missing_at is null and publication_year is not null
				) snapshot_values
			),'[]'::jsonb),
			'roots', coalesce((
				select jsonb_agg(id order by id) from library_roots
			),'[]'::jsonb),
			'reviewStates', coalesce((
				select jsonb_agg(value order by value)
				from (
					select distinct review_state as value
					from books where missing_at is null
				) snapshot_values
			),'[]'::jsonb)
		)
	`).Scan(&encoded); err != nil {
		return FilterSnapshot{}, fmt.Errorf("list catalog filters: %w", err)
	}
	var filters FilterSnapshot
	if err := json.Unmarshal(encoded, &filters); err != nil {
		return FilterSnapshot{}, fmt.Errorf("decode catalog filters: %w", err)
	}
	if filters.Authors == nil {
		filters.Authors = []string{}
	}
	if filters.Genres == nil {
		filters.Genres = []string{}
	}
	if filters.Years == nil {
		filters.Years = []int{}
	}
	if filters.Roots == nil {
		filters.Roots = []string{}
	}
	if filters.ReviewStates == nil {
		filters.ReviewStates = []string{}
	}
	return filters, nil
}

func (repository *PostgresRepository) SaveMatches(
	ctx context.Context,
	bookID uuid.UUID,
	matches []MetadataMatch,
) error {
	if bookID == uuid.Nil {
		return ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin metadata match save: %w", err)
	}
	defer transaction.Rollback(ctx)
	var exists bool
	if err := transaction.QueryRow(
		ctx,
		`select exists(select 1 from books where id=$1 for update)`,
		bookID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("lock matched book: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	if _, err := transaction.Exec(
		ctx,
		`delete from metadata_matches where book_id=$1 and not confirmed`,
		bookID,
	); err != nil {
		return fmt.Errorf("clear unconfirmed metadata matches: %w", err)
	}
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if match.ID == uuid.Nil || match.BookID != bookID ||
			(match.Provider != MetadataOpenLibrary && match.Provider != MetadataGoogleBooks) ||
			strings.TrimSpace(match.ProviderID) == "" ||
			math.IsNaN(match.Score) || match.Score < 0 || match.Score > 1 {
			return ErrInvalidData
		}
		key := match.Provider + "\x00" + match.ProviderID
		if _, exists := seen[key]; exists {
			return ErrInvalidData
		}
		seen[key] = struct{}{}
		payload, err := marshalObject(match.Payload)
		if err != nil {
			return err
		}
		if _, err := transaction.Exec(ctx, `
			insert into metadata_matches(
				id, book_id, provider, provider_id, score, payload, cover_url, confirmed
			)
			values ($1,$2,$3,$4,$5,$6,nullif($7,''),false)
			on conflict (book_id, provider, provider_id) do update
			set score=excluded.score,
			    payload=excluded.payload,
			    cover_url=excluded.cover_url
			where not metadata_matches.confirmed
		`, match.ID, bookID, match.Provider, match.ProviderID, match.Score,
			payload, match.CoverURL); err != nil {
			return classifyConstraint(err, "save metadata match")
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit metadata match save: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) ConfirmMatch(
	ctx context.Context,
	bookID uuid.UUID,
	matchID uuid.UUID,
) error {
	if bookID == uuid.Nil || matchID == uuid.Nil {
		return ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin metadata confirmation: %w", err)
	}
	defer transaction.Rollback(ctx)
	var matchBookID uuid.UUID
	var provider string
	var providerID string
	var payload []byte
	var score float64
	if err := transaction.QueryRow(ctx, `
		select book_id,provider,provider_id,payload,score::float8
		from metadata_matches where id=$1 for update
	`, matchID).Scan(&matchBookID, &provider, &providerID, &payload, &score); err != nil {
		return mapNotFound(err, "load metadata match")
	}
	if matchBookID != bookID {
		return ErrConflict
	}
	if provider != MetadataOpenLibrary && provider != MetadataGoogleBooks {
		return ErrInvalidData
	}
	candidate, err := decodeConfirmedCandidate(payload, provider, providerID)
	if err != nil {
		return err
	}
	result, err := transaction.Exec(ctx, `
		update books
		set confirmed_match_id=null
		where id=$1 and missing_at is null
	`, bookID)
	if err != nil {
		return fmt.Errorf("temporarily clear confirmed match: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	if _, err := transaction.Exec(
		ctx,
		`update metadata_matches set confirmed=false where book_id=$1`,
		bookID,
	); err != nil {
		return fmt.Errorf("clear metadata confirmations: %w", err)
	}
	if _, err := transaction.Exec(
		ctx,
		`update metadata_matches set confirmed=true where id=$1 and book_id=$2`,
		matchID,
		bookID,
	); err != nil {
		return fmt.Errorf("confirm metadata match: %w", err)
	}
	result, err = transaction.Exec(ctx, `
		update books
		set confirmed_match_id=$2,
		    review_state='confirmed',
		    metadata_source='confirmed',
		    title=case when btrim($3)<>'' then $3 else title end,
		    subtitle=case when btrim($4)<>'' then $4 else subtitle end,
		    authors=case when cardinality($5::text[])>0 then $5 else authors end,
		    narrators=case when cardinality($6::text[])>0 then $6 else narrators end,
		    series=case when btrim($7)<>'' then $7 else series end,
		    series_position=case when btrim($8)<>'' then $8 else series_position end,
		    publisher=case when btrim($9)<>'' then $9 else publisher end,
		    published_date=case when btrim($10)<>'' then $10 else published_date end,
		    publication_year=coalesce($11,publication_year),
		    genres=case when cardinality($12::text[])>0 then $12 else genres end,
		    provider_subjects=case
		        when cardinality($13::text[])>0 then $13
		        else provider_subjects
		    end,
		    identifiers=identifiers || $14::jsonb,
		    confidence=greatest(confidence,$15),
		    updated_at=now()
		where id=$1 and missing_at is null
	`, bookID, matchID, candidate.Title, candidate.Subtitle,
		candidate.Authors, candidate.Narrators, candidate.Series,
		candidate.SeriesPosition, candidate.Publisher, candidate.PublishedDate,
		candidate.PublicationYear, candidate.Genres, candidate.Subjects,
		candidate.Identifiers, score)
	if err != nil {
		return classifyConstraint(err, "record confirmed metadata match")
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit metadata confirmation: %w", err)
	}
	return nil
}

type confirmedCandidatePayload struct {
	Title           string   `json:"title"`
	Subtitle        string   `json:"subtitle"`
	Authors         []string `json:"authors"`
	Narrators       []string `json:"narrators"`
	Series          string   `json:"series"`
	SeriesPosition  string   `json:"seriesPosition"`
	Publisher       string   `json:"publisher"`
	PublishedDate   string   `json:"publishedDate"`
	Genres          []string `json:"genres"`
	Subjects        []string `json:"subjects"`
	ISBN            string   `json:"isbn"`
	PublicationYear *int     `json:"-"`
	Identifiers     string   `json:"-"`
}

func decodeConfirmedCandidate(
	payload []byte,
	provider string,
	providerID string,
) (confirmedCandidatePayload, error) {
	if len(payload) > 256<<10 || strings.TrimSpace(providerID) == "" ||
		len(providerID) > 500 || strings.IndexFunc(providerID, unicode.IsControl) >= 0 {
		return confirmedCandidatePayload{}, ErrInvalidData
	}
	var candidate confirmedCandidatePayload
	if err := json.Unmarshal(payload, &candidate); err != nil {
		return confirmedCandidatePayload{}, ErrInvalidData
	}
	candidate.Title = strings.TrimSpace(candidate.Title)
	candidate.Subtitle = strings.TrimSpace(candidate.Subtitle)
	candidate.Series = strings.TrimSpace(candidate.Series)
	candidate.SeriesPosition = strings.TrimSpace(candidate.SeriesPosition)
	candidate.Publisher = strings.TrimSpace(candidate.Publisher)
	candidate.PublishedDate = strings.TrimSpace(candidate.PublishedDate)
	for _, value := range []struct {
		text string
		max  int
	}{
		{candidate.Title, 1000}, {candidate.Subtitle, 2000},
		{candidate.Series, 1000}, {candidate.SeriesPosition, 100},
		{candidate.Publisher, 1000}, {candidate.PublishedDate, 64},
		{candidate.ISBN, 64},
	} {
		if len(value.text) > value.max ||
			strings.IndexFunc(value.text, unicode.IsControl) >= 0 {
			return confirmedCandidatePayload{}, ErrInvalidData
		}
	}
	var err error
	if candidate.Authors, err = cleanCandidateStrings(candidate.Authors); err != nil {
		return confirmedCandidatePayload{}, err
	}
	if candidate.Narrators, err = cleanCandidateStrings(candidate.Narrators); err != nil {
		return confirmedCandidatePayload{}, err
	}
	if candidate.Genres, err = cleanCandidateStrings(candidate.Genres); err != nil {
		return confirmedCandidatePayload{}, err
	}
	if candidate.Subjects, err = cleanCandidateStrings(candidate.Subjects); err != nil {
		return confirmedCandidatePayload{}, err
	}
	if len(candidate.PublishedDate) >= 4 {
		if year, err := strconv.Atoi(candidate.PublishedDate[:4]); err == nil &&
			year >= 1000 && year <= 3000 {
			candidate.PublicationYear = &year
		}
	}
	identifiers := map[string]string{}
	if isbn := strings.TrimSpace(candidate.ISBN); isbn != "" {
		identifiers["isbn"] = isbn
	}
	identifiers[provider] = strings.TrimSpace(providerID)
	encoded, err := json.Marshal(identifiers)
	if err != nil {
		return confirmedCandidatePayload{}, ErrInvalidData
	}
	candidate.Identifiers = string(encoded)
	return candidate, nil
}

func cleanCandidateStrings(values []string) ([]string, error) {
	if len(values) > 100 {
		return nil, ErrInvalidData
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if len(value) > 500 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return nil, ErrInvalidData
		}
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func (repository *PostgresRepository) SaveGrouping(
	ctx context.Context,
	correction GroupingCorrection,
) (CatalogGeneration, error) {
	var zero CatalogGeneration
	if correction.ID == uuid.Nil || !rootIDPattern.MatchString(correction.RootID) ||
		ValidateSourceFolder(correction.SourceFolder) != nil || len(correction.Groups) == 0 {
		return zero, ErrInvalidData
	}
	legacy := len(correction.ExpectedFiles) == 0
	assignmentCounts := make(map[uuid.UUID]int)
	groupingKeys := make(map[string]struct{}, len(correction.Groups))
	seenBookIDs := make(map[uuid.UUID]struct{}, len(correction.Groups))
	for _, group := range correction.Groups {
		if strings.TrimSpace(group.GroupingKey) == "" || len(group.FileIDs) == 0 {
			return zero, ErrInvalidData
		}
		if _, exists := groupingKeys[group.GroupingKey]; exists {
			return zero, ErrInvalidData
		}
		groupingKeys[group.GroupingKey] = struct{}{}
		if group.BookID != nil {
			if *group.BookID == uuid.Nil {
				return zero, ErrInvalidData
			}
			if _, exists := seenBookIDs[*group.BookID]; exists {
				return zero, ErrInvalidData
			}
			seenBookIDs[*group.BookID] = struct{}{}
		}
		groupFiles := make(map[uuid.UUID]struct{}, len(group.FileIDs))
		for _, id := range group.FileIDs {
			if id == uuid.Nil {
				return zero, ErrInvalidData
			}
			if _, duplicate := groupFiles[id]; duplicate {
				return zero, ErrInvalidData
			}
			groupFiles[id] = struct{}{}
			assignmentCounts[id]++
			if legacy && assignmentCounts[id] > 1 {
				return zero, ErrInvalidData
			}
		}
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin grouping save: %w", err)
	}
	defer transaction.Rollback(ctx)
	folderFiles, err := loadGroupingFolderFiles(
		ctx,
		transaction,
		correction.RootID,
		correction.SourceFolder,
	)
	if err != nil {
		return zero, fmt.Errorf("list grouping folder files: %w", err)
	}
	if legacy {
		folderFiles = directGroupingFolderFiles(folderFiles, correction.SourceFolder)
	}
	filesByID := make(map[uuid.UUID]groupingFolderFile, len(folderFiles))
	for _, file := range folderFiles {
		filesByID[file.id] = file
	}
	if err := validateGroupingFingerprints(correction.ExpectedFiles, folderFiles); err != nil {
		return zero, err
	}
	presentAudio := 0
	for _, file := range folderFiles {
		role := groupingFileRole(file)
		count := assignmentCounts[file.id]
		switch role {
		case FileRoleAudio:
			presentAudio++
			if count != 1 {
				return zero, ErrConflict
			}
		case FileRoleSidecar:
			if !legacy && count != 1 {
				return zero, ErrConflict
			}
		case FileRoleCover:
			if !legacy && count < 1 {
				return zero, ErrConflict
			}
		default:
			if count != 0 {
				return zero, ErrConflict
			}
		}
	}
	if presentAudio == 0 {
		return zero, ErrConflict
	}
	for id, count := range assignmentCounts {
		file, exists := filesByID[id]
		if !exists {
			return zero, ErrConflict
		}
		role := groupingFileRole(file)
		if role == "" || (role != FileRoleCover && count != 1) ||
			(role == FileRoleCover && legacy) {
			return zero, ErrConflict
		}
	}
	bookIDs := make([]uuid.UUID, 0, len(correction.Groups))
	for index := range correction.Groups {
		group := &correction.Groups[index]
		audioIDs := make([]uuid.UUID, 0, len(group.FileIDs))
		for _, fileID := range group.FileIDs {
			if groupingFileRole(filesByID[fileID]) == FileRoleAudio {
				audioIDs = append(audioIDs, fileID)
			}
		}
		if len(audioIDs) == 0 {
			return zero, ErrConflict
		}
		var matchedBookID uuid.UUID
		if group.BookID == nil {
			matchedBookID, err = createCorrectedBook(
				ctx,
				transaction,
				correction.RootID,
				correction.SourceFolder,
				group.GroupingKey,
				audioIDs,
			)
			if err != nil {
				return zero, err
			}
			group.BookID = &matchedBookID
		} else {
			if err := transaction.QueryRow(ctx, `
				select id
				from books
				where id=$1 and root_id=$2 and source_folder=$3 and grouping_key=$4
				for update
			`, *group.BookID, correction.RootID, correction.SourceFolder,
				group.GroupingKey).Scan(&matchedBookID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return zero, ErrConflict
				}
				return zero, fmt.Errorf("validate grouping book: %w", err)
			}
		}
		bookIDs = append(bookIDs, matchedBookID)
		selectedIDs := group.FileIDs
		if legacy {
			selectedIDs = audioIDs
		}
		assignmentIDs, roles, err := groupingBookFiles(selectedIDs, folderFiles)
		if err != nil {
			return zero, err
		}
		if err := replaceBookFiles(
			ctx,
			transaction,
			matchedBookID,
			correction.RootID,
			assignmentIDs,
			roles,
		); err != nil {
			return zero, err
		}
		if err := upsertManualGroupingEvidence(
			ctx, transaction, matchedBookID, correction.RootID,
			assignmentIDs, roles, filesByID,
		); err != nil {
			return zero, err
		}
	}
	groups, err := json.Marshal(correction.Groups)
	if err != nil {
		return zero, ErrInvalidData
	}
	if _, err := transaction.Exec(ctx, `
		insert into grouping_corrections(id, root_id, source_folder, groups, updated_at)
		values ($1,$2,$3,$4,now())
		on conflict (root_id, source_folder) do update
		set groups=excluded.groups, updated_at=now()
	`, correction.ID, correction.RootID, correction.SourceFolder, groups); err != nil {
		return zero, classifyConstraint(err, "save grouping correction")
	}
	if _, err := transaction.Exec(ctx, `
		update books
		set grouping_locked=true, review_state='confirmed', missing_at=null, updated_at=now()
		where id=any($1::uuid[])
	`, bookIDs); err != nil {
		return zero, fmt.Errorf("lock grouping: %w", err)
	}
	missingRows, err := transaction.Query(ctx, `
		update books
		set missing_at=now(), updated_at=now()
		where root_id=$1
		  and source_folder=$2
		  and missing_at is null
		  and not (id=any($3::uuid[]))
		returning id
	`, correction.RootID, correction.SourceFolder, bookIDs)
	if err != nil {
		return zero, fmt.Errorf("soft-mark superseded grouped books: %w", err)
	}
	var superseded []uuid.UUID
	for missingRows.Next() {
		var id uuid.UUID
		if err := missingRows.Scan(&id); err != nil {
			missingRows.Close()
			return zero, fmt.Errorf("scan superseded grouped book: %w", err)
		}
		superseded = append(superseded, id)
	}
	if err := missingRows.Err(); err != nil {
		missingRows.Close()
		return zero, fmt.Errorf("iterate superseded grouped books: %w", err)
	}
	missingRows.Close()
	for _, id := range superseded {
		if err := insertAvailabilityEvent(
			ctx,
			transaction,
			correction.RootID,
			"book",
			id,
			"missing",
			nil,
		); err != nil {
			return zero, err
		}
	}
	var generation CatalogGeneration
	if err := transaction.QueryRow(ctx, `
		update catalog_generations
		set generation=generation+1, updated_at=now()
		where root_id=$1
		returning root_id,generation,updated_at
	`, correction.RootID).Scan(
		&generation.RootID, &generation.Value, &generation.UpdatedAt,
	); err != nil {
		return zero, fmt.Errorf("advance grouping generation: %w", err)
	}
	jobID := uuid.New()
	payload, err := marshalObject(map[string]any{
		"rootId": correction.RootID, "catalogGeneration": generation.Value,
	})
	if err != nil {
		return zero, err
	}
	if _, err := transaction.Exec(ctx, `
		insert into worker_jobs(
			id, kind, dedupe_key, payload, run_after, max_attempts
		)
		values ($1,$2,$3,$4,$5,5)
		on conflict (kind,dedupe_key)
			where dedupe_key is not null and status in ('queued','running')
		do nothing
	`, jobID, JobPublishSearch, "publish_search:"+correction.RootID,
		payload, repository.now()); err != nil {
		return zero, classifyConstraint(err, "queue grouping search publication")
	}
	if err := transaction.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit grouping save: %w", err)
	}
	return generation, nil
}

func directGroupingFolderFiles(files []groupingFolderFile, sourceFolder string) []groupingFolderFile {
	direct := make([]groupingFolderFile, 0, len(files))
	for _, file := range files {
		folder := path.Dir(file.relativePath)
		if folder == "." {
			folder = ""
		}
		if folder == sourceFolder {
			direct = append(direct, file)
		}
	}
	return direct
}

func validateGroupingFingerprints(expected []GroupingFileFingerprint, files []groupingFolderFile) error {
	if len(expected) == 0 {
		return nil
	}
	if len(expected) != len(files) {
		return ErrConflict
	}
	filesByID := make(map[uuid.UUID]groupingFolderFile, len(files))
	for _, file := range files {
		filesByID[file.id] = file
	}
	seen := make(map[uuid.UUID]struct{}, len(expected))
	for _, fingerprint := range expected {
		file, exists := filesByID[fingerprint.FileID]
		if fingerprint.FileID == uuid.Nil || fingerprint.Size < 0 ||
			fingerprint.ModifiedAt.IsZero() {
			return ErrInvalidData
		}
		if _, duplicate := seen[fingerprint.FileID]; duplicate {
			return ErrInvalidData
		}
		seen[fingerprint.FileID] = struct{}{}
		if !exists || file.size != fingerprint.Size ||
			!file.modifiedAt.Equal(fingerprint.ModifiedAt) {
			return ErrConflict
		}
	}
	return nil
}

func upsertManualGroupingEvidence(
	ctx context.Context,
	transaction pgx.Tx,
	bookID uuid.UUID,
	rootID string,
	fileIDs []uuid.UUID,
	roles map[uuid.UUID]string,
	filesByID map[uuid.UUID]groupingFolderFile,
) error {
	values := map[string][]string{
		FileRoleAudio: {}, FileRoleCover: {}, FileRoleSidecar: {},
	}
	for _, fileID := range fileIDs {
		role := roles[fileID]
		values[role] = append(values[role], filesByID[fileID].relativePath)
	}
	for role := range values {
		sort.Strings(values[role])
	}
	evidence, err := json.Marshal(values)
	if err != nil {
		return ErrInvalidData
	}
	if _, err := transaction.Exec(ctx, `
		insert into book_grouping_evidence(book_id,root_id,reason_code,evidence,updated_at)
		values ($1,$2,'manual',$3,now())
		on conflict (book_id) do update
		set root_id=excluded.root_id, reason_code='manual',
		    evidence=excluded.evidence, updated_at=now()
	`, bookID, rootID, evidence); err != nil {
		return classifyConstraint(err, "save manual grouping evidence")
	}
	return nil
}

func isAudioPath(relativePath string) bool {
	switch strings.ToLower(path.Ext(relativePath)) {
	case ".m4b", ".m4a", ".mp3", ".aac", ".flac", ".ogg", ".opus", ".wav":
		return true
	default:
		return false
	}
}

type groupingFolderFile struct {
	id            uuid.UUID
	relativePath  string
	size          int64
	modifiedAt    time.Time
	fileKind      string
	companionKind string
}

func loadGroupingFolderFiles(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	sourceFolder string,
) ([]groupingFolderFile, error) {
	rows, err := transaction.Query(ctx, `
		select id, relative_path, size, modified_at, file_kind,
		       coalesce(companion_kind,'')
		from source_files
		where root_id=$1
		  and missing_at is null
		  and ($2='' or left(relative_path, length($2)+1)=$2 || '/')
		  and file_kind in ('audio','cover','companion')
		order by relative_path, id
	`, rootID, sourceFolder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make([]groupingFolderFile, 0)
	for rows.Next() {
		var file groupingFolderFile
		if err := rows.Scan(
			&file.id, &file.relativePath, &file.size, &file.modifiedAt,
			&file.fileKind, &file.companionKind,
		); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

func groupingBookFiles(
	selectedIDs []uuid.UUID,
	folderFiles []groupingFolderFile,
) ([]uuid.UUID, map[uuid.UUID]string, error) {
	filesByID := make(map[uuid.UUID]groupingFolderFile, len(folderFiles))
	for _, file := range folderFiles {
		filesByID[file.id] = file
	}
	fileIDs := make([]uuid.UUID, 0, len(selectedIDs)+1)
	roles := make(map[uuid.UUID]string, len(selectedIDs)+1)
	audioStems := make(map[string]struct{}, len(selectedIDs))
	explicitNonAudio := false
	for _, id := range selectedIDs {
		file, exists := filesByID[id]
		if !exists {
			return nil, nil, ErrConflict
		}
		role := groupingFileRole(file)
		if role == "" {
			return nil, nil, ErrConflict
		}
		fileIDs = append(fileIDs, id)
		roles[id] = role
		if role == FileRoleAudio {
			audioStems[fileStem(file.relativePath)] = struct{}{}
		} else {
			explicitNonAudio = true
		}
	}
	if explicitNonAudio {
		return fileIDs, roles, nil
	}
	for _, file := range folderFiles {
		if groupingFileRole(file) == FileRoleAudio {
			continue
		}
		role := ""
		if isRecognizedSharedCover(file.relativePath) {
			role = FileRoleCover
		} else if _, matchesAudio := audioStems[fileStem(file.relativePath)]; matchesAudio {
			role = FileRoleSidecar
		}
		if role == "" {
			continue
		}
		fileIDs = append(fileIDs, file.id)
		roles[file.id] = role
	}
	return fileIDs, roles, nil
}

func groupingFileRole(file groupingFolderFile) string {
	switch file.fileKind {
	case FileKindAudio:
		if isAudioPath(file.relativePath) {
			return FileRoleAudio
		}
		if isRecognizedSharedCover(file.relativePath) {
			return FileRoleCover
		}
		return ""
	case FileKindCover:
		return FileRoleCover
	case FileKindCompanion:
		return FileRoleSidecar
	default:
		return ""
	}
}

func fileStem(relativePath string) string {
	base := path.Base(relativePath)
	return strings.ToLower(strings.TrimSuffix(base, path.Ext(base)))
}

func isRecognizedSharedCover(relativePath string) bool {
	extension := strings.ToLower(path.Ext(relativePath))
	switch extension {
	case ".jpg", ".jpeg", ".png", ".webp":
	default:
		return false
	}
	switch fileStem(relativePath) {
	case "cover", "folder", "front":
		return true
	default:
		return false
	}
}

func createCorrectedBook(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	sourceFolder string,
	groupingKey string,
	fileIDs []uuid.UUID,
) (uuid.UUID, error) {
	var existingID uuid.UUID
	if err := transaction.QueryRow(ctx, `
		select id
		from books
		where root_id=$1 and source_folder=$2 and grouping_key=$3
		for update
	`, rootID, sourceFolder, groupingKey).Scan(&existingID); err == nil {
		if _, err := transaction.Exec(ctx, `
			update books
			set grouping_locked=true,
			    review_state='needs_review',
			    confirmed_match_id=null,
			    missing_at=null,
			    updated_at=now()
			where id=$1
		`, existingID); err != nil {
			return uuid.Nil, fmt.Errorf("restore corrected book: %w", err)
		}
		return existingID, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("find corrected book: %w", err)
	}

	var mediaTitle, mediaAuthor string
	var mediaTitleCount, mediaAuthorCount int
	if err := transaction.QueryRow(ctx, `
		select
		    count(distinct nullif(btrim(media->>'title'),'')),
		    coalesce(min(nullif(btrim(media->>'title'),'')),''),
		    count(distinct nullif(btrim(media->>'author'),'')),
		    coalesce(min(nullif(btrim(media->>'author'),'')),'')
		from source_files
		where root_id=$1 and id=any($2::uuid[]) and missing_at is null
	`, rootID, fileIDs).Scan(
		&mediaTitleCount,
		&mediaTitle,
		&mediaAuthorCount,
		&mediaAuthor,
	); err != nil {
		return uuid.Nil, fmt.Errorf("load corrected audio identity: %w", err)
	}
	title := ""
	if mediaTitleCount == 1 {
		title = mediaTitle
	}
	if title == "" {
		var err error
		title, err = firstAudioStem(ctx, transaction, rootID, fileIDs)
		if err != nil {
			return uuid.Nil, err
		}
	}
	authors := []string{}
	if mediaAuthorCount == 1 {
		authors = []string{mediaAuthor}
	}
	metadataSource := MetadataInferred
	if mediaTitleCount == 1 || mediaAuthorCount == 1 {
		metadataSource = MetadataLocal
	}
	inferred := InferredBook{
		RootID:         rootID,
		SourceFolder:   sourceFolder,
		GroupingKey:    groupingKey,
		Title:          title,
		Authors:        authors,
		MetadataSource: metadataSource,
		Confidence:     0.5,
		ReviewState:    ReviewNeedsReview,
		SourceFileIDs:  fileIDs,
	}

	var distinctTitles int
	if err := transaction.QueryRow(ctx, `
		select count(distinct b.title)
		from books b
		join book_files bf on bf.book_id=b.id
		where b.root_id=$1
		  and b.source_folder=$2
		  and bf.file_id=any($3::uuid[])
		  and btrim(b.title)<>''
	`, rootID, sourceFolder, fileIDs).Scan(&distinctTitles); err != nil {
		return uuid.Nil, fmt.Errorf("inspect corrected book metadata: %w", err)
	}
	if distinctTitles == 1 {
		template, err := scanBook(transaction.QueryRow(ctx, `
			select `+bookColumns("b")+`
			from books b
			join book_files bf on bf.book_id=b.id
			where b.root_id=$1
			  and b.source_folder=$2
			  and bf.file_id=any($3::uuid[])
			order by
			    case b.metadata_source
			        when 'local' then 0
			        when 'confirmed' then 1
			        else 2
			    end,
			    b.created_at,
			    b.id
			limit 1
			for update of b
		`, rootID, sourceFolder, fileIDs))
		if err != nil {
			return uuid.Nil, fmt.Errorf("load corrected book template: %w", err)
		}
		if strings.EqualFold(strings.TrimSpace(template.Title), strings.TrimSpace(title)) {
			inferred.Subtitle = template.Subtitle
			if len(inferred.Authors) == 0 {
				inferred.Authors = template.Authors
			}
			inferred.Narrators = template.Narrators
			inferred.Series = template.Series
			inferred.SeriesPosition = template.SeriesPosition
			inferred.Publisher = template.Publisher
			inferred.PublishedDate = template.PublishedDate
			inferred.PublicationYear = template.PublicationYear
			inferred.Genres = template.Genres
			inferred.ProviderSubjects = template.ProviderSubjects
			inferred.Identifiers = template.Identifiers
			if template.MetadataSource == MetadataLocal {
				inferred.MetadataSource = MetadataLocal
			}
			inferred.Confidence = template.Confidence
		}
	}
	id := uuid.New()
	if err := insertInferredBook(ctx, transaction, id, inferred, true); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func firstAudioStem(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	fileIDs []uuid.UUID,
) (string, error) {
	var relativePath string
	if err := transaction.QueryRow(ctx, `
		select relative_path
		from source_files
		where root_id=$1 and id=any($2::uuid[]) and missing_at is null
		order by relative_path, id
		limit 1
	`, rootID, fileIDs).Scan(&relativePath); err != nil {
		return "", mapNotFound(err, "load corrected audio title")
	}
	base := path.Base(relativePath)
	title := strings.TrimSpace(strings.TrimSuffix(base, path.Ext(base)))
	if title == "" {
		title = "Audiobook"
	}
	return title, nil
}

func (repository *PostgresRepository) Enqueue(
	ctx context.Context,
	job WorkerJob,
) error {
	if job.ID == uuid.Nil || !jobKinds[job.Kind] {
		return ErrInvalidData
	}
	payload, err := marshalObject(job.Payload)
	if err != nil {
		return err
	}
	if job.RunAfter.IsZero() {
		job.RunAfter = repository.now()
	}
	if job.MaxAttempts == 0 {
		job.MaxAttempts = 5
	}
	if job.MaxAttempts < 1 || job.MaxAttempts > 100 {
		return ErrInvalidData
	}
	_, err = repository.pool.Exec(ctx, `
		insert into worker_jobs(id, kind, payload, run_after, max_attempts)
		values ($1,$2,$3,$4,$5)
	`, job.ID, job.Kind, payload, job.RunAfter, job.MaxAttempts)
	if err != nil {
		return classifyConstraint(err, "enqueue worker job")
	}
	return nil
}

func (repository *PostgresRepository) EnqueueDeduplicated(
	ctx context.Context,
	job WorkerJob,
	dedupeKey string,
) (WorkerJob, error) {
	dedupeKey = strings.TrimSpace(dedupeKey)
	if job.ID == uuid.Nil || !jobKinds[job.Kind] || dedupeKey == "" ||
		len(dedupeKey) > 256 || strings.IndexFunc(dedupeKey, unicode.IsControl) >= 0 {
		return WorkerJob{}, ErrInvalidData
	}
	payload, err := marshalObject(job.Payload)
	if err != nil {
		return WorkerJob{}, err
	}
	if job.RunAfter.IsZero() {
		job.RunAfter = repository.now()
	}
	if job.MaxAttempts == 0 {
		job.MaxAttempts = 5
	}
	if job.MaxAttempts < 1 || job.MaxAttempts > 100 {
		return WorkerJob{}, ErrInvalidData
	}
	row := repository.pool.QueryRow(ctx, `
		insert into worker_jobs(
			id, kind, dedupe_key, payload, run_after, max_attempts
		)
		values ($1,$2,$3,$4,$5,$6)
		on conflict (kind,dedupe_key)
		    where dedupe_key is not null and status in ('queued','running')
		do update set updated_at=worker_jobs.updated_at
		returning id, kind, payload, status, run_after, attempts, max_attempts,
		          locked_at, coalesce(locked_by,''), lease_until, lease_token,
		          coalesce(last_error,''), created_at, updated_at
	`, job.ID, job.Kind, dedupeKey, payload, job.RunAfter, job.MaxAttempts)
	enqueued, err := scanWorkerJob(row)
	if err != nil {
		return WorkerJob{}, classifyConstraint(err, "enqueue deduplicated worker job")
	}
	return enqueued, nil
}

func (repository *PostgresRepository) ClaimJob(
	ctx context.Context,
	workerID string,
	now time.Time,
) (WorkerJob, error) {
	if strings.TrimSpace(workerID) == "" || len(workerID) > 200 || now.IsZero() {
		return WorkerJob{}, ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return WorkerJob{}, fmt.Errorf("begin worker job claim: %w", err)
	}
	defer transaction.Rollback(ctx)
	leaseToken := uuid.New()
	row := transaction.QueryRow(ctx, `
		with candidate as (
			select id
			from worker_jobs
			where status='queued' and run_after <= $1
			order by run_after, created_at, id
			for update skip locked
			limit 1
		)
		update worker_jobs job
		set status='running',
		    locked_at=$1,
		    locked_by=$2,
		    lease_until=$3,
		    lease_token=$4,
		    updated_at=$1
		from candidate
		where job.id=candidate.id
		returning job.id, job.kind, job.payload, job.status, job.run_after,
		          job.attempts, job.max_attempts, job.locked_at, job.locked_by,
		          job.lease_until, job.lease_token, coalesce(job.last_error,''),
		          job.created_at, job.updated_at
	`, now, workerID, now.Add(jobLease), leaseToken)
	job, err := scanWorkerJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkerJob{}, ErrNoJob
	}
	if err != nil {
		return WorkerJob{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return WorkerJob{}, fmt.Errorf("commit worker job claim: %w", err)
	}
	return job, nil
}

func (repository *PostgresRepository) RecoverExpiredJobs(
	ctx context.Context,
	now time.Time,
) error {
	if repository == nil || repository.pool == nil || now.IsZero() {
		return ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin expired worker job recovery: %w", err)
	}
	defer transaction.Rollback(ctx)

	rows, err := transaction.Query(ctx, `
		select id, attempts, max_attempts
		from worker_jobs
		where status='running' and lease_until < $1
		order by lease_until, created_at, id
		for update
	`, now)
	if err != nil {
		return fmt.Errorf("load expired worker jobs: %w", err)
	}
	type expiredJob struct {
		id                    uuid.UUID
		attempts, maxAttempts int
	}
	var expired []expiredJob
	for rows.Next() {
		var job expiredJob
		if err := rows.Scan(&job.id, &job.attempts, &job.maxAttempts); err != nil {
			rows.Close()
			return fmt.Errorf("scan expired worker job: %w", err)
		}
		expired = append(expired, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate expired worker jobs: %w", err)
	}
	rows.Close()

	for _, job := range expired {
		attempts := job.attempts + 1
		status := JobQueued
		runAfter := now.Add(retryDelay(attempts))
		lastError := "worker lease expired"
		if attempts >= job.maxAttempts {
			status = JobDead
			runAfter = now
			lastError = "worker lease expired at maximum attempts"
		}
		if _, err := transaction.Exec(ctx, `
			update worker_jobs
			set status=$2,
			    attempts=$3,
			    run_after=$4,
			    locked_at=null,
			    locked_by=null,
			    lease_until=null,
			    lease_token=null,
			    last_error=$5,
			    updated_at=$6
			where id=$1
		`, job.id, status, attempts, runAfter, lastError, now); err != nil {
			return fmt.Errorf("recover expired worker job: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit expired worker job recovery: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) RenewJob(
	ctx context.Context,
	jobID uuid.UUID,
	leaseToken uuid.UUID,
	now time.Time,
) (time.Time, error) {
	if repository == nil || repository.pool == nil || jobID == uuid.Nil ||
		leaseToken == uuid.Nil || now.IsZero() {
		return time.Time{}, ErrInvalidData
	}
	next := now.Add(jobLease)
	var renewed time.Time
	err := repository.pool.QueryRow(ctx, `
		update worker_jobs
		set lease_until=$4, updated_at=$3
		where id=$1
		  and status='running'
		  and lease_token=$2
		  and lease_until > $3
		returning lease_until
	`, jobID, leaseToken, now, next).Scan(&renewed)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrConflict
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("renew worker job lease: %w", err)
	}
	return renewed, nil
}

func (repository *PostgresRepository) CompleteJob(
	ctx context.Context,
	jobID uuid.UUID,
	leaseToken uuid.UUID,
	jobError error,
) error {
	if jobID == uuid.Nil || leaseToken == uuid.Nil {
		return ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin worker job completion: %w", err)
	}
	defer transaction.Rollback(ctx)
	var status string
	var attempts, maxAttempts int
	var activeLeaseToken uuid.UUID
	var leaseUntil time.Time
	if err := transaction.QueryRow(ctx, `
		select status, attempts, max_attempts, lease_token, lease_until
		from worker_jobs
		where id=$1
		for update
	`, jobID).Scan(
		&status,
		&attempts,
		&maxAttempts,
		&activeLeaseToken,
		&leaseUntil,
	); err != nil {
		return mapNotFound(err, "load worker job")
	}
	now := repository.now()
	if status != JobRunning || activeLeaseToken != leaseToken || !leaseUntil.After(now) {
		return ErrConflict
	}
	if jobError == nil {
		if _, err := transaction.Exec(ctx, `
			update worker_jobs
			set status='succeeded',
			    locked_at=null,
			    locked_by=null,
			    lease_until=null,
			    lease_token=null,
			    last_error=null,
			    updated_at=$2
			where id=$1
		`, jobID, now); err != nil {
			return fmt.Errorf("complete worker job: %w", err)
		}
	} else {
		attempts++
		nextStatus := JobQueued
		runAfter := now.Add(retryDelay(attempts))
		if attempts >= maxAttempts {
			nextStatus = JobDead
			runAfter = now
		}
		message := jobError.Error()
		if len(message) > 4096 {
			message = message[:4096]
		}
		if _, err := transaction.Exec(ctx, `
			update worker_jobs
			set status=$2,
			    attempts=$3,
			    run_after=$4,
			    locked_at=null,
			    locked_by=null,
			    lease_until=null,
			    lease_token=null,
			    last_error=$5,
			    updated_at=$6
			where id=$1
		`, jobID, nextStatus, attempts, runAfter, message, now); err != nil {
			return fmt.Errorf("retry worker job: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit worker job completion: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) RecordCoverEvent(
	ctx context.Context,
	event CoverEvent,
) error {
	if event.ID == uuid.Nil || event.BookID == uuid.Nil ||
		(event.Action != "created" && event.Action != "replaced") ||
		ValidateRelativePath(event.Destination) != nil ||
		!sha256Pattern.MatchString(event.NewSHA256) ||
		(event.OldSHA256 != "" && !sha256Pattern.MatchString(event.OldSHA256)) ||
		!validDimensions(event.NewDimensions) ||
		(len(event.OldDimensions) != 0 && !validDimensions(event.OldDimensions)) ||
		strings.TrimSpace(event.Provider) == "" {
		return ErrInvalidData
	}
	result, err := repository.pool.Exec(ctx, `
		insert into cover_events(
			id, book_id, action, destination, old_sha256, new_sha256,
			old_dimensions, new_dimensions, provider, backup_key
		)
		values ($1,$2,$3,$4,nullif($5,''),$6,$7,$8,$9,nullif($10,''))
	`, event.ID, event.BookID, event.Action, event.Destination, event.OldSHA256,
		event.NewSHA256, nullableDimensions(event.OldDimensions), event.NewDimensions,
		event.Provider, event.BackupKey)
	if err != nil {
		return classifyConstraint(err, "record cover event")
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func validateSourceFile(file SourceFile, rootID string) error {
	if file.ID == uuid.Nil || file.RootID != rootID ||
		ValidateRelativePath(file.RelativePath) != nil || file.Size < 0 ||
		file.ModifiedAt.IsZero() ||
		(file.SHA256 != "" && !sha256Pattern.MatchString(file.SHA256)) {
		return ErrInvalidData
	}
	return nil
}

func validateInferredBook(book InferredBook, rootID string) error {
	if book.RootID != rootID || ValidateSourceFolder(book.SourceFolder) != nil ||
		strings.TrimSpace(book.GroupingKey) == "" || strings.TrimSpace(book.Title) == "" ||
		!metadataSources[book.MetadataSource] || !reviewStates[book.ReviewState] ||
		math.IsNaN(book.Confidence) || book.Confidence < 0 || book.Confidence > 1 ||
		len(book.SourceFileIDs) == 0 {
		return ErrInvalidData
	}
	if book.PublicationYear != nil &&
		(*book.PublicationYear < 1000 || *book.PublicationYear > 3000) {
		return ErrInvalidData
	}
	seen := make(map[uuid.UUID]struct{}, len(book.SourceFileIDs))
	for _, id := range book.SourceFileIDs {
		if id == uuid.Nil {
			return ErrInvalidData
		}
		if _, exists := seen[id]; exists {
			return ErrInvalidData
		}
		seen[id] = struct{}{}
		role := book.FileRoles[id]
		if role != "" && role != FileRoleAudio && role != FileRoleCover && role != FileRoleSidecar {
			return ErrInvalidData
		}
	}
	if _, err := marshalObject(book.Identifiers); err != nil {
		return err
	}
	return nil
}

func verifyAssignmentFiles(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	fileIDs []uuid.UUID,
) error {
	var count int
	if err := transaction.QueryRow(ctx, `
		select count(*)
		from source_files
		where id=any($1::uuid[]) and root_id=$2 and missing_at is null
	`, fileIDs, rootID).Scan(&count); err != nil {
		return fmt.Errorf("validate inferred source files: %w", err)
	}
	if count != len(fileIDs) {
		return ErrConflict
	}
	return nil
}

func loadGroupingCorrections(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
) (map[string]GroupingCorrection, error) {
	rows, err := transaction.Query(ctx, `
		select id, root_id, source_folder, groups, updated_at
		from grouping_corrections
		where root_id=$1
		for share
	`, rootID)
	if err != nil {
		return nil, fmt.Errorf("load grouping corrections: %w", err)
	}
	defer rows.Close()
	corrections := make(map[string]GroupingCorrection)
	for rows.Next() {
		var correction GroupingCorrection
		var groups []byte
		if err := rows.Scan(
			&correction.ID,
			&correction.RootID,
			&correction.SourceFolder,
			&groups,
			&correction.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan grouping correction: %w", err)
		}
		if err := json.Unmarshal(groups, &correction.Groups); err != nil {
			return nil, fmt.Errorf("decode grouping correction: %w", err)
		}
		corrections[correction.SourceFolder] = correction
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate grouping corrections: %w", err)
	}
	return corrections, nil
}

type correctedBook struct {
	id         uuid.UUID
	fileIDs    []uuid.UUID
	wasMissing bool
}

func applyGroupingCorrections(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	corrections map[string]GroupingCorrection,
) (
	map[string]map[uuid.UUID]struct{},
	map[string][]uuid.UUID,
	[]uuid.UUID,
	error,
) {
	claimedFiles := make(map[string]map[uuid.UUID]struct{}, len(corrections))
	booksByFolder := make(map[string][]uuid.UUID, len(corrections))
	presentBookIDs := make([]uuid.UUID, 0)
	for folder, correction := range corrections {
		claimedFiles[folder] = make(map[uuid.UUID]struct{})
		folderFiles, err := loadGroupingFolderFiles(ctx, transaction, rootID, folder)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("list corrected grouping folder files: %w", err)
		}
		for _, group := range correction.Groups {
			if group.BookID == nil || *group.BookID == uuid.Nil ||
				len(group.FileIDs) == 0 {
				continue
			}
			if err := verifyAssignmentFiles(ctx, transaction, rootID, group.FileIDs); err != nil {
				if errors.Is(err, ErrConflict) {
					continue
				}
				return nil, nil, nil, err
			}
			var id uuid.UUID
			var missingAt *time.Time
			if err := transaction.QueryRow(ctx, `
				select id, missing_at
				from books
				where id=$1 and root_id=$2 and source_folder=$3 and grouping_key=$4
				for update
			`, *group.BookID, rootID, folder, group.GroupingKey).Scan(
				&id,
				&missingAt,
			); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return nil, nil, nil, fmt.Errorf("load corrected book: %w", err)
			}
			book := correctedBook{
				id:         id,
				fileIDs:    group.FileIDs,
				wasMissing: missingAt != nil,
			}
			assignmentIDs, roles, err := groupingBookFiles(book.fileIDs, folderFiles)
			if errors.Is(err, ErrConflict) {
				continue
			}
			if err != nil {
				return nil, nil, nil, err
			}
			if err := replaceBookFiles(
				ctx,
				transaction,
				book.id,
				rootID,
				assignmentIDs,
				roles,
			); err != nil {
				return nil, nil, nil, err
			}
			if _, err := transaction.Exec(ctx, `
				update books
				set grouping_locked=true, missing_at=null, updated_at=now()
				where id=$1
			`, book.id); err != nil {
				return nil, nil, nil, fmt.Errorf("apply corrected grouping: %w", err)
			}
			if book.wasMissing {
				if err := insertAvailabilityEvent(
					ctx,
					transaction,
					rootID,
					"book",
					book.id,
					"restored",
					nil,
				); err != nil {
					return nil, nil, nil, err
				}
			}
			presentBookIDs = append(presentBookIDs, book.id)
			booksByFolder[folder] = append(booksByFolder[folder], book.id)
			for _, fileID := range assignmentIDs {
				if roles[fileID] == FileRoleCover {
					continue
				}
				claimedFiles[folder][fileID] = struct{}{}
			}
		}
	}
	return claimedFiles, booksByFolder, presentBookIDs, nil
}

func prepareInferredBooks(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	inferred []InferredBook,
	corrections map[string]GroupingCorrection,
	claimedFiles map[string]map[uuid.UUID]struct{},
	correctedBooksByFolder map[string][]uuid.UUID,
) ([]InferredBook, error) {
	assigned := make(map[string]map[uuid.UUID]struct{}, len(claimedFiles))
	for folder, fileIDs := range claimedFiles {
		assigned[folder] = make(map[uuid.UUID]struct{}, len(fileIDs))
		for fileID := range fileIDs {
			assigned[folder][fileID] = struct{}{}
		}
	}
	prepared := make([]InferredBook, 0, len(inferred))
	for _, candidate := range inferred {
		if err := validateInferredBook(candidate, rootID); err != nil {
			return nil, err
		}
		if assigned[candidate.SourceFolder] == nil {
			assigned[candidate.SourceFolder] = make(map[uuid.UUID]struct{})
		}
		filtered := make([]uuid.UUID, 0, len(candidate.SourceFileIDs))
		filteredRoles := make(map[uuid.UUID]string)
		for _, fileID := range candidate.SourceFileIDs {
			role := candidate.FileRoles[fileID]
			isSharedCover := role == FileRoleCover
			if _, alreadyAssigned := assigned[candidate.SourceFolder][fileID]; alreadyAssigned && !isSharedCover {
				continue
			}
			filtered = append(filtered, fileID)
			if role != "" {
				filteredRoles[fileID] = role
			}
		}
		if len(filtered) == 0 {
			continue
		}
		if err := verifyAssignmentFiles(ctx, transaction, rootID, filtered); err != nil {
			return nil, err
		}
		audioFileIDs, err := verifyFilesInGroupingScope(
			ctx,
			transaction,
			rootID,
			candidate.SourceFolder,
			filtered,
			filteredRoles,
		)
		if err != nil {
			return nil, err
		}
		companionIDs := make([]uuid.UUID, 0)
		for _, fileID := range filtered {
			role := filteredRoles[fileID]
			if role == FileRoleCover {
				companionIDs = append(companionIDs, fileID)
			}
		}
		if len(companionIDs) > 0 {
			for _, correctedBookID := range correctedBooksByFolder[candidate.SourceFolder] {
				if err := ensureBookFiles(
					ctx,
					transaction,
					correctedBookID,
					rootID,
					companionIDs,
					filteredRoles,
				); err != nil {
					return nil, err
				}
			}
		}
		if len(audioFileIDs) == 0 {
			continue
		}
		candidate.SourceFileIDs = filtered
		candidate.FileRoles = filteredRoles
		if _, correctedFolder := corrections[candidate.SourceFolder]; correctedFolder {
			candidate.GroupingKey = unmatchedGroupingKey(audioFileIDs)
			candidate.ReviewState = ReviewNeedsReview
			if candidate.MetadataSource == MetadataConfirmed {
				candidate.MetadataSource = MetadataInferred
			}
		}
		for _, fileID := range audioFileIDs {
			assigned[candidate.SourceFolder][fileID] = struct{}{}
		}
		prepared = append(prepared, candidate)
	}
	for folder := range corrections {
		files, err := directAudioFiles(ctx, transaction, rootID, folder)
		if err != nil {
			return nil, err
		}
		if assigned[folder] == nil {
			assigned[folder] = make(map[uuid.UUID]struct{})
		}
		for _, file := range files {
			if _, exists := assigned[folder][file.id]; exists {
				continue
			}
			prepared = append(prepared, InferredBook{
				RootID:         rootID,
				SourceFolder:   folder,
				GroupingKey:    unmatchedGroupingKey([]uuid.UUID{file.id}),
				Title:          audioStem(file.relativePath),
				MetadataSource: MetadataInferred,
				Confidence:     0.5,
				ReviewState:    ReviewNeedsReview,
				SourceFileIDs:  []uuid.UUID{file.id},
			})
			assigned[folder][file.id] = struct{}{}
		}
	}
	return prepared, nil
}

type catalogFile struct {
	id           uuid.UUID
	relativePath string
}

func directAudioFiles(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	folder string,
) ([]catalogFile, error) {
	rows, err := transaction.Query(ctx, `
		select id, relative_path
		from source_files
		where root_id=$1
		  and missing_at is null
		  and (
		      ($2='' and position('/' in relative_path)=0)
		      or (
		          $2<>''
		          and left(relative_path, length($2)+1)=$2 || '/'
		          and position('/' in substring(relative_path from length($2)+2))=0
		      )
		  )
		order by relative_path, id
	`, rootID, folder)
	if err != nil {
		return nil, fmt.Errorf("list direct folder audio: %w", err)
	}
	defer rows.Close()
	var files []catalogFile
	for rows.Next() {
		var file catalogFile
		if err := rows.Scan(&file.id, &file.relativePath); err != nil {
			return nil, fmt.Errorf("scan direct folder audio: %w", err)
		}
		if isAudioPath(file.relativePath) {
			files = append(files, file)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate direct folder audio: %w", err)
	}
	return files, nil
}

func verifyFilesInGroupingScope(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	folder string,
	fileIDs []uuid.UUID,
	roles map[uuid.UUID]string,
) ([]uuid.UUID, error) {
	rows, err := transaction.Query(ctx, `
		select id, relative_path
		from source_files
		where root_id=$1 and id=any($2::uuid[]) and missing_at is null
	`, rootID, fileIDs)
	if err != nil {
		return nil, fmt.Errorf("validate inferred file folders: %w", err)
	}
	defer rows.Close()
	count := 0
	audioFileIDs := make([]uuid.UUID, 0, len(fileIDs))
	for rows.Next() {
		var id uuid.UUID
		var relativePath string
		if err := rows.Scan(&id, &relativePath); err != nil {
			return nil, fmt.Errorf("scan inferred file folder: %w", err)
		}
		inScope := folder == "" || strings.HasPrefix(relativePath, folder+"/")
		if !inScope || !isAudioPath(relativePath) {
			role := roles[id]
			if !inScope ||
				(role != FileRoleCover && role != FileRoleSidecar) {
				return nil, ErrConflict
			}
		} else {
			role := roles[id]
			if role == FileRoleCover || role == FileRoleSidecar {
				return nil, ErrConflict
			}
			audioFileIDs = append(audioFileIDs, id)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inferred file folders: %w", err)
	}
	if count != len(fileIDs) {
		return nil, ErrConflict
	}
	return audioFileIDs, nil
}

func unmatchedGroupingKey(fileIDs []uuid.UUID) string {
	values := make([]string, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		values = append(values, fileID.String())
	}
	sort.Strings(values)
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return fmt.Sprintf("unmatched-%x", digest[:8])
}

func audioStem(relativePath string) string {
	base := path.Base(relativePath)
	title := strings.TrimSpace(strings.TrimSuffix(base, path.Ext(base)))
	if title == "" {
		return "Audiobook"
	}
	return title
}

func findExistingBook(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	sourceFolder string,
	groupingKey string,
) (id uuid.UUID, locked, confirmed, missing bool, err error) {
	var confirmedMatchID *uuid.UUID
	var missingAt *time.Time
	err = transaction.QueryRow(ctx, `
		select id, grouping_locked, confirmed_match_id, missing_at
		from books
		where root_id=$1 and source_folder=$2 and grouping_key=$3
		for update
	`, rootID, sourceFolder, groupingKey).Scan(&id, &locked, &confirmedMatchID, &missingAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, false, false, nil
	}
	if err != nil {
		return uuid.Nil, false, false, false, fmt.Errorf("load inferred book: %w", err)
	}
	return id, locked, confirmedMatchID != nil, missingAt != nil, nil
}

func insertInferredBook(
	ctx context.Context,
	transaction pgx.Tx,
	bookID uuid.UUID,
	book InferredBook,
	locked bool,
) error {
	identifiers, err := marshalObject(book.Identifiers)
	if err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `
		insert into books(
			id, root_id, source_folder, grouping_key, grouping_locked,
			title, subtitle, authors, narrators, series, series_position,
			publisher, published_date, publication_year, genres, provider_subjects,
			identifiers, metadata_source, confidence, review_state
		)
		values (
			$1,$2,$3,$4,$5,$6,nullif($7,''),$8,$9,nullif($10,''),nullif($11,''),
			nullif($12,''),nullif($13,''),$14,$15,$16,$17,$18,$19,$20
		)
	`, bookID, book.RootID, book.SourceFolder, book.GroupingKey, locked,
		book.Title, book.Subtitle, nonNilStrings(book.Authors), nonNilStrings(book.Narrators),
		book.Series, book.SeriesPosition, book.Publisher, book.PublishedDate,
		book.PublicationYear, nonNilStrings(book.Genres), nonNilStrings(book.ProviderSubjects),
		identifiers, book.MetadataSource, book.Confidence, book.ReviewState); err != nil {
		return classifyConstraint(err, "insert inferred book")
	}
	return replaceBookFiles(
		ctx,
		transaction,
		bookID,
		book.RootID,
		book.SourceFileIDs,
		book.FileRoles,
	)
}

func updateInferredBook(
	ctx context.Context,
	transaction pgx.Tx,
	bookID uuid.UUID,
	book InferredBook,
) error {
	identifiers, err := marshalObject(book.Identifiers)
	if err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `
		update books
		set title=$2,
		    subtitle=nullif($3,''),
		    authors=$4,
		    narrators=$5,
		    series=nullif($6,''),
		    series_position=nullif($7,''),
		    publisher=nullif($8,''),
		    published_date=nullif($9,''),
		    publication_year=$10,
		    genres=$11,
		    provider_subjects=$12,
		    identifiers=$13,
		    metadata_source=$14,
		    confidence=$15,
		    review_state=$16,
		    updated_at=now()
		where id=$1
	`, bookID, book.Title, book.Subtitle, nonNilStrings(book.Authors),
		nonNilStrings(book.Narrators), book.Series, book.SeriesPosition,
		book.Publisher, book.PublishedDate, book.PublicationYear,
		nonNilStrings(book.Genres), nonNilStrings(book.ProviderSubjects),
		identifiers, book.MetadataSource, book.Confidence, book.ReviewState); err != nil {
		return classifyConstraint(err, "update inferred book")
	}
	return nil
}

func bookHasMissingAssignments(
	ctx context.Context,
	transaction pgx.Tx,
	bookID uuid.UUID,
) (bool, error) {
	var missing bool
	if err := transaction.QueryRow(ctx, `
		select exists (
			select 1
			from book_files bf
			join source_files f on f.id=bf.file_id and f.root_id=bf.root_id
			where bf.book_id=$1 and f.missing_at is not null
		)
	`, bookID).Scan(&missing); err != nil {
		return false, fmt.Errorf("check missing book assignments: %w", err)
	}
	return missing, nil
}

func replaceBookFiles(
	ctx context.Context,
	transaction pgx.Tx,
	bookID uuid.UUID,
	rootID string,
	fileIDs []uuid.UUID,
	roles map[uuid.UUID]string,
) error {
	exclusiveFileIDs := make([]uuid.UUID, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		role := roles[fileID]
		if role == "" || role == FileRoleAudio || role == FileRoleSidecar {
			exclusiveFileIDs = append(exclusiveFileIDs, fileID)
		}
	}
	if len(exclusiveFileIDs) > 0 {
		if _, err := transaction.Exec(ctx, `
			delete from book_files bf
			using books b
			where bf.book_id=b.id
			  and bf.root_id=$1
			  and bf.file_id=any($2::uuid[])
			  and bf.book_id<>$3
			  and b.missing_at is null
		`, rootID, exclusiveFileIDs, bookID); err != nil {
			return fmt.Errorf("clear competing active book assignments: %w", err)
		}
	}
	if _, err := transaction.Exec(
		ctx,
		`delete from book_files where book_id=$1`,
		bookID,
	); err != nil {
		return fmt.Errorf("clear book files: %w", err)
	}
	return ensureBookFiles(ctx, transaction, bookID, rootID, fileIDs, roles)
}

func ensureBookFiles(
	ctx context.Context,
	transaction pgx.Tx,
	bookID uuid.UUID,
	rootID string,
	fileIDs []uuid.UUID,
	roles map[uuid.UUID]string,
) error {
	for _, fileID := range fileIDs {
		role := roles[fileID]
		if role == "" {
			role = FileRoleAudio
		}
		if _, err := transaction.Exec(ctx, `
			insert into book_files(book_id, file_id, root_id, role)
			values ($1,$2,$3,$4)
			on conflict (book_id, file_id) do update set role=excluded.role
		`, bookID, fileID, rootID, role); err != nil {
			return classifyConstraint(err, "assign book file")
		}
	}
	return nil
}

func bookColumns(alias string) string {
	return alias + `.id, ` + alias + `.root_id, ` + alias + `.source_folder,
		` + alias + `.grouping_key, ` + alias + `.grouping_locked, ` +
		alias + `.title, coalesce(` + alias + `.subtitle,''), ` + alias + `.authors,
		` + alias + `.narrators, coalesce(` + alias + `.series,''),
		coalesce(` + alias + `.series_position,''), coalesce(` + alias + `.publisher,''),
		coalesce(` + alias + `.published_date,''), ` + alias + `.publication_year,
		` + alias + `.genres, ` + alias + `.provider_subjects, ` + alias + `.identifiers,
		` + alias + `.metadata_source, ` + alias + `.confidence::float8,
		` + alias + `.review_state, ` + alias + `.confirmed_match_id,
		` + alias + `.missing_at, ` + alias + `.created_at, ` + alias + `.updated_at`
}

type scanner interface {
	Scan(...any) error
}

func scanBook(row scanner) (Book, error) {
	return scanBookWithExtras(row)
}

func scanBookWithExtras(row scanner, extras ...any) (Book, error) {
	var book Book
	var identifiers []byte
	destinations := []any{
		&book.ID,
		&book.RootID,
		&book.SourceFolder,
		&book.GroupingKey,
		&book.GroupingLocked,
		&book.Title,
		&book.Subtitle,
		&book.Authors,
		&book.Narrators,
		&book.Series,
		&book.SeriesPosition,
		&book.Publisher,
		&book.PublishedDate,
		&book.PublicationYear,
		&book.Genres,
		&book.ProviderSubjects,
		&identifiers,
		&book.MetadataSource,
		&book.Confidence,
		&book.ReviewState,
		&book.ConfirmedMatchID,
		&book.MissingAt,
		&book.CreatedAt,
		&book.UpdatedAt,
	}
	destinations = append(destinations, extras...)
	if err := row.Scan(destinations...); err != nil {
		return Book{}, err
	}
	if err := json.Unmarshal(identifiers, &book.Identifiers); err != nil {
		return Book{}, fmt.Errorf("decode book identifiers: %w", err)
	}
	return book, nil
}

func scanSourceFile(row scanner) (SourceFile, error) {
	var file SourceFile
	var media []byte
	if err := row.Scan(
		&file.ID,
		&file.RootID,
		&file.RelativePath,
		&file.Size,
		&file.ModifiedAt,
		&file.SHA256,
		&media,
		&file.ScanRunID,
		&file.MissingAt,
	); err != nil {
		return SourceFile{}, err
	}
	if err := json.Unmarshal(media, &file.Media); err != nil {
		return SourceFile{}, fmt.Errorf("decode source media: %w", err)
	}
	return file, nil
}

func scanMetadataMatch(row scanner) (MetadataMatch, error) {
	var match MetadataMatch
	var payload []byte
	if err := row.Scan(
		&match.ID,
		&match.BookID,
		&match.Provider,
		&match.ProviderID,
		&match.Score,
		&payload,
		&match.CoverURL,
		&match.Confirmed,
		&match.CreatedAt,
	); err != nil {
		return MetadataMatch{}, err
	}
	if err := json.Unmarshal(payload, &match.Payload); err != nil {
		return MetadataMatch{}, fmt.Errorf("decode metadata match payload: %w", err)
	}
	return match, nil
}

func scanWorkerJob(row scanner) (WorkerJob, error) {
	var job WorkerJob
	var payload []byte
	if err := row.Scan(
		&job.ID,
		&job.Kind,
		&payload,
		&job.Status,
		&job.RunAfter,
		&job.Attempts,
		&job.MaxAttempts,
		&job.LockedAt,
		&job.LockedBy,
		&job.LeaseUntil,
		&job.LeaseToken,
		&job.LastError,
		&job.CreatedAt,
		&job.UpdatedAt,
	); err != nil {
		return WorkerJob{}, err
	}
	if err := json.Unmarshal(payload, &job.Payload); err != nil {
		return WorkerJob{}, fmt.Errorf("decode worker job payload: %w", err)
	}
	return job, nil
}

func marshalObject(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalidData
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, ErrInvalidData
	}
	if _, object := decoded.(map[string]any); !object {
		return nil, ErrInvalidData
	}
	return encoded, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func insertAvailabilityEvent(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	entityKind string,
	entityID uuid.UUID,
	action string,
	scanRunID *uuid.UUID,
) error {
	if _, err := transaction.Exec(ctx, `
		insert into availability_events(
			id, root_id, entity_kind, entity_id, action, scan_run_id
		)
		values ($1,$2,$3,$4,$5,$6)
	`, uuid.New(), rootID, entityKind, entityID, action, scanRunID); err != nil {
		return classifyConstraint(err, "record availability event")
	}
	return nil
}

func retryDelay(attempts int) time.Duration {
	if attempts <= 1 {
		return time.Minute
	}
	if attempts >= 10 {
		return 6 * time.Hour
	}
	delay := time.Minute * time.Duration(1<<uint(attempts-1))
	if delay > 6*time.Hour {
		return 6 * time.Hour
	}
	return delay
}

func validDimensions(dimensions []int32) bool {
	return len(dimensions) == 2 && dimensions[0] > 0 && dimensions[1] > 0
}

func nullableDimensions(dimensions []int32) any {
	if len(dimensions) == 0 {
		return nil
	}
	return dimensions
}

func mapNotFound(err error, operation string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func classifyConstraint(err error, operation string) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		switch databaseError.Code {
		case "23503", "23505":
			return fmt.Errorf("%w: %s", ErrConflict, operation)
		case "23502", "23514", "22P02":
			return fmt.Errorf("%w: %s", ErrInvalidData, operation)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

var _ Repository = (*PostgresRepository)(nil)
