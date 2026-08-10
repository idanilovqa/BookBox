package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ReorganizationPlanFile struct {
	ID           uuid.UUID
	RelativePath string
	Role         string
	Size         int64
	ModifiedAt   time.Time
	GroupingKey  string
	Shared       bool
}

type ReorganizationDestinationState struct {
	Exists          bool
	Collision       bool
	SymlinkAncestor bool
	OutsideRoot     bool
}

type ReorganizationDestinationInspector interface {
	InspectDestination(string, []ReorganizationMove) (ReorganizationDestinationState, error)
}

type ReorganizationPlanInput struct {
	RootID                   string
	CatalogGeneration        int64
	CurrentCatalogGeneration int64
	Groups                   []GroupingGroup
	TargetFolders            map[string]string
	Files                    []ReorganizationPlanFile
	Destinations             ReorganizationDestinationInspector
}

func BuildReorganizationPlan(input ReorganizationPlanInput) (ReorganizationPreview, error) {
	if input.RootID == "" || input.CatalogGeneration <= 0 ||
		input.CatalogGeneration != input.CurrentCatalogGeneration {
		if input.CatalogGeneration != input.CurrentCatalogGeneration {
			return ReorganizationPreview{}, ErrReorganizationStale
		}
		return ReorganizationPreview{}, ErrInvalidData
	}
	groupKeys := make(map[string]struct{}, len(input.Groups))
	targetKeys := make(map[string]struct{}, len(input.Groups))
	for _, group := range input.Groups {
		key := strings.TrimSpace(group.GroupingKey)
		if key == "" {
			return ReorganizationPreview{}, ErrInvalidData
		}
		if _, duplicate := groupKeys[key]; duplicate {
			return ReorganizationPreview{}, ErrInvalidData
		}
		if _, exists := input.TargetFolders[key]; !exists {
			return ReorganizationPreview{}, ErrInvalidData
		}
		target, err := sanitizeTargetFolder(input.TargetFolders[key])
		if err != nil {
			return ReorganizationPreview{}, err
		}
		canonicalTarget := strings.ToLower(target)
		if _, duplicate := targetKeys[canonicalTarget]; duplicate {
			return ReorganizationPreview{}, ErrDestinationConflict
		}
		targetKeys[canonicalTarget] = struct{}{}
		groupKeys[key] = struct{}{}
	}
	if len(input.TargetFolders) != len(groupKeys) {
		return ReorganizationPreview{}, ErrInvalidData
	}
	moves := make([]ReorganizationMove, 0, len(input.Files))
	destinations := make(map[string]struct{}, len(input.Files))
	for _, file := range input.Files {
		if file.ID == uuid.Nil || file.Size < 0 || file.ModifiedAt.IsZero() {
			return ReorganizationPreview{}, ErrInvalidData
		}
		if _, ok := groupKeys[file.GroupingKey]; !ok {
			return ReorganizationPreview{}, ErrInvalidData
		}
		target, err := sanitizeTargetFolder(input.TargetFolders[file.GroupingKey])
		if err != nil {
			return ReorganizationPreview{}, err
		}
		name := sanitizeFilename(path.Base(file.RelativePath))
		if name == "" {
			return ReorganizationPreview{}, ErrInvalidData
		}
		destination := path.Join(target, name)
		if err := ValidateRelativePath(file.RelativePath); err != nil {
			return ReorganizationPreview{}, ErrInvalidData
		}
		if err := ValidateRelativePath(destination); err != nil {
			return ReorganizationPreview{}, ErrInvalidData
		}
		if _, exists := destinations[strings.ToLower(destination)]; exists {
			return ReorganizationPreview{}, ErrDestinationConflict
		}
		destinations[strings.ToLower(destination)] = struct{}{}
		role := file.Role
		if role != "audio" && role != "cover" {
			role = "sidecar"
		}
		moves = append(moves, ReorganizationMove{
			FileID: file.ID, SourcePath: file.RelativePath,
			DestinationPath: destination, Role: role,
			ExpectedSize: file.Size, ExpectedModifiedAt: file.ModifiedAt.UTC(),
		})
	}
	sort.Slice(moves, func(i, j int) bool {
		left, right := moveRoleOrder(moves[i].Role), moveRoleOrder(moves[j].Role)
		if left != right {
			return left < right
		}
		return moves[i].SourcePath < moves[j].SourcePath
	})
	for index := range moves {
		moves[index].Order = index + 1
	}
	if len(moves) == 0 {
		return ReorganizationPreview{}, ErrInvalidData
	}
	if input.Destinations != nil {
		state, err := input.Destinations.InspectDestination(input.RootID, moves)
		if err != nil {
			return ReorganizationPreview{}, fmt.Errorf("inspect reorganization destinations: %w", err)
		}
		if state.Exists || state.Collision {
			return ReorganizationPreview{}, ErrDestinationConflict
		}
		if state.SymlinkAncestor || state.OutsideRoot {
			return ReorganizationPreview{}, ErrInvalidData
		}
	}
	return ReorganizationPreview{RootID: input.RootID, CatalogGeneration: input.CatalogGeneration, Moves: moves}, nil
}

