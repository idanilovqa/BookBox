package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/companion"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/grouping"
	"bookbox.local/server/internal/library"
	"bookbox.local/server/internal/media"
	"bookbox.local/server/internal/scanner"
)

const companionInputLimit = 2 << 20

var discInventoryFolder = regexp.MustCompile(`(?i)^(disc|disk|cd)[\s._-]*(\d+|one|two|three|four|five|six|seven|eight|nine|ten)$`)

type PostgresStagedRepository struct {
	*catalog.PostgresRepository
}

func NewPostgresStagedRepository(repository *catalog.PostgresRepository) *PostgresStagedRepository {
	return &PostgresStagedRepository{PostgresRepository: repository}
}

func (repository *PostgresStagedRepository) LoadGroupingTree(
	ctx context.Context,
	rootID string,
	scopePath string,
) (grouping.Tree, error) {
	files, corrections, err := repository.LoadGroupingScope(ctx, rootID, scopePath)
	if err != nil {
		return grouping.Tree{}, err
	}
	return grouping.Tree{
		RootID: rootID, ScopePath: scopePath, Files: files, Corrections: corrections,
	}, nil
}

func inferFullInventoryTree(
	tree grouping.Tree,
	infer func(grouping.Tree) grouping.Result,
) grouping.Result {
	if infer == nil {
		infer = grouping.InferTree
	}
	audioScopes := make(map[uuid.UUID]string)
	scopeSet := make(map[string]struct{})
	for _, file := range tree.Files {
		if file.MissingAt != nil || file.FileKind != catalog.FileKindAudio {
			continue
		}
		scope := fullInventoryAudioScope(file.RelativePath)
		audioScopes[file.ID] = scope
		scopeSet[scope] = struct{}{}
	}
	scopes := make([]string, 0, len(scopeSet))
	for scope := range scopeSet {
		scopes = append(scopes, scope)
	}
	sort.Slice(scopes, func(i, j int) bool {
		if len(scopes[i]) != len(scopes[j]) {
			return len(scopes[i]) > len(scopes[j])
		}
		return scopes[i] < scopes[j]
	})

	result := grouping.Result{Evidence: make(map[string]catalog.GroupingEvidence)}
	for _, scope := range scopes {
		partition := grouping.Tree{RootID: tree.RootID, ScopePath: scope}
		for _, file := range tree.Files {
			if file.MissingAt != nil {
				continue
			}
			if file.FileKind == catalog.FileKindAudio {
				if audioScopes[file.ID] == scope {
					partition.Files = append(partition.Files, file)
				}
				continue
			}
			if owner, ok := fullInventoryFileScope(file.RelativePath, scopes); ok && owner == scope {
				partition.Files = append(partition.Files, file)
			}
		}
		for _, correction := range tree.Corrections {
			if correction.SourceFolder == scope {
				partition.Corrections = append(partition.Corrections, correction)
			}
		}
		grouped := infer(partition)
		result.Books = append(result.Books, grouped.Books...)
		for key, evidence := range grouped.Evidence {
			result.Evidence[key] = evidence
		}
	}
	sort.Slice(result.Books, func(i, j int) bool {
		if result.Books[i].SourceFolder != result.Books[j].SourceFolder {
			return result.Books[i].SourceFolder < result.Books[j].SourceFolder
		}
		if result.Books[i].Title != result.Books[j].Title {
			return result.Books[i].Title < result.Books[j].Title
		}
		return result.Books[i].GroupingKey < result.Books[j].GroupingKey
	})
	return result
}

func fullInventoryAudioScope(relativePath string) string {
	directory := path.Dir(relativePath)
	if directory == "." {
		return ""
	}
	if discInventoryFolder.MatchString(path.Base(directory)) {
		directory = path.Dir(directory)
		if directory == "." {
			return ""
		}
	}
	return directory
}

func fullInventoryFileScope(relativePath string, scopes []string) (string, bool) {
	directory := path.Dir(relativePath)
	if directory == "." {
		directory = ""
	}
	for _, scope := range scopes {
		if scope == "" {
			if directory == "" {
				return scope, true
			}
			continue
		}
		if directory == scope || strings.HasPrefix(directory, scope+"/") {
			return scope, true
		}
	}
	return "", false
}

