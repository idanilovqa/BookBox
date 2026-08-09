package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bookbox.local/server/internal/catalog"
	"github.com/google/uuid"
)

func TestReorganizeFilesHandlerJournalsMovesBeforePublishingGrouping(t *testing.T) {
	t.Parallel()

	operation := testReorganizationOperation()
	repository := &reorganizationRepositoryFake{operation: operation}
	mover := &confinedMoverFake{}
	handler := ReorganizeFilesHandler{Repository: repository, Mover: mover}

	err := handler.Handle(context.Background(), catalog.WorkerJob{Payload: map[string]any{
		"operationId": operation.ID.String(), "actor": operation.Actor,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := repository.events, []string{
		"operation:running", "move:1:moved", "move:2:moved",
		"grouping:commit", "operation:succeeded", "job:scan_root", "job:publish_search",
	}; !equalStrings(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if len(mover.moves) != 2 || len(mover.rollbacks) != 0 {
		t.Fatalf("moves = %q, rollbacks = %q", mover.moves, mover.rollbacks)
	}
	if repository.catalogGeneration != operation.CatalogGeneration+1 {
		t.Fatalf("catalog generation = %d, want %d", repository.catalogGeneration, operation.CatalogGeneration+1)
	}
}

func TestReorganizeFilesHandlerRollsBackAndMarksAttentionWhenNecessary(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		moveErrorAt   int
		rollbackError bool
		fingerprint   bool
		destination   bool
		cancelled     bool
		wantStatus    string
		wantMoved     int
		wantRollback  int
	}{
		{name: "failure before moves", moveErrorAt: 1, wantStatus: "rolled_back"},
		{name: "failure after two moves", moveErrorAt: 3, wantStatus: "rolled_back", wantMoved: 2, wantRollback: 2},
		{name: "rollback failure", moveErrorAt: 3, rollbackError: true, wantStatus: "needs_attention", wantMoved: 2, wantRollback: 2},
		{name: "changed source fingerprint", fingerprint: true, wantStatus: "rolled_back"},
		{name: "destination appeared", destination: true, wantStatus: "rolled_back"},
		{name: "cancelled", cancelled: true, wantStatus: "rolled_back"},
	} {
		t.Run(test.name, func(t *testing.T) {
			operation := testReorganizationOperation()
			if test.moveErrorAt == 3 {
				operation.Moves = append(operation.Moves, catalog.ReorganizationMove{
					Order: 3, FileID: uuid.New(), SourcePath: "Incoming/notes.nfo",
					DestinationPath: "Author/Book/notes.nfo", Role: "metadata",
					ExpectedSize: 3, ExpectedModifiedAt: time.Unix(30, 0).UTC(),
				})
			}
			repository := &reorganizationRepositoryFake{operation: operation}
			mover := &confinedMoverFake{
				moveErrorAt: test.moveErrorAt, rollbackError: test.rollbackError,
				fingerprintConflict: test.fingerprint, destinationConflict: test.destination,
			}
			handler := ReorganizeFilesHandler{Repository: repository, Mover: mover}
			ctx := context.Background()
			if test.cancelled {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			err := handler.Handle(ctx, catalog.WorkerJob{Payload: map[string]any{
				"operationId": operation.ID.String(), "actor": operation.Actor,
			}})
			if err == nil {
				t.Fatal("Handle returned nil")
			}
			if repository.status != test.wantStatus {
				t.Fatalf("status = %q, want %q", repository.status, test.wantStatus)
			}
			if len(mover.moves) != test.wantMoved || len(mover.rollbacks) != test.wantRollback {
				t.Fatalf("moves = %d, rollbacks = %d", len(mover.moves), len(mover.rollbacks))
			}
			if repository.groupingCommitted {
				t.Fatal("grouping was committed after an incomplete filesystem operation")
			}
			if test.wantRollback > 0 && !test.rollbackError &&
				!strings.Contains(strings.Join(repository.events, ","), ":rolled_back") {
				t.Fatalf("rollback was not journaled: %q", repository.events)
			}
			if test.rollbackError && !strings.Contains(strings.Join(repository.events, ","), ":failed") {
				t.Fatalf("failed rollback was not journaled: %q", repository.events)
			}
			if strings.Contains(repository.errorText, "Incoming") ||
				strings.Contains(repository.errorText, "C:\\") || strings.Contains(repository.errorText, "injected") {
				t.Fatalf("journal leaked sensitive error detail: %q", repository.errorText)
			}
		})
	}
}

func testReorganizationOperation() catalog.ReorganizationOperation {
	bookID := uuid.New()
	return catalog.ReorganizationOperation{
		ID: uuid.New(), Actor: "owner", BookID: bookID, RootID: "audio", CatalogGeneration: 12,
		Groups: []catalog.GroupingGroup{{BookID: &bookID, GroupingKey: "book"}},
		Moves: []catalog.ReorganizationMove{
			{Order: 1, FileID: uuid.New(), SourcePath: "Incoming/book.m4b", DestinationPath: "Author/Book/book.m4b", Role: "audio", ExpectedSize: 10, ExpectedModifiedAt: time.Unix(10, 0).UTC()},
			{Order: 2, FileID: uuid.New(), SourcePath: "Incoming/cover.jpg", DestinationPath: "Author/Book/cover.jpg", Role: "cover", ExpectedSize: 2, ExpectedModifiedAt: time.Unix(20, 0).UTC()},
		},
	}
}

type confinedMoverFake struct {
	moves, rollbacks                         []string
	moveErrorAt                              int
	rollbackError                            bool
	fingerprintConflict, destinationConflict bool
}

func (mover *confinedMoverFake) Validate(_ context.Context, _ string, move catalog.ReorganizationMove) error {
	if mover.fingerprintConflict {
		return catalog.ErrReorganizationStale
	}
	if mover.destinationConflict {
		return catalog.ErrDestinationConflict
	}
	return nil
}

func (mover *confinedMoverFake) Move(_ context.Context, _ string, move catalog.ReorganizationMove) error {
	if mover.moveErrorAt == len(mover.moves)+1 {
		return errors.New("injected C:\\private move failure")
	}
	mover.moves = append(mover.moves, move.DestinationPath)
	return nil
}

func (mover *confinedMoverFake) Rollback(_ context.Context, _ string, move catalog.ReorganizationMove) error {
	mover.rollbacks = append(mover.rollbacks, move.SourcePath)
	if mover.rollbackError {
		return errors.New("injected rollback failure")
	}
	return nil
}

type reorganizationRepositoryFake struct {
	operation         catalog.ReorganizationOperation
	events            []string
	status, errorText string
	groupingCommitted bool
	catalogGeneration int64
}

func (repository *reorganizationRepositoryFake) LoadReorganization(_ context.Context, id uuid.UUID, actor string) (catalog.ReorganizationOperation, error) {
	if id != repository.operation.ID || actor != repository.operation.Actor {
		return catalog.ReorganizationOperation{}, catalog.ErrReorganizationStale
	}
	return repository.operation, nil
}

func (repository *reorganizationRepositoryFake) SetReorganizationStatus(_ context.Context, _ uuid.UUID, status, errorText string) error {
	repository.status, repository.errorText = status, errorText
	repository.events = append(repository.events, "operation:"+status)
	return nil
}

func (repository *reorganizationRepositoryFake) SetReorganizationMoveStatus(_ context.Context, _ uuid.UUID, order int, status string) error {
	repository.events = append(repository.events, "move:"+string(rune('0'+order))+":"+status)
	return nil
}

func (repository *reorganizationRepositoryFake) CommitReorganizationGrouping(_ context.Context, _ catalog.ReorganizationOperation) error {
	repository.groupingCommitted = true
	repository.catalogGeneration = repository.operation.CatalogGeneration + 1
	repository.status = "succeeded"
	repository.events = append(repository.events,
		"grouping:commit", "operation:succeeded", "job:scan_root", "job:publish_search",
	)
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
