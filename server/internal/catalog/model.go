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

	FileKindAudio     = "audio"
	FileKindCover     = "cover"
	FileKindCompanion = "companion"

	CompanionCue      = "cue"
	CompanionNFO      = "nfo"
	CompanionOPF      = "opf"
	CompanionPlaylist = "playlist"
	CompanionEbook    = "ebook"
	CompanionOther    = "other"

	ProbeNotApplicable = "not_applicable"
	ProbePending       = "pending"
	ProbeSucceeded     = "succeeded"
	ProbeFailed        = "failed"

	JobScanRoot         = "scan_root"
	JobProbeFile        = "probe_file"
	JobInspectCompanion = "inspect_companion"
	JobRegroupScope     = "regroup_scope"
	JobPublishSearch    = "publish_search"
	JobEnrichBook       = "enrich_book"
	JobPublishCover     = "publish_cover"
	JobReorganizeFiles  = "reorganize_files"

	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobDead      = "dead"

	BookSortTitle           = "title"
	BookSortAuthor          = "author"
	BookSortYear            = "year"
	BookSortRecentlyAdded   = "recently_added"
	BookSortRecentlyScanned = "recently_scanned"
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
	ID            uuid.UUID
	RootID        string
	RelativePath  string
	Size          int64
	ModifiedAt    time.Time
	SHA256        string
	Media         map[string]any
	ScanRunID     *uuid.UUID
	MissingAt     *time.Time
	FileKind      string
	CompanionKind string
	ProbeStatus   string
	ProbeVersion  int
	ProbeError    string
	ProbedAt      *time.Time
}

type CatalogGeneration struct {
	RootID    string
	Value     int64
	UpdatedAt time.Time
}

type GroupingEvidence struct {
	ReasonCode  string
	Confidence  float64
	ReviewState string
	Values      map[string][]string
}

type InventoryFile struct {
	ID            uuid.UUID
	RootID        string
	RelativePath  string
	FileKind      string
	CompanionKind string
	Size          int64
	ModifiedAt    time.Time
}

type InventoryUpdate struct {
	ScanRunID uuid.UUID
	RootID    string
	ScopePath string
	Complete  bool
	Files     []InventoryFile
}

type InventoryDelta struct {
	ProbeFiles        []SourceFile
	InspectCompanions []SourceFile
	RegroupScopes     []string
	UnchangedFiles    []SourceFile
	MissingFileIDs    []uuid.UUID
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

type GroupingContext struct {
	BookID        uuid.UUID
	RootID        string
	SourceFolder  string
	ScopePath     string
	ReviewReason  string
	Evidence      map[string][]string
	Files         []GroupingContextFile
	Books         []GroupingContextBook
	CanReorganize bool
}

type GroupingContextFile struct {
	SourceFile
	Role          string
	CompanionKind string
	Shared        bool
}

type GroupingContextBook struct {
	BookID      uuid.UUID
	Title       string
	GroupingKey string
	FileIDs     []uuid.UUID
}

type InferredBook struct {
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
	ReviewReason     string
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
	ID            uuid.UUID
	RootID        string
	SourceFolder  string
	ExpectedFiles []GroupingFileFingerprint
	Groups        []GroupingGroup
	UpdatedAt     time.Time
}

type GroupingFileFingerprint struct {
	FileID     uuid.UUID `json:"fileId"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
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

type FilterSnapshot struct {
	Authors      []string `json:"authors"`
	Genres       []string `json:"genres"`
	Years        []int    `json:"years"`
	Roots        []string `json:"roots"`
	ReviewStates []string `json:"reviewStates"`
}