type StagedScanRepository interface {
	ApplyInventory(context.Context, catalog.InventoryUpdate) (catalog.InventoryDelta, error)
	DrainReadyFingerprintOutbox(context.Context, time.Time, int) (int, error)
	CurrentCatalogGeneration(context.Context, string) (catalog.CatalogGeneration, error)
	FinalizeInventoryAndPublish(
		context.Context,
		catalog.InventoryFinalization,
	) (catalog.CatalogGeneration, error)
	ApplyGroupingRefinement(
		context.Context,
		catalog.GroupingRefinement,
	) (catalog.CatalogGeneration, error)
	FinalizeScopedInventoryAndPublish(
		context.Context,
		uuid.UUID,
		catalog.GroupingRefinement,
	) (catalog.CatalogGeneration, error)
}

func (handler *ScanRootHandler) handleInventory(
	ctx context.Context,
	job catalog.WorkerJob,
	root config.Root,
) error {
	repository, ok := handler.Repo.(StagedScanRepository)
	if !ok || handler.LoadTree == nil {
		return errors.New("staged scan repository is unavailable")
	}
	scopePath := optionalPayloadString(job.Payload, "scopePath")
	if scopePath == "" {
		scopePath = optionalPayloadString(job.Payload, "relativePath")
	}
	full, _ := job.Payload["full"].(bool)
	if full && scopePath != "" {
		return errors.New("full inventory cannot include a relative path")
	}
	if scopePath == "" {
		full = true
	}
	var scope scanner.Scope
	var err error
	if full {
		scope = scanner.FullScope()
	} else {
		scope, err = scanner.PathScope(scopePath)
		if err != nil {
			return err
		}
	}
	run, err := handler.Repo.BeginScan(ctx, root.ID)
	if err != nil {
		return fmt.Errorf("begin inventory: %w", err)
	}
	result, scanErr := handler.Inventory.Scan(ctx, root, scope)
	if scanErr != nil {
		return handler.failScan(ctx, run.ID, "source inventory failed", scanErr)
	}
	files := make([]catalog.InventoryFile, 0, len(result.Files))
	for _, file := range result.Files {
		files = append(files, catalog.InventoryFile{
			ID: file.ID, RootID: file.RootID, RelativePath: file.RelativePath,
			FileKind: string(file.Kind), CompanionKind: file.CompanionKind,
			Size: file.Size, ModifiedAt: file.ModifiedAt,
		})
	}
	complete := len(result.Warnings) == 0
	delta, err := repository.ApplyInventory(ctx, catalog.InventoryUpdate{
		ScanRunID: run.ID, RootID: root.ID, ScopePath: scopePath,
		Complete: complete, Files: files,
	})
	if err != nil {
		return handler.failScan(ctx, run.ID, "source inventory indexing failed", err)
	}
	if !complete {
		finishErr := handler.Repo.FinishScan(ctx, run.ID, catalog.ScanResult{
			Success: false, Partial: true, Error: "source inventory was incomplete",
		})
		if finishErr != nil {
			return fmt.Errorf("finish partial inventory: %w", finishErr)
		}
		return errors.New("source inventory was incomplete")
	}
	tree, err := handler.LoadTree(ctx, root.ID, scopePath)
	if err != nil {
		return handler.failScan(ctx, run.ID, "load grouping evidence failed", err)
	}
	tree = excludeDeferredMissingFiles(tree, delta.MissingFileIDs)
	infer := handler.InferTree
	if infer == nil {
		infer = grouping.InferTree
	}
	var grouped grouping.Result
	if full {
		grouped = inferFullInventoryTree(tree, infer)
	} else {
		grouped = infer(tree)
	}
	if full {
		if _, err := repository.FinalizeInventoryAndPublish(ctx, catalog.InventoryFinalization{
			ScanRunID: run.ID, RootID: root.ID, Books: grouped.Books, Evidence: grouped.Evidence,
		}); err != nil {
			return handler.failScan(ctx, run.ID, "provisional catalog publication failed", err)
		}
	} else {
		current, err := repository.CurrentCatalogGeneration(ctx, root.ID)
		if err != nil {
			return handler.failScan(ctx, run.ID, "load catalog generation failed", err)
		}
		if _, err := repository.FinalizeScopedInventoryAndPublish(ctx, run.ID, catalog.GroupingRefinement{
			RootID: root.ID, ScopePath: scopePath, ExpectedGeneration: current.Value,
			Books: grouped.Books, Evidence: grouped.Evidence,
		}); err != nil {
			return handler.failScan(ctx, run.ID, "scoped catalog publication failed", err)
		}
	}
	if _, err := repository.DrainReadyFingerprintOutbox(ctx, time.Now().UTC(), 256); err != nil {
		return fmt.Errorf("dispatch fingerprint work: %w", err)
	}
	if err := queueIncompleteBooks(ctx, handler.Repo, root.ID); err != nil {
		return fmt.Errorf("queue metadata enrichment: %w", err)
	}
	return nil
}

