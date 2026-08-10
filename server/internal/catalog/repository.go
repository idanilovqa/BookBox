package catalog

import (
	"context"
	"errors"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrNotFound            = errors.New("catalog record not found")
	ErrNoJob               = errors.New("no worker job is ready")
	ErrInvalidPath         = errors.New("invalid relative path")
	ErrInvalidData         = errors.New("invalid catalog data")
	ErrInvalidPage         = errors.New("invalid catalog page")
	ErrConflict            = errors.New("catalog state conflict")
	ErrSearchIndexBuilding = errors.New("search index is building")
	ErrReorganizationStale = errors.New("reorganization preview is stale")
	ErrDestinationConflict = errors.New("reorganization destination conflict")
)

var windowsDrivePath = regexp.MustCompile(`^[A-Za-z]:/`)

type Repository interface {
	SyncRoots(context.Context, []LibraryRoot) error
	BeginScan(context.Context, string) (ScanRun, error)
	UpsertSourceFiles(context.Context, uuid.UUID, []SourceFile) error
	FinishScan(context.Context, uuid.UUID, ScanResult) error
	ApplyInventory(context.Context, InventoryUpdate) (InventoryDelta, error)
	DrainReadyFingerprintOutbox(context.Context, time.Time, int) (int, error)
	IncrementCatalogGeneration(context.Context, string) (CatalogGeneration, error)
	ReplaceInferredBooks(context.Context, string, []InferredBook) error
	FinalizeSuccessfulScan(context.Context, uuid.UUID, []InferredBook) error
	PublishSearchProjection(context.Context, SearchProjection) error
	FinalizeInventoryAndPublish(context.Context, InventoryFinalization) (CatalogGeneration, error)
	ApplyGroupingRefinement(context.Context, GroupingRefinement) (CatalogGeneration, error)
	SearchLibrary(context.Context, SearchQuery) (SearchPage, error)
	ListBooks(context.Context, BookQuery) (Page[Book], error)
	ListCatalogFilters(context.Context) (FilterSnapshot, error)
	GetBook(context.Context, uuid.UUID) (BookDetail, error)
	GetGroupingContext(context.Context, uuid.UUID) (GroupingContext, error)
	SaveMatches(context.Context, uuid.UUID, []MetadataMatch) error
	ConfirmMatch(context.Context, uuid.UUID, uuid.UUID) error
	SaveGrouping(context.Context, GroupingCorrection) (CatalogGeneration, error)
	Enqueue(context.Context, WorkerJob) error
	EnqueueDeduplicated(context.Context, WorkerJob, string) (WorkerJob, error)
	RecoverExpiredJobs(context.Context, time.Time) error
	ClaimJob(context.Context, string, time.Time) (WorkerJob, error)
	RenewJob(context.Context, uuid.UUID, uuid.UUID, time.Time) (time.Time, error)
	CompleteJob(context.Context, uuid.UUID, uuid.UUID, error) error
	RecordCoverEvent(context.Context, CoverEvent) error
}

func ValidateRelativePath(value string) error {
	if value == "" || value == "." || !utf8.ValidString(value) ||
		strings.HasPrefix(value, "/") || windowsDrivePath.MatchString(value) ||
		strings.ContainsRune(value, '\\') || path.Clean(value) != value {
		return ErrInvalidPath
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ErrInvalidPath
		}
		for _, character := range segment {
			if unicode.IsControl(character) {
				return ErrInvalidPath
			}
		}
	}
	return nil
}

func ValidateSourceFolder(value string) error {
	if value == "" {
		return nil
	}
	return ValidateRelativePath(value)
}
