package catalog

import (
	"time"

	"github.com/google/uuid"
)

type SearchMode string

const (
	SearchModeAudiobooks SearchMode = "audiobooks"
	SearchModeRaw        SearchMode = "raw"
)

type SearchEntityKind string

const (
	SearchEntityAudiobook SearchEntityKind = "audiobook"
	SearchEntityFolder    SearchEntityKind = "folder"
	SearchEntityFile      SearchEntityKind = "file"
)

type SearchProjectionInput struct {
	RootID      string
	ScanRunID   uuid.UUID
	CompletedAt time.Time
	Generation  CatalogGeneration
	Books       []Book
	Files       []SourceFile
	Assignments map[uuid.UUID][]uuid.UUID
	Evidence    map[uuid.UUID]GroupingEvidence
}

type SearchSnapshot struct {
	RootID            string
	ScanRunID         uuid.UUID
	CompletedAt       time.Time
	DocumentCount     int
	CatalogGeneration int64
}

type SearchProjection struct {
	Snapshot  SearchSnapshot
	Documents []SearchDocument
}

type SearchDocument struct {
	ID                uuid.UUID
	RootID            string
	EntityKind        SearchEntityKind
	BookID            uuid.UUID
	RelativePath      string
	DisplayName       string
	Size              *int64
	ModifiedAt        *time.Time
	Title             string
	Authors           []string
	Series            string
	SeriesPosition    string
	Narrators         []string
	Genres            []string
	ReviewState       string
	ReviewReason      string
	PublicationYear   *int
	CreatedAt         *time.Time
	RecentlyScannedAt *time.Time
	Filenames         []string
	DurationMS        *int64
	DuplicateGroupID  uuid.UUID
	DuplicateCount    int
	CompactName       string
	CompactPath       string
	CompactMetadata   string
	CompactAuthors    []string
	CompactGenres     []string
	SearchCompact     string
	SearchTerms       string
	SearchGrams       []string
}

type InventoryFinalization struct {
	ScanRunID uuid.UUID
	RootID    string
	Books     []InferredBook
	Evidence  map[string]GroupingEvidence
}

type GroupingRefinement struct {
	RootID             string
	ScopePath          string
	ExpectedGeneration int64
	Books              []InferredBook
	Evidence           map[string]GroupingEvidence
}

type SearchGroup struct {
	ID        uuid.UUID
	Duplicate bool
	Documents []SearchDocument
}

type SearchPage struct {
	Snapshots  []SearchSnapshot
	Groups     []SearchGroup
	NextCursor string
}

type SearchQuery struct {
	RootIDs         []string
	Mode            SearchMode
	Text            string
	Author          string
	Series          string
	Genre           string
	ReviewState     string
	PublicationYear *int
	Sort            string
	Limit           int
	Cursor          string
}