func excludeDeferredMissingFiles(
	tree grouping.Tree,
	missingFileIDs []uuid.UUID,
) grouping.Tree {
	if len(missingFileIDs) == 0 {
		return tree
	}
	missing := make(map[uuid.UUID]struct{}, len(missingFileIDs))
	for _, id := range missingFileIDs {
		missing[id] = struct{}{}
	}
	files := make([]catalog.SourceFile, 0, len(tree.Files))
	for _, file := range tree.Files {
		if _, excluded := missing[file.ID]; !excluded {
			files = append(files, file)
		}
	}
	tree.Files = files
	return tree
}

type SourceFileResolver interface {
	OpenRegular(string, string) (*os.File, library.ResolvedPath, error)
}

type SourceFileProber struct {
	resolver SourceFileResolver
	probe    media.OpenFileProbe
}

func NewSourceFileProber(resolver SourceFileResolver, probe media.OpenFileProbe) *SourceFileProber {
	return &SourceFileProber{resolver: resolver, probe: probe}
}

func (prober *SourceFileProber) ProbeSourceFile(
	ctx context.Context,
	rootID string,
	relativePath string,
) (media.Metadata, error) {
	if prober == nil || prober.resolver == nil || prober.probe == nil {
		return media.Metadata{}, media.ErrProbeFailed
	}
	file, _, err := prober.resolver.OpenRegular(rootID, relativePath)
	if err != nil {
		return media.Metadata{}, media.ErrProbeFailed
	}
	defer file.Close()
	return prober.probe.ProbeOpenFile(ctx, file)
}

type SourceProber interface {
	ProbeSourceFile(context.Context, string, string) (media.Metadata, error)
}

type ProbeRepository interface {
	GetSourceFile(context.Context, uuid.UUID) (catalog.SourceFile, error)
	SaveProbeResult(
		context.Context,
		catalog.SourceFile,
		media.Metadata,
		string,
	) (catalog.CatalogGeneration, bool, error)
	EnqueueDeduplicated(context.Context, catalog.WorkerJob, string) (catalog.WorkerJob, error)
}

type ProbeFileHandler struct {
	Repo   ProbeRepository
	Prober SourceProber
}

