package catalog

import (
	"time"

	"github.com/google/uuid"
)

const (
	MetadataInferred    = "inferred"
	MetadataLocal       = "local"
	MetadataOpenLibrary = "open_library"
	MetadataGoogleBooks = "google_books"
	MetadataConfirmed   = "confirmed"
	MetadataMixed       = "mixed"

	ReviewUnreviewed  = "unreviewed"
	ReviewNeedsReview = "needs_review"
	ReviewConfirmed   = "confirmed"

	FileRoleAudio   = "audio"
	FileRoleCover   = "cover"
	FileRoleSidecar = "sidecar"

	JobScanRoot     = "scan_root"
	JobEnrichBook   = "enrich_book"
	JobPublishCover = "publish_cover"

	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobDead      = "dead"

	BookSortTitle         = "title"
	BookSortRecentlyAdded = "recently_added"
)

type LibraryRoot struct {
	ID          string
	DisplayName string
	ScannedAt   *time.Time
}

type ScanRun struct {
	ID         uuid.UUID
	RootID     string
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string
	Error      string
}

type ScanResult struct {
	Success bool
	Partial bool
	Error   string
}

type SourceFile struct {
	ID           uuid.UUID
	RootID       string
	RelativePath string
	Size         int64
	ModifiedAt   time.Time
	SHA256       string
	Media        map[string]any
	ScanRunID    *uuid.UUID
	MissingAt    *time.Time
}

type Book struct {
	ID               uuid.UUID
	RootID           string
	SourceFolder     string
	GroupingKey      string
	GroupingLocked   bool
	Title            string
	Subtitle         string
	Authors          []string
	Narrators        []string
	Series           string
	SeriesPosition   string
	Publisher        string
	PublishedDate    string
	PublicationYear  *int
	Genres           []string
	ProviderSubjects []string
	Identifiers      map[string]any
	MetadataSource   string
	Confidence       float64
	ReviewState      string
	ConfirmedMatchID *uuid.UUID
	MissingAt        *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type BookDetail struct {
	Book    Book
	Files   []SourceFile
	Matches []MetadataMatch
}

type InferredBook struct {
	RootID           string
	SourceFolder     string
	GroupingKey      string
	Title            string
	Subtitle         string
	Authors          []string
	Narrators        []string
	Series           string
	SeriesPosition   string
	Publisher        string
	PublishedDate    string
	PublicationYear  *int
	Genres           []string
	ProviderSubjects []string
	Identifiers      map[string]any
	MetadataSource   string
	Confidence       float64
	ReviewState      string
	SourceFileIDs    []uuid.UUID
	FileRoles        map[uuid.UUID]string
}

type MetadataMatch struct {
	ID         uuid.UUID
	BookID     uuid.UUID
	Provider   string
	ProviderID string
	Score      float64
	Payload    map[string]any
	CoverURL   string
	Confirmed  bool
	CreatedAt  time.Time
}

type GroupingGroup struct {
	BookID      *uuid.UUID  `json:"bookId,omitempty"`
	GroupingKey string      `json:"groupingKey"`
	FileIDs     []uuid.UUID `json:"fileIds"`
}

type GroupingCorrection struct {
	ID           uuid.UUID
	RootID       string
	SourceFolder string
	Groups       []GroupingGroup
	UpdatedAt    time.Time
}

type WorkerJob struct {
	ID          uuid.UUID
	Kind        string
	Payload     map[string]any
	Status      string
	RunAfter    time.Time
	Attempts    int
	MaxAttempts int
	LockedAt    *time.Time
	LockedBy    string
	LeaseUntil  *time.Time
	LeaseToken  uuid.UUID
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CoverEvent struct {
	ID            uuid.UUID
	BookID        uuid.UUID
	Action        string
	Destination   string
	OldSHA256     string
	NewSHA256     string
	OldDimensions []int32
	NewDimensions []int32
	Provider      string
	BackupKey     string
	CreatedAt     time.Time
}

type BookQuery struct {
	Search          string
	RootID          string
	Author          string
	Genre           string
	ReviewState     string
	PublicationYear *int
	Sort            string
	Limit           int
	Cursor          string
}

type Page[T any] struct {
	Items      []T
	NextCursor string
}
