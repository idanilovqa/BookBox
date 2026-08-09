package catalog

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBuildReorganizationPlanIsDeterministicAndIncludesCompanions(t *testing.T) {
	t.Parallel()

	modified := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	audio := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	cover := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	opf := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	input := ReorganizationPlanInput{
		RootID: "audio", CatalogGeneration: 7, CurrentCatalogGeneration: 7,
		Groups:        []GroupingGroup{{GroupingKey: "dune", FileIDs: []uuid.UUID{audio}}},
		TargetFolders: map[string]string{"dune": " Frank Herbert / Dune: Messiah "},
		Files: []ReorganizationPlanFile{
			{ID: opf, RelativePath: "Incoming/Dune/book.opf", Role: "metadata", Size: 30, ModifiedAt: modified, GroupingKey: "dune"},
			{ID: audio, RelativePath: "Incoming/Dune/01 - Dune?.m4b", Role: "audio", Size: 100, ModifiedAt: modified, GroupingKey: "dune"},
			{ID: cover, RelativePath: "Incoming/Dune/cover.jpg", Role: "cover", Size: 20, ModifiedAt: modified, GroupingKey: "dune"},
		},
	}

	first, err := BuildReorganizationPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildReorganizationPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("plan is nondeterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	want := []ReorganizationMove{
		{Order: 1, FileID: audio, SourcePath: "Incoming/Dune/01 - Dune?.m4b", DestinationPath: "Frank Herbert/Dune Messiah/01 - Dune.m4b", Role: "audio", ExpectedSize: 100, ExpectedModifiedAt: modified},
		{Order: 2, FileID: cover, SourcePath: "Incoming/Dune/cover.jpg", DestinationPath: "Frank Herbert/Dune Messiah/cover.jpg", Role: "cover", ExpectedSize: 20, ExpectedModifiedAt: modified},
		{Order: 3, FileID: opf, SourcePath: "Incoming/Dune/book.opf", DestinationPath: "Frank Herbert/Dune Messiah/book.opf", Role: "sidecar", ExpectedSize: 30, ExpectedModifiedAt: modified},
	}
	if !reflect.DeepEqual(first.Moves, want) {
		t.Fatalf("moves = %#v, want %#v", first.Moves, want)
	}
}

func TestBuildReorganizationPlanRejectsStaleOrUnsafeMoves(t *testing.T) {
	t.Parallel()

	fileID := uuid.New()
	base := ReorganizationPlanInput{
		RootID: "audio", CatalogGeneration: 4, CurrentCatalogGeneration: 4,
		Groups:        []GroupingGroup{{GroupingKey: "book", FileIDs: []uuid.UUID{fileID}}},
		TargetFolders: map[string]string{"book": "Author/Book"},
		Files: []ReorganizationPlanFile{{
			ID: fileID, RelativePath: "Incoming/book.m4b", Role: "audio",
			Size: 10, ModifiedAt: time.Unix(100, 0).UTC(), GroupingKey: "book",
		}},
	}

	for _, test := range []struct {
		name   string
		mutate func(*ReorganizationPlanInput)
		want   error
	}{
		{name: "stale generation", mutate: func(input *ReorganizationPlanInput) { input.CurrentCatalogGeneration++ }, want: ErrReorganizationStale},
		{name: "traversal", mutate: func(input *ReorganizationPlanInput) { input.TargetFolders["book"] = "../Outside" }, want: ErrInvalidData},
		{name: "absolute", mutate: func(input *ReorganizationPlanInput) { input.TargetFolders["book"] = "C:/Outside" }, want: ErrInvalidData},
		{name: "destination collision", mutate: func(input *ReorganizationPlanInput) { input.Destinations = staticDestinationInspector{collision: true} }, want: ErrDestinationConflict},
		{name: "existing destination", mutate: func(input *ReorganizationPlanInput) { input.Destinations = staticDestinationInspector{exists: true} }, want: ErrDestinationConflict},
		{name: "symlink ancestor", mutate: func(input *ReorganizationPlanInput) { input.Destinations = staticDestinationInspector{symlink: true} }, want: ErrInvalidData},
		{name: "cross root resolution", mutate: func(input *ReorganizationPlanInput) {
			input.Destinations = staticDestinationInspector{outsideRoot: true}
		}, want: ErrInvalidData},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.TargetFolders = map[string]string{"book": base.TargetFolders["book"]}
			test.mutate(&input)
			_, err := BuildReorganizationPlan(input)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSharedReorganizationFileRequiresOneExplicitOwner(t *testing.T) {
	file := ReorganizationPlanFile{Shared: true}
	if sharedFileAssignmentConflicts(file, 2, 1) {
		t.Fatal("one explicit shared-file owner was rejected")
	}
	if !sharedFileAssignmentConflicts(file, 2, 0) || !sharedFileAssignmentConflicts(file, 2, 2) {
		t.Fatal("ambiguous shared-file ownership was accepted")
	}
}

type staticDestinationInspector struct {
	exists, collision, symlink, outsideRoot bool
}

func (inspector staticDestinationInspector) InspectDestination(
	_ string,
	_ []ReorganizationMove,
) (ReorganizationDestinationState, error) {
	return ReorganizationDestinationState{
		Exists: inspector.exists, Collision: inspector.collision,
		SymlinkAncestor: inspector.symlink, OutsideRoot: inspector.outsideRoot,
	}, nil
}