func (handler ProbeFileHandler) Handle(ctx context.Context, job catalog.WorkerJob) error {
	if handler.Repo == nil || handler.Prober == nil || job.Kind != catalog.JobProbeFile {
		return errors.New("invalid probe job")
	}
	file, stale, err := loadFingerprintFile(ctx, handler.Repo, job)
	if err != nil || stale {
		return err
	}
	metadata, probeErr := handler.Prober.ProbeSourceFile(ctx, file.RootID, file.RelativePath)
	failure := ""
	if probeErr != nil {
		failure = "probe_failed"
	}
	generation, changed, err := handler.Repo.SaveProbeResult(ctx, file, metadata, failure)
	if errors.Is(err, catalog.ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	if changed {
		return enqueueRegroup(ctx, handler.Repo, file.RootID, fullInventoryAudioScope(file.RelativePath), generation.Value)
	}
	return nil
}

type fingerprintRepository interface {
	GetSourceFile(context.Context, uuid.UUID) (catalog.SourceFile, error)
}

func loadFingerprintFile(
	ctx context.Context,
	repository fingerprintRepository,
	job catalog.WorkerJob,
) (catalog.SourceFile, bool, error) {
	fileID, err := requiredPayloadUUID(job.Payload, "fileId")
	if err != nil {
		return catalog.SourceFile{}, false, err
	}
	file, err := repository.GetSourceFile(ctx, fileID)
	if err != nil {
		return catalog.SourceFile{}, false, err
	}
	rootID, err := requiredPayloadString(job.Payload, "rootId")
	if err != nil {
		return catalog.SourceFile{}, false, err
	}
	relativePath, err := requiredPayloadString(job.Payload, "relativePath")
	if err != nil {
		return catalog.SourceFile{}, false, err
	}
	size, err := payloadInt64(job.Payload, "size")
	if err != nil {
		return catalog.SourceFile{}, false, err
	}
	modified, err := payloadInt64(job.Payload, "modifiedNanos")
	if err != nil {
		return catalog.SourceFile{}, false, err
	}
	stale := file.RootID != rootID || file.RelativePath != relativePath ||
		file.Size != size || file.ModifiedAt.UnixNano() != modified
	return file, stale, nil
}

type CompanionInspector interface {
	InspectCompanion(context.Context, catalog.SourceFile) (map[string]any, error)
}

type CompanionRepository interface {
	fingerprintRepository
	SaveCompanionInspection(
		context.Context,
		catalog.SourceFile,
		map[string]any,
		string,
	) (catalog.CatalogGeneration, bool, error)
	EnqueueDeduplicated(context.Context, catalog.WorkerJob, string) (catalog.WorkerJob, error)
}

type InspectCompanionHandler struct {
	Repo      CompanionRepository
	Inspector CompanionInspector
}

func (handler InspectCompanionHandler) Handle(ctx context.Context, job catalog.WorkerJob) error {
	if handler.Repo == nil || handler.Inspector == nil || job.Kind != catalog.JobInspectCompanion {
		return errors.New("invalid companion job")
	}
	file, stale, err := loadFingerprintFile(ctx, handler.Repo, job)
	if err != nil || stale {
		return err
	}
	evidence, inspectErr := handler.Inspector.InspectCompanion(ctx, file)
	failure := ""
	if inspectErr != nil {
		failure = "inspection_failed"
		evidence = map[string]any{}
	}
	generation, changed, err := handler.Repo.SaveCompanionInspection(ctx, file, evidence, failure)
	if errors.Is(err, catalog.ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	if changed {
		return enqueueRegroup(ctx, handler.Repo, file.RootID, path.Dir(file.RelativePath), generation.Value)
	}
	return nil
}

type ResolverCompanionInspector struct {
	resolver SourceFileResolver
}

func NewCompanionInspector(resolver SourceFileResolver) *ResolverCompanionInspector {
	return &ResolverCompanionInspector{resolver: resolver}
}

func (inspector *ResolverCompanionInspector) InspectCompanion(
	_ context.Context,
	file catalog.SourceFile,
) (map[string]any, error) {
	handle, _, err := inspector.resolver.OpenRegular(file.RootID, file.RelativePath)
	if err != nil {
		return nil, companion.ErrMalformedInput
	}
	defer handle.Close()
	kind := file.CompanionKind
	if kind == catalog.CompanionPlaylist {
		kind = strings.TrimPrefix(strings.ToLower(path.Ext(file.RelativePath)), ".")
	}
	evidence, err := companion.Parse(kind, io.Reader(handle), companionInputLimit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"title": evidence.Title, "authors": evidence.Creators,
		"references": evidence.ReferencedFiles, "playlistOrder": evidence.PlaylistOrder,
	}, nil
}

type RegroupRepository interface {
	LoadGroupingTree(context.Context, string, string) (grouping.Tree, error)
	CurrentCatalogGeneration(context.Context, string) (catalog.CatalogGeneration, error)
	ApplyGroupingRefinement(
		context.Context,
		catalog.GroupingRefinement,
	) (catalog.CatalogGeneration, error)
	ListBooks(context.Context, catalog.BookQuery) (catalog.Page[catalog.Book], error)
	EnqueueDeduplicated(context.Context, catalog.WorkerJob, string) (catalog.WorkerJob, error)
}

type RegroupScopeHandler struct {
	Repo      RegroupRepository
	InferTree func(grouping.Tree) grouping.Result
}

func (handler RegroupScopeHandler) Handle(ctx context.Context, job catalog.WorkerJob) error {
	rootID, err := requiredPayloadString(job.Payload, "rootId")
	if err != nil || handler.Repo == nil || job.Kind != catalog.JobRegroupScope {
		return errors.New("invalid regroup job")
	}
	scope := optionalPayloadString(job.Payload, "scopePath")
	expected, err := payloadInt64(job.Payload, "catalogGeneration")
	if err != nil {
		return err
	}
	tree, err := handler.Repo.LoadGroupingTree(ctx, rootID, scope)
	if err != nil {
		return err
	}
	infer := handler.InferTree
	if infer == nil {
		infer = grouping.InferTree
	}
	result := infer(tree)
	if scope == "" {
		result = inferFullInventoryTree(tree, infer)
	}
	generation, err := handler.Repo.ApplyGroupingRefinement(ctx, catalog.GroupingRefinement{
		RootID: rootID, ScopePath: scope, ExpectedGeneration: expected,
		Books: result.Books, Evidence: result.Evidence,
	})
	if errors.Is(err, catalog.ErrConflict) {
		current, currentErr := handler.Repo.CurrentCatalogGeneration(ctx, rootID)
		if currentErr != nil {
			return currentErr
		}
		return enqueueRegroup(ctx, handler.Repo, rootID, scope, current.Value)
	} else if err != nil {
		return err
	}
	if err := enqueuePublish(ctx, handler.Repo, rootID, generation.Value); err != nil {
		return err
	}
	if err := queueIncompleteBooks(ctx, handler.Repo, rootID); err != nil {
		return fmt.Errorf("queue metadata enrichment: %w", err)
	}
	return nil
}

type SearchPublicationRepository interface {
	LoadSearchProjection(context.Context, string, int64) (catalog.SearchProjection, error)
	PublishSearchProjection(context.Context, catalog.SearchProjection) error
	CurrentCatalogGeneration(context.Context, string) (catalog.CatalogGeneration, error)
	EnqueueDeduplicated(context.Context, catalog.WorkerJob, string) (catalog.WorkerJob, error)
}

type PublishSearchHandler struct {
	Repo SearchPublicationRepository
}

func (handler PublishSearchHandler) Handle(ctx context.Context, job catalog.WorkerJob) error {
	rootID, err := requiredPayloadString(job.Payload, "rootId")
	if err != nil || handler.Repo == nil || job.Kind != catalog.JobPublishSearch {
		return errors.New("invalid search publication job")
	}
	generation, err := payloadInt64(job.Payload, "catalogGeneration")
	if err != nil {
		return err
	}
	projection, err := handler.Repo.LoadSearchProjection(ctx, rootID, generation)
	if errors.Is(err, catalog.ErrConflict) {
		return enqueueCurrentPublication(ctx, handler.Repo, rootID)
	}
	if err != nil {
		return err
	}
	if err := handler.Repo.PublishSearchProjection(ctx, projection); errors.Is(err, catalog.ErrConflict) {
		return enqueueCurrentPublication(ctx, handler.Repo, rootID)
	} else {
		return err
	}
}

func enqueueCurrentPublication(
	ctx context.Context,
	repository SearchPublicationRepository,
	rootID string,
) error {
	current, err := repository.CurrentCatalogGeneration(ctx, rootID)
	if err != nil {
		return err
	}
	return enqueuePublish(ctx, repository, rootID, current.Value)
}

func enqueueRegroup(
	ctx context.Context,
	repository interface {
		EnqueueDeduplicated(context.Context, catalog.WorkerJob, string) (catalog.WorkerJob, error)
	},
	rootID string,
	scope string,
	generation int64,
) error {
	if scope == "." {
		scope = ""
	}
	key := "regroup_scope:" + rootID + ":" + scope + ":" + strconv.FormatInt(generation, 10)
	_, err := repository.EnqueueDeduplicated(ctx, catalog.WorkerJob{
		ID: uuid.New(), Kind: catalog.JobRegroupScope,
		Payload: map[string]any{
			"rootId": rootID, "scopePath": scope,
			"catalogGeneration": generation,
		},
	}, key)
	return err
}

func enqueuePublish(
	ctx context.Context,
	repository interface {
		EnqueueDeduplicated(context.Context, catalog.WorkerJob, string) (catalog.WorkerJob, error)
	},
	rootID string,
	generation int64,
) error {
	if generation <= 0 {
		return errors.New("invalid catalog generation")
	}
	_, err := repository.EnqueueDeduplicated(ctx, catalog.WorkerJob{
		ID: uuid.New(), Kind: catalog.JobPublishSearch,
		Payload: map[string]any{"rootId": rootID, "catalogGeneration": generation},
	}, catalog.PublishSearchDedupeKey(rootID, generation))
	return err
}

func payloadInt64(payload map[string]any, key string) (int64, error) {
	switch value := payload[key].(type) {
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
			value < float64(-1<<63) || value >= float64(1<<63) {
			break
		}
		return int64(value), nil
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err == nil {
			return parsed, nil
		}
	}
	return 0, fmt.Errorf("job payload %s is invalid", key)
}

func optionalPayloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}