func sanitizeTargetFolder(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" || strings.HasPrefix(value, "/") || windowsDrivePath.MatchString(value) {
		return "", ErrInvalidData
	}
	segments := strings.Split(value, "/")
	clean := make([]string, 0, len(segments))
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalidData
		}
		segment = sanitizeFilename(segment)
		if segment == "" {
			return "", ErrInvalidData
		}
		clean = append(clean, segment)
	}
	result := path.Join(clean...)
	if err := ValidateRelativePath(result); err != nil {
		return "", ErrInvalidData
	}
	return result, nil
}

func sanitizeFilename(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) || strings.ContainsRune(`<>:"/\\|?*`, character) {
			continue
		}
		builder.WriteRune(character)
	}
	return strings.TrimRight(strings.TrimSpace(builder.String()), ".")
}

func moveRoleOrder(role string) int {
	switch role {
	case "audio":
		return 0
	case "cover":
		return 1
	default:
		return 2
	}
}

func (repository *PostgresRepository) BuildReorganizationPreview(ctx context.Context, request ReorganizationRequest) (ReorganizationPreview, error) {
	if request.BookID == uuid.Nil || request.CatalogGeneration <= 0 || strings.TrimSpace(request.Actor) == "" {
		return ReorganizationPreview{}, ErrInvalidData
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return ReorganizationPreview{}, fmt.Errorf("begin reorganization preview: %w", err)
	}
	defer tx.Rollback(ctx)
	var rootID, scopePath string
	var generation int64
	if err := tx.QueryRow(ctx, `
		select b.root_id, b.source_folder, coalesce(g.generation,0)
		from books b left join catalog_generations g on g.root_id=b.root_id
		where b.id=$1 and b.missing_at is null for update of b
	`, request.BookID).Scan(&rootID, &scopePath, &generation); err != nil {
		return ReorganizationPreview{}, mapNotFound(err, "load reorganization book")
	}
	if generation != request.CatalogGeneration {
		return ReorganizationPreview{}, ErrReorganizationStale
	}
	rows, err := tx.Query(ctx, `
		select distinct on (sf.id) sf.id,sf.relative_path,
		       case sf.file_kind when 'audio' then 'audio' when 'cover' then 'cover' else 'sidecar' end,
		       sf.size,sf.modified_at,coalesce(b.grouping_key,''),
		       count(bf.book_id) over(partition by sf.id)>1
		from source_files sf
		left join book_files bf on bf.file_id=sf.id and bf.root_id=sf.root_id
		left join books b on b.id=bf.book_id and b.root_id=bf.root_id and b.missing_at is null
		where sf.root_id=$1 and sf.missing_at is null
		  and ($2='' or left(sf.relative_path,length($2)+1)=$2 || '/')
		  and sf.file_kind in ('audio','cover','companion')
		order by sf.id,b.grouping_key
	`, rootID, scopePath)
	if err != nil {
		return ReorganizationPreview{}, fmt.Errorf("load reorganization files: %w", err)
	}
	defer rows.Close()
	files := []ReorganizationPlanFile{}
	for rows.Next() {
		var file ReorganizationPlanFile
		if err := rows.Scan(&file.ID, &file.RelativePath, &file.Role, &file.Size, &file.ModifiedAt, &file.GroupingKey, &file.Shared); err != nil {
			return ReorganizationPreview{}, fmt.Errorf("scan reorganization file: %w", err)
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return ReorganizationPreview{}, fmt.Errorf("read reorganization files: %w", err)
	}
	explicitGroups := make(map[uuid.UUID]string)
	explicitCounts := make(map[uuid.UUID]int)
	for _, group := range request.Groups {
		for _, fileID := range group.FileIDs {
			explicitCounts[fileID]++
			explicitGroups[fileID] = group.GroupingKey
		}
	}
	knownFiles := make(map[uuid.UUID]struct{}, len(files))
	for _, file := range files {
		knownFiles[file.ID] = struct{}{}
	}
	for fileID, count := range explicitCounts {
		if _, exists := knownFiles[fileID]; !exists {
			return ReorganizationPreview{}, ErrInvalidData
		}
		if count > 1 {
			return ReorganizationPreview{}, ErrDestinationConflict
		}
	}
	audioFolderGroups := make(map[string]string)
	for index := range files {
		if sharedFileAssignmentConflicts(files[index], len(request.Groups), explicitCounts[files[index].ID]) {
			return ReorganizationPreview{}, ErrDestinationConflict
		}
		if groupingKey, exists := explicitGroups[files[index].ID]; exists {
			files[index].GroupingKey = groupingKey
		}
		if files[index].Role == FileRoleAudio {
			if explicitCounts[files[index].ID] != 1 {
				return ReorganizationPreview{}, ErrInvalidData
			}
			folder := path.Dir(files[index].RelativePath)
			if existing, exists := audioFolderGroups[folder]; exists && existing != files[index].GroupingKey {
				audioFolderGroups[folder] = ""
			} else {
				audioFolderGroups[folder] = files[index].GroupingKey
			}
		}
	}
	for index := range files {
		if files[index].Role == FileRoleAudio {
			continue
		}
		if _, exists := request.TargetFolders[files[index].GroupingKey]; !exists {
			folderGroup := audioFolderGroups[path.Dir(files[index].RelativePath)]
			if folderGroup == "" {
				return ReorganizationPreview{}, ErrInvalidData
			}
			files[index].GroupingKey = folderGroup
		}
	}
	plannedGroups := append([]GroupingGroup(nil), request.Groups...)
	for index := range plannedGroups {
		seen := make(map[uuid.UUID]struct{}, len(plannedGroups[index].FileIDs))
		for _, id := range plannedGroups[index].FileIDs {
			seen[id] = struct{}{}
		}
		for _, file := range files {
			if file.GroupingKey == plannedGroups[index].GroupingKey {
				if _, exists := seen[file.ID]; !exists {
					plannedGroups[index].FileIDs = append(plannedGroups[index].FileIDs, file.ID)
					seen[file.ID] = struct{}{}
				}
			}
		}
	}
	preview, err := BuildReorganizationPlan(ReorganizationPlanInput{
		RootID: rootID, CatalogGeneration: request.CatalogGeneration,
		CurrentCatalogGeneration: generation, Groups: plannedGroups,
		TargetFolders: request.TargetFolders, Files: files,
		Destinations: repository.reorganizationDestinationInspector,
	})
	if err != nil {
		return ReorganizationPreview{}, err
	}
	preview.OperationID = uuid.New()
	groupsJSON, _ := json.Marshal(plannedGroups)
	targetsJSON, _ := json.Marshal(request.TargetFolders)
	if _, err := tx.Exec(ctx, `insert into reorganization_operations(id,root_id,book_id,catalog_generation,actor,status,groups,target_folders) values($1,$2,$3,$4,$5,'previewed',$6,$7)`, preview.OperationID, rootID, request.BookID, request.CatalogGeneration, strings.TrimSpace(request.Actor), groupsJSON, targetsJSON); err != nil {
		return ReorganizationPreview{}, classifyConstraint(err, "save reorganization preview")
	}
	for _, move := range preview.Moves {
		if _, err := tx.Exec(ctx, `insert into reorganization_moves(operation_id,move_order,file_id,root_id,source_path,destination_path,role,expected_size,expected_modified_at,state) values($1,$2,$3,$4,$5,$6,$7,$8,$9,'planned')`, preview.OperationID, move.Order, move.FileID, rootID, move.SourcePath, move.DestinationPath, move.Role, move.ExpectedSize, move.ExpectedModifiedAt); err != nil {
			return ReorganizationPreview{}, classifyConstraint(err, "save reorganization move")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ReorganizationPreview{}, fmt.Errorf("commit reorganization preview: %w", err)
	}
	return preview, nil
}

func sharedFileAssignmentConflicts(file ReorganizationPlanFile, groupCount, explicitCount int) bool {
	return file.Shared && groupCount > 1 && explicitCount != 1
}

func (repository *PostgresRepository) LoadReorganization(ctx context.Context, id uuid.UUID, actor string) (ReorganizationOperation, error) {
	if id == uuid.Nil || strings.TrimSpace(actor) == "" {
		return ReorganizationOperation{}, ErrInvalidData
	}
	var operation ReorganizationOperation
	var groupsJSON, targetsJSON []byte
	err := repository.pool.QueryRow(ctx, `select id,actor,book_id,root_id,catalog_generation,status,groups,target_folders from reorganization_operations where id=$1 and actor=$2`, id, strings.TrimSpace(actor)).Scan(
		&operation.ID, &operation.Actor, &operation.BookID, &operation.RootID,
		&operation.CatalogGeneration, &operation.Status, &groupsJSON, &targetsJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReorganizationOperation{}, ErrReorganizationStale
	}
	if err != nil {
		return ReorganizationOperation{}, fmt.Errorf("load reorganization: %w", err)
	}
	if operation.Status != "queued" && operation.Status != "running" {
		return ReorganizationOperation{}, ErrReorganizationStale
	}
	if err := json.Unmarshal(groupsJSON, &operation.Groups); err != nil {
		return ReorganizationOperation{}, ErrInvalidData
	}
	if err := json.Unmarshal(targetsJSON, &operation.TargetFolders); err != nil {
		return ReorganizationOperation{}, ErrInvalidData
	}
	rows, err := repository.pool.Query(ctx, `select move_order,file_id,source_path,destination_path,role,expected_size,expected_modified_at from reorganization_moves where operation_id=$1 order by move_order`, id)
	if err != nil {
		return ReorganizationOperation{}, fmt.Errorf("load reorganization moves: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var move ReorganizationMove
		if err := rows.Scan(&move.Order, &move.FileID, &move.SourcePath, &move.DestinationPath, &move.Role, &move.ExpectedSize, &move.ExpectedModifiedAt); err != nil {
			return ReorganizationOperation{}, fmt.Errorf("scan reorganization move: %w", err)
		}
		operation.Moves = append(operation.Moves, move)
	}
	if err := rows.Err(); err != nil {
		return ReorganizationOperation{}, fmt.Errorf("read reorganization moves: %w", err)
	}
	return operation, nil
}

func (repository *PostgresRepository) SetReorganizationStatus(ctx context.Context, id uuid.UUID, status, errorText string) error {
	if id == uuid.Nil || !map[string]bool{"running": true, "succeeded": true, "rolled_back": true, "needs_attention": true}[status] {
		return ErrInvalidData
	}
	if len(errorText) > 240 {
		errorText = errorText[:240]
	}
	result, err := repository.pool.Exec(ctx, `
		update reorganization_operations
		set status=$2,last_error=nullif($3,''),updated_at=now()
		where id=$1 and (
			($2='running' and status in ('queued','running')) or
			($2 in ('succeeded','rolled_back','needs_attention') and status='running')
		)
	`, id, status, errorText)
	if err != nil {
		return classifyConstraint(err, "update reorganization status")
	}
	if result.RowsAffected() != 1 {
		return ErrReorganizationStale
	}
	return nil
}

func (repository *PostgresRepository) SetReorganizationMoveStatus(ctx context.Context, id uuid.UUID, order int, status string) error {
	if id == uuid.Nil || order < 0 || !map[string]bool{"moved": true, "rolled_back": true, "failed": true}[status] {
		return ErrInvalidData
	}
	result, err := repository.pool.Exec(ctx, `
		update reorganization_moves set state=$3,last_error=null
		where operation_id=$1 and move_order=$2 and (
			($3='moved' and state='planned') or
			($3 in ('rolled_back','failed') and state in ('planned','moved'))
		)
	`, id, order, status)
	if err != nil {
		return classifyConstraint(err, "update reorganization move")
	}
	if result.RowsAffected() != 1 {
		return ErrReorganizationStale
	}
	return nil
}

func (repository *PostgresRepository) CommitReorganizationGrouping(ctx context.Context, operation ReorganizationOperation) error {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reorganization commit: %w", err)
	}
	defer tx.Rollback(ctx)
	var current int64
	if err := tx.QueryRow(ctx, `select generation from catalog_generations where root_id=$1 for update`, operation.RootID).Scan(&current); err != nil {
		return mapNotFound(err, "lock reorganization generation")
	}
	if current != operation.CatalogGeneration {
		return ErrReorganizationStale
	}
	oldScope := commonPathAncestor(operation.Moves, false)
	newScope := commonPathAncestor(operation.Moves, true)
	for _, move := range operation.Moves {
		result, err := tx.Exec(ctx, `update source_files set relative_path=$3,updated_at=now() where id=$1 and root_id=$2 and relative_path=$4 and size=$5 and modified_at=$6 and missing_at is null`, move.FileID, operation.RootID, move.DestinationPath, move.SourcePath, move.ExpectedSize, move.ExpectedModifiedAt)
		if err != nil {
			return classifyConstraint(err, "commit reorganization path")
		}
		if result.RowsAffected() != 1 {
			return ErrReorganizationStale
		}
	}
	folderFiles, err := loadGroupingFolderFiles(ctx, tx, operation.RootID, newScope)
	if err != nil {
		return fmt.Errorf("load reorganized grouping files: %w", err)
	}
	movedIDs := make(map[uuid.UUID]struct{}, len(operation.Moves))
	for _, move := range operation.Moves {
		movedIDs[move.FileID] = struct{}{}
	}
	movedFiles := make([]groupingFolderFile, 0, len(operation.Moves))
	filesByID := make(map[uuid.UUID]groupingFolderFile, len(operation.Moves))
	for _, file := range folderFiles {
		if _, moved := movedIDs[file.id]; !moved {
			continue
		}
		movedFiles = append(movedFiles, file)
		filesByID[file.id] = file
	}
	assignmentCounts := make(map[uuid.UUID]int, len(folderFiles))
	bookIDs := make([]uuid.UUID, 0, len(operation.Groups))
	for index := range operation.Groups {
		group := &operation.Groups[index]
		groupFolder, err := sanitizeTargetFolder(operation.TargetFolders[group.GroupingKey])
		if err != nil {
			return err
		}
		var audioIDs []uuid.UUID
		roles := make(map[uuid.UUID]string, len(group.FileIDs))
		for _, fileID := range group.FileIDs {
			file, exists := filesByID[fileID]
			if !exists {
				return ErrReorganizationStale
			}
			role := groupingFileRole(file)
			if role == "" {
				return ErrInvalidData
			}
			roles[fileID] = role
			assignmentCounts[fileID]++
			if role == FileRoleAudio {
				audioIDs = append(audioIDs, fileID)
			}
		}
		if len(audioIDs) == 0 {
			return ErrInvalidData
		}
		var bookID uuid.UUID
		if group.BookID == nil {
			bookID, err = createCorrectedBook(ctx, tx, operation.RootID, groupFolder, group.GroupingKey, audioIDs)
			if err != nil {
				return err
			}
			group.BookID = &bookID
		} else {
			bookID = *group.BookID
			result, err := tx.Exec(ctx, `update books set source_folder=$3,grouping_key=$4,missing_at=null,updated_at=now() where id=$1 and root_id=$2`, bookID, operation.RootID, groupFolder, group.GroupingKey)
			if err != nil {
				return classifyConstraint(err, "update reorganized book")
			}
			if result.RowsAffected() != 1 {
				return ErrReorganizationStale
			}
		}
		bookIDs = append(bookIDs, bookID)
		if err := replaceBookFiles(ctx, tx, bookID, operation.RootID, group.FileIDs, roles); err != nil {
			return err
		}
		if err := upsertManualGroupingEvidence(ctx, tx, bookID, operation.RootID, group.FileIDs, roles, filesByID); err != nil {
			return err
		}
	}
	for _, file := range movedFiles {
		count := assignmentCounts[file.id]
		role := groupingFileRole(file)
		if (role == FileRoleCover && count < 1) || (role != FileRoleCover && count != 1) {
			return ErrInvalidData
		}
	}
	for _, group := range operation.Groups {
		groupFolder, err := sanitizeTargetFolder(operation.TargetFolders[group.GroupingKey])
		if err != nil {
			return err
		}
		groupsJSON, err := json.Marshal([]GroupingGroup{group})
		if err != nil {
			return ErrInvalidData
		}
		if _, err := tx.Exec(ctx, `insert into grouping_corrections(id,root_id,source_folder,groups,updated_at) values($1,$2,$3,$4,now()) on conflict(root_id,source_folder) do update set groups=excluded.groups,updated_at=now()`, uuid.New(), operation.RootID, groupFolder, groupsJSON); err != nil {
			return classifyConstraint(err, "save reorganized grouping")
		}
	}
	if _, err := tx.Exec(ctx, `update books set grouping_locked=true,review_state='confirmed',missing_at=null,updated_at=now() where id=any($1::uuid[])`, bookIDs); err != nil {
		return fmt.Errorf("lock reorganized grouping: %w", err)
	}
	if oldScope != newScope {
		if _, err := tx.Exec(ctx, `update books set missing_at=now(),updated_at=now() where root_id=$1 and source_folder=$2 and missing_at is null and not(id=any($3::uuid[]))`, operation.RootID, oldScope, bookIDs); err != nil {
			return fmt.Errorf("retire prior grouping: %w", err)
		}
	}
	var generation int64
	if err := tx.QueryRow(ctx, `update catalog_generations set generation=generation+1,updated_at=now() where root_id=$1 returning generation`, operation.RootID).Scan(&generation); err != nil {
		return fmt.Errorf("increment reorganization generation: %w", err)
	}
	for _, followup := range []struct {
		kind, key string
		payload   map[string]any
	}{
		{
			kind: JobScanRoot, key: JobScanRoot + ":" + operation.RootID + ":reorganization",
			payload: map[string]any{"rootId": operation.RootID, "relativePath": "", "full": true},
		},
		{
			kind: JobPublishSearch, key: PublishSearchDedupeKey(operation.RootID, generation),
			payload: map[string]any{"rootId": operation.RootID, "catalogGeneration": generation},
		},
	} {
		payload, err := marshalObject(followup.payload)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			insert into worker_jobs(id,kind,dedupe_key,payload,run_after,max_attempts)
			values($1,$2,$3,$4,$5,5)
			on conflict(kind,dedupe_key)
				where dedupe_key is not null and status in ('queued','running')
			do nothing
		`, uuid.New(), followup.kind, followup.key, payload, repository.now()); err != nil {
			return classifyConstraint(err, "queue reorganization follow-up")
		}
	}
	result, err := tx.Exec(ctx, `
		update reorganization_operations
		set status='succeeded',last_error=null,updated_at=now()
		where id=$1 and status='running'
	`, operation.ID)
	if err != nil {
		return classifyConstraint(err, "complete reorganization operation")
	}
	if result.RowsAffected() != 1 {
		return ErrReorganizationStale
	}
	return tx.Commit(ctx)
}

func commonPathAncestor(moves []ReorganizationMove, destination bool) string {
	if len(moves) == 0 {
		return ""
	}
	value := moves[0].SourcePath
	if destination {
		value = moves[0].DestinationPath
	}
	segments := strings.Split(path.Dir(value), "/")
	for _, move := range moves[1:] {
		value = move.SourcePath
		if destination {
			value = move.DestinationPath
		}
		other := strings.Split(path.Dir(value), "/")
		limit := len(segments)
		if len(other) < limit {
			limit = len(other)
		}
		index := 0
		for index < limit && segments[index] == other[index] {
			index++
		}
		segments = segments[:index]
	}
	if len(segments) == 0 || (len(segments) == 1 && segments[0] == ".") {
		return ""
	}
	return strings.Join(segments, "/")
}

func (repository *PostgresRepository) QueueReorganization(ctx context.Context, operationID uuid.UUID, actor string) (WorkerJob, error) {
	if operationID == uuid.Nil || strings.TrimSpace(actor) == "" {
		return WorkerJob{}, ErrInvalidData
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return WorkerJob{}, fmt.Errorf("begin queue reorganization: %w", err)
	}
	defer tx.Rollback(ctx)
	var rootID string
	var generation, current int64
	err = tx.QueryRow(ctx, `select o.root_id,o.catalog_generation,coalesce(g.generation,0) from reorganization_operations o left join catalog_generations g on g.root_id=o.root_id where o.id=$1 and o.actor=$2 and o.status='previewed' for update of o`, operationID, strings.TrimSpace(actor)).Scan(&rootID, &generation, &current)
	if errors.Is(err, pgx.ErrNoRows) || generation != current {
		return WorkerJob{}, ErrReorganizationStale
	}
	if err != nil {
		return WorkerJob{}, fmt.Errorf("lock reorganization preview: %w", err)
	}
	if _, err := tx.Exec(ctx, `update reorganization_operations set status='queued',updated_at=now() where id=$1`, operationID); err != nil {
		return WorkerJob{}, fmt.Errorf("queue reorganization operation: %w", err)
	}
	job := WorkerJob{ID: uuid.New(), Kind: JobReorganizeFiles, Payload: map[string]any{"operationId": operationID.String(), "actor": strings.TrimSpace(actor)}, RunAfter: repository.now(), MaxAttempts: 5}
	payload, _ := json.Marshal(job.Payload)
	if _, err := tx.Exec(ctx, `insert into worker_jobs(id,kind,payload,run_after,max_attempts) values($1,$2,$3,$4,$5)`, job.ID, job.Kind, payload, job.RunAfter, job.MaxAttempts); err != nil {
		return WorkerJob{}, classifyConstraint(err, "enqueue reorganization")
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkerJob{}, fmt.Errorf("commit queued reorganization: %w", err)
	}
	return job, nil
}
