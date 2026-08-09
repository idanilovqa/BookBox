package worker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/grouping"
	"bookbox.local/server/internal/library"
	"bookbox.local/server/internal/media"
	"bookbox.local/server/internal/scanner"
)

func TestScanRootPublishesProvisionalCatalogBeforeProbeJobs(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 123, time.UTC)
	audio := stagedSourceFile("Author/Book/01.mp3", catalog.FileKindAudio, now)
	repository := &stagedRepositoryFake{
		run:        catalog.ScanRun{ID: uuid.New(), RootID: "audiobooks"},
		delta:      catalog.InventoryDelta{ProbeFiles: []catalog.SourceFile{audio}},
		generation: catalog.CatalogGeneration{RootID: "audiobooks", Value: 2},
		tree:       grouping.Tree{RootID: "audiobooks", ScopePath: "", Files: []catalog.SourceFile{audio}},
	}
	handler := stagedScanHandler(repository, scanner.InventoryResult{
		RootID: "audiobooks", Scope: scanner.FullScope(),
		Files: []scanner.InventoryFile{stagedInventoryFile(audio)},
	})

	if err := handler.Handle(context.Background(), stagedScanJob("audiobooks", "", true)); err != nil {
		t.Fatal(err)
	}
	if repository.finalizations != 1 {
		t.Fatalf("finalizations = %d, want 1", repository.finalizations)
	}
	if got := repository.events; !reflect.DeepEqual(got, []string{"apply", "load_tree", "finalize", "enqueue:" + catalog.JobProbeFile}) {
		t.Fatalf("events = %v", got)
	}
	wantKey := "probe_file:" + audio.ID.String() + ":17:" + stagedFingerprintNanos(now)
	if !reflect.DeepEqual(repository.dedupeKeys, []string{wantKey}) {
		t.Fatalf("dedupe keys = %v, want %q", repository.dedupeKeys, wantKey)
	}
	if repository.probeCalls != 0 {
		t.Fatalf("probe calls during inventory = %d", repository.probeCalls)
	}
}

func TestScanRootPartialInventoryDoesNotFinalizeOrMarkComplete(t *testing.T) {
	repository := &stagedRepositoryFake{run: catalog.ScanRun{ID: uuid.New(), RootID: "audiobooks"}}
	result := scanner.InventoryResult{
		RootID: "audiobooks", Scope: scanner.FullScope(),
		Warnings: []scanner.Warning{{RelativePath: "Unreadable", Operation: "walk"}},
	}
	handler := stagedScanHandler(repository, result)

	err := handler.Handle(context.Background(), stagedScanJob("audiobooks", "", true))
	if err == nil {
		t.Fatal("partial inventory unexpectedly succeeded")
	}
	if len(repository.inventoryUpdates) != 1 || repository.inventoryUpdates[0].Complete {
		t.Fatalf("inventory updates = %#v, want one incomplete update", repository.inventoryUpdates)
	}
	if repository.finalizations != 0 || repository.finish.Success || !repository.finish.Partial {
		t.Fatalf("finalizations=%d finish=%#v", repository.finalizations, repository.finish)
	}
}

func TestScanRootUnchangedDeltaQueuesNoRefinementJobs(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	audio := stagedSourceFile("Book/01.mp3", catalog.FileKindAudio, now)
	repository := &stagedRepositoryFake{
		run:        catalog.ScanRun{ID: uuid.New(), RootID: "audiobooks"},
		delta:      catalog.InventoryDelta{UnchangedFiles: []catalog.SourceFile{audio}},
		generation: catalog.CatalogGeneration{RootID: "audiobooks", Value: 3},
		tree:       grouping.Tree{RootID: "audiobooks", Files: []catalog.SourceFile{audio}},
	}
	handler := stagedScanHandler(repository, scanner.InventoryResult{
		RootID: "audiobooks", Scope: scanner.FullScope(),
		Files: []scanner.InventoryFile{stagedInventoryFile(audio)},
	})

	if err := handler.Handle(context.Background(), stagedScanJob("audiobooks", "", true)); err != nil {
		t.Fatal(err)
	}
	if len(repository.jobs) != 0 {
		t.Fatalf("jobs = %#v, want none for unchanged fingerprint", repository.jobs)
	}
}

func TestScanRootUsesDeterministicFingerprintDedupeKeys(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 456, time.UTC)
	audio := stagedSourceFile("Book/01.mp3", catalog.FileKindAudio, now)
	companion := stagedSourceFile("Book/book.opf", catalog.FileKindCompanion, now.Add(time.Second))
	companion.CompanionKind = catalog.CompanionOPF
	repository := &stagedRepositoryFake{
		run: catalog.ScanRun{ID: uuid.New(), RootID: "audiobooks"},
		delta: catalog.InventoryDelta{
			ProbeFiles: []catalog.SourceFile{audio}, InspectCompanions: []catalog.SourceFile{companion},
		},
		generation: catalog.CatalogGeneration{RootID: "audiobooks", Value: 2},
		tree:       grouping.Tree{RootID: "audiobooks", Files: []catalog.SourceFile{audio, companion}},
	}
	handler := stagedScanHandler(repository, scanner.InventoryResult{
		RootID: "audiobooks", Scope: scanner.FullScope(),
		Files: []scanner.InventoryFile{stagedInventoryFile(audio), stagedInventoryFile(companion)},
	})

	if err := handler.Handle(context.Background(), stagedScanJob("audiobooks", "", true)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"probe_file:" + audio.ID.String() + ":17:" + stagedFingerprintNanos(audio.ModifiedAt),
		"inspect_companion:" + companion.ID.String() + ":17:" + stagedFingerprintNanos(companion.ModifiedAt),
	}
	if !reflect.DeepEqual(repository.dedupeKeys, want) {
		t.Fatalf("dedupe keys = %v, want %v", repository.dedupeKeys, want)
	}
}

func TestSourceFileProberUsesResolverOpenedDescriptor(t *testing.T) {
	root := t.TempDir()
	relative := "Book/chapter.mp3"
	absolute := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte("descriptor-authority"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := library.NewResolver([]config.Root{{ID: "audiobooks", ContainerPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	probe := &descriptorProbeFake{metadata: media.Metadata{Title: "Book"}}

	got, err := NewSourceFileProber(resolver, probe).ProbeSourceFile(
		context.Background(), "audiobooks", relative,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Book" || string(probe.contents) != "descriptor-authority" || probe.pathCalls != 0 {
		t.Fatalf("metadata=%#v descriptor=%q pathCalls=%d", got, probe.contents, probe.pathCalls)
	}
}

func TestProbeFileSuccessFailureAndFingerprintConflict(t *testing.T) {
	tests := []struct {
		name        string
		probeErr    error
		storeErr    error
		changed     bool
		wantFailure string
		wantJobs    int
	}{
		{name: "success queues regroup after material change", changed: true, wantJobs: 1},
		{name: "sanitized failure remains regroupable", probeErr: errors.New(`C:\secret\book.m4b: invalid stream`), changed: true, wantFailure: "probe_failed", wantJobs: 1},
		{name: "stale fingerprint is a successful no-op", storeErr: catalog.ErrConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := stagedSourceFile("Book/book.m4b", catalog.FileKindAudio, time.Unix(10, 20).UTC())
			repository := &stagedRepositoryFake{
				file: file, probeChanged: test.changed, probeStoreErr: test.storeErr,
				generation: catalog.CatalogGeneration{RootID: "audiobooks", Value: 8},
			}
			handler := ProbeFileHandler{
				Repo:   repository,
				Prober: stagedProberFake{metadata: media.Metadata{Title: "Book"}, err: test.probeErr},
			}
			if err := handler.Handle(context.Background(), stagedFileJob(catalog.JobProbeFile, file)); err != nil {
				t.Fatal(err)
			}
			if repository.savedProbeFailure != test.wantFailure {
				t.Errorf("stored failure = %q, want %q", repository.savedProbeFailure, test.wantFailure)
			}
			if len(repository.jobs) != test.wantJobs {
				t.Errorf("jobs = %#v, want %d", repository.jobs, test.wantJobs)
			}
			if test.wantJobs == 1 && repository.dedupeKeys[0] != "regroup_scope:audiobooks:Book:8" {
				t.Errorf("regroup key = %q", repository.dedupeKeys[0])
			}
		})
	}
}

func TestProbeFileSkipsDescriptorWhenQueuedFingerprintIsAlreadyStale(t *testing.T) {
	file := stagedSourceFile("Book/book.m4b", catalog.FileKindAudio, time.Unix(10, 20).UTC())
	repository := &stagedRepositoryFake{file: file}
	prober := &countingStagedProber{}
	handler := ProbeFileHandler{Repo: repository, Prober: prober}
	job := stagedFileJob(catalog.JobProbeFile, file)
	job.Payload["size"] = strconv.FormatInt(file.Size+1, 10)

	if err := handler.Handle(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if prober.calls != 0 {
		t.Fatalf("probe calls = %d, want zero for stale fingerprint", prober.calls)
	}
}

func TestInspectCompanionStoresEvidenceAndQueuesRegroup(t *testing.T) {
	file := stagedSourceFile("Book/book.opf", catalog.FileKindCompanion, time.Unix(10, 0).UTC())
	file.CompanionKind = catalog.CompanionOPF
	repository := &stagedRepositoryFake{
		file: file, companionChanged: true,
		generation: catalog.CatalogGeneration{RootID: "audiobooks", Value: 4},
	}
	handler := InspectCompanionHandler{
		Repo: repository,
		Inspector: stagedInspectorFake{evidence: map[string]any{
			"title": "Book", "authors": []string{"Author"},
		}},
	}
	if err := handler.Handle(context.Background(), stagedFileJob(catalog.JobInspectCompanion, file)); err != nil {
		t.Fatal(err)
	}
	if repository.savedCompanion["title"] != "Book" ||
		!reflect.DeepEqual(repository.dedupeKeys, []string{"regroup_scope:audiobooks:Book:4"}) {
		t.Fatalf("evidence=%#v keys=%v", repository.savedCompanion, repository.dedupeKeys)
	}
}

func TestPublishSearchCoalescesStaleGeneration(t *testing.T) {
	repository := &stagedRepositoryFake{
		publishErr: catalog.ErrConflict,
		generation: catalog.CatalogGeneration{RootID: "audiobooks", Value: 9},
	}
	handler := PublishSearchHandler{Repo: repository}
	job := catalog.WorkerJob{
		Kind:    catalog.JobPublishSearch,
		Payload: map[string]any{"rootId": "audiobooks", "catalogGeneration": float64(8)},
	}
	if err := handler.Handle(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repository.dedupeKeys, []string{"publish_search:audiobooks"}) || len(repository.jobs) != 1 {
		t.Fatalf("replacement publication jobs=%#v keys=%v", repository.jobs, repository.dedupeKeys)
	}
}

func TestRegroupScopeAppliesRefinementThenCoalescesPublication(t *testing.T) {
	repository := &stagedRepositoryFake{
		generation: catalog.CatalogGeneration{RootID: "audiobooks", Value: 6},
		tree:       grouping.Tree{RootID: "audiobooks", ScopePath: "Book"},
	}
	handler := RegroupScopeHandler{
		Repo: repository,
		InferTree: func(grouping.Tree) grouping.Result {
			return grouping.Result{Books: []catalog.InferredBook{{GroupingKey: "book"}}}
		},
	}
	job := catalog.WorkerJob{
		Kind:    catalog.JobRegroupScope,
		Payload: map[string]any{"rootId": "audiobooks", "scopePath": "Book", "catalogGeneration": float64(5)},
	}
	if err := handler.Handle(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if repository.refinements != 1 || !reflect.DeepEqual(repository.dedupeKeys, []string{"publish_search:audiobooks"}) {
		t.Fatalf("refinements=%d keys=%v", repository.refinements, repository.dedupeKeys)
	}
}

func stagedScanHandler(repository *stagedRepositoryFake, result scanner.InventoryResult) *ScanRootHandler {
	return &ScanRootHandler{
		Repo:         repository,
		Inventory:    stagedInventoryFake{result: result},
		Roots:        map[string]config.Root{"audiobooks": {ID: "audiobooks", ContainerPath: "ignored"}},
		ValidateRoot: func(string) error { return nil },
		LoadTree:     repository.LoadGroupingTree,
		InferTree: func(tree grouping.Tree) grouping.Result {
			book := catalog.InferredBook{
				RootID: tree.RootID, SourceFolder: "Book", GroupingKey: "book",
				Title: "Book", MetadataSource: catalog.MetadataInferred,
				ReviewState: catalog.ReviewUnreviewed, Confidence: 0.5,
			}
			for _, file := range tree.Files {
				if file.FileKind == catalog.FileKindAudio {
					book.SourceFileIDs = append(book.SourceFileIDs, file.ID)
				}
			}
			return grouping.Result{Books: []catalog.InferredBook{book}}
		},
	}
}

type stagedInventoryFake struct {
	result scanner.InventoryResult
}

func (fake stagedInventoryFake) Scan(
	context.Context,
	config.Root,
	scanner.Scope,
) (scanner.InventoryResult, error) {
	return fake.result, nil
}

type stagedRepositoryFake struct {
	run               catalog.ScanRun
	delta             catalog.InventoryDelta
	generation        catalog.CatalogGeneration
	tree              grouping.Tree
	file              catalog.SourceFile
	finish            catalog.ScanResult
	inventoryUpdates  []catalog.InventoryUpdate
	events            []string
	jobs              []catalog.WorkerJob
	dedupeKeys        []string
	finalizations     int
	refinements       int
	probeCalls        int
	probeChanged      bool
	probeStoreErr     error
	companionChanged  bool
	publishErr        error
	savedProbeFailure string
	savedCompanion    map[string]any
	activeDedupeKeys  map[string]catalog.WorkerJob
}

func (fake *stagedRepositoryFake) BeginScan(context.Context, string) (catalog.ScanRun, error) {
	return fake.run, nil
}

func (fake *stagedRepositoryFake) UpsertSourceFiles(
	context.Context,
	uuid.UUID,
	[]catalog.SourceFile,
) error {
	return nil
}

func (fake *stagedRepositoryFake) FinalizeSuccessfulScan(
	context.Context,
	uuid.UUID,
	[]catalog.InferredBook,
) error {
	return nil
}

func (fake *stagedRepositoryFake) ListBooks(
	context.Context,
	catalog.BookQuery,
) (catalog.Page[catalog.Book], error) {
	return catalog.Page[catalog.Book]{}, nil
}

func (fake *stagedRepositoryFake) ApplyInventory(
	_ context.Context,
	update catalog.InventoryUpdate,
) (catalog.InventoryDelta, error) {
	fake.events = append(fake.events, "apply")
	fake.inventoryUpdates = append(fake.inventoryUpdates, update)
	return fake.delta, nil
}

func (fake *stagedRepositoryFake) LoadGroupingTree(
	context.Context,
	string,
	string,
) (grouping.Tree, error) {
	fake.events = append(fake.events, "load_tree")
	return fake.tree, nil
}

func (fake *stagedRepositoryFake) FinalizeInventoryAndPublish(
	_ context.Context,
	_ catalog.InventoryFinalization,
) (catalog.CatalogGeneration, error) {
	fake.events = append(fake.events, "finalize")
	fake.finalizations++
	return fake.generation, nil
}

func (fake *stagedRepositoryFake) CurrentCatalogGeneration(
	context.Context,
	string,
) (catalog.CatalogGeneration, error) {
	return fake.generation, nil
}

func (fake *stagedRepositoryFake) FinishScopedInventory(
	context.Context,
	uuid.UUID,
	string,
) error {
	return nil
}

func (fake *stagedRepositoryFake) ApplyGroupingRefinement(
	_ context.Context,
	_ catalog.GroupingRefinement,
) (catalog.CatalogGeneration, error) {
	fake.refinements++
	return fake.generation, nil
}

func (fake *stagedRepositoryFake) FinishScan(
	_ context.Context,
	_ uuid.UUID,
	result catalog.ScanResult,
) error {
	fake.finish = result
	return nil
}

func (fake *stagedRepositoryFake) EnqueueDeduplicated(
	_ context.Context,
	job catalog.WorkerJob,
	key string,
) (catalog.WorkerJob, error) {
	if fake.activeDedupeKeys == nil {
		fake.activeDedupeKeys = make(map[string]catalog.WorkerJob)
	}
	if active, exists := fake.activeDedupeKeys[key]; exists {
		return active, nil
	}
	fake.events = append(fake.events, "enqueue:"+job.Kind)
	fake.jobs = append(fake.jobs, job)
	fake.dedupeKeys = append(fake.dedupeKeys, key)
	fake.activeDedupeKeys[key] = job
	return job, nil
}

func (fake *stagedRepositoryFake) GetSourceFile(context.Context, uuid.UUID) (catalog.SourceFile, error) {
	return fake.file, nil
}

func (fake *stagedRepositoryFake) SaveProbeResult(
	_ context.Context,
	_ catalog.SourceFile,
	_ media.Metadata,
	failure string,
) (catalog.CatalogGeneration, bool, error) {
	fake.savedProbeFailure = failure
	return fake.generation, fake.probeChanged, fake.probeStoreErr
}

func (fake *stagedRepositoryFake) SaveCompanionInspection(
	_ context.Context,
	_ catalog.SourceFile,
	evidence map[string]any,
	_ string,
) (catalog.CatalogGeneration, bool, error) {
	fake.savedCompanion = evidence
	return fake.generation, fake.companionChanged, nil
}

func (fake *stagedRepositoryFake) LoadSearchProjection(
	context.Context,
	string,
	int64,
) (catalog.SearchProjection, error) {
	return catalog.SearchProjection{
		Snapshot: catalog.SearchSnapshot{RootID: "audiobooks", CatalogGeneration: 8},
	}, nil
}

func (fake *stagedRepositoryFake) PublishSearchProjection(
	context.Context,
	catalog.SearchProjection,
) error {
	return fake.publishErr
}

type stagedProberFake struct {
	metadata media.Metadata
	err      error
}

type countingStagedProber struct {
	calls int
}

func (fake *countingStagedProber) ProbeSourceFile(
	context.Context,
	string,
	string,
) (media.Metadata, error) {
	fake.calls++
	return media.Metadata{}, nil
}

func (fake stagedProberFake) ProbeSourceFile(context.Context, string, string) (media.Metadata, error) {
	return fake.metadata, fake.err
}

type stagedInspectorFake struct {
	evidence map[string]any
}

func (fake stagedInspectorFake) InspectCompanion(
	context.Context,
	catalog.SourceFile,
) (map[string]any, error) {
	return fake.evidence, nil
}

type descriptorProbeFake struct {
	metadata  media.Metadata
	contents  []byte
	pathCalls int
}

func (fake *descriptorProbeFake) Probe(context.Context, string) (media.Metadata, error) {
	fake.pathCalls++
	return media.Metadata{}, errors.New("path probe must not be used")
}

func (fake *descriptorProbeFake) ProbeOpenFile(_ context.Context, file *os.File) (media.Metadata, error) {
	contents, err := io.ReadAll(file)
	if err != nil {
		return media.Metadata{}, err
	}
	fake.contents = contents
	return fake.metadata, nil
}

func stagedSourceFile(relativePath, kind string, modified time.Time) catalog.SourceFile {
	return catalog.SourceFile{
		ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(relativePath)), RootID: "audiobooks",
		RelativePath: relativePath, FileKind: kind, Size: 17, ModifiedAt: modified,
	}
}

func stagedInventoryFile(file catalog.SourceFile) scanner.InventoryFile {
	return scanner.InventoryFile{
		ID: file.ID, RootID: file.RootID, RelativePath: file.RelativePath,
		Name: filepath.Base(file.RelativePath), Folder: filepath.ToSlash(filepath.Dir(file.RelativePath)),
		Kind: scanner.FileKind(file.FileKind), CompanionKind: file.CompanionKind,
		Size: file.Size, ModifiedAt: file.ModifiedAt,
	}
}

func stagedScanJob(rootID, scopePath string, full bool) catalog.WorkerJob {
	return catalog.WorkerJob{
		Kind:    catalog.JobScanRoot,
		Payload: map[string]any{"rootId": rootID, "scopePath": scopePath, "full": full},
	}
}

func stagedFileJob(kind string, file catalog.SourceFile) catalog.WorkerJob {
	return catalog.WorkerJob{
		Kind: kind,
		Payload: map[string]any{
			"fileId": file.ID.String(), "rootId": file.RootID,
			"relativePath":  file.RelativePath,
			"size":          strconv.FormatInt(file.Size, 10),
			"modifiedNanos": strconv.FormatInt(file.ModifiedAt.UnixNano(), 10),
		},
	}
}

func stagedFingerprintNanos(value time.Time) string {
	return strconv.FormatInt(value.UnixNano(), 10)
}
