package worker

import (
	"testing"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/grouping"
	"github.com/google/uuid"
)

func TestInferFullInventoryTreePreservesAudiobookFolderScopes(t *testing.T) {
	files := []catalog.SourceFile{
		inventoryGroupingAudio("Author/Book/chapter01.mp3"),
		inventoryGroupingAudio("Author/Book/chapter02.mp3"),
		inventoryGroupingAudio("Disc Saga/Disc 1/chapter01.mp3"),
		inventoryGroupingAudio("Disc Saga/Disk 2/chapter02.mp3"),
		inventoryGroupingAudio("Mixed Shelf/Alpha.mp3"),
		inventoryGroupingAudio("Mixed Shelf/Beta.mp3"),
		inventoryGroupingAudio("loose-weak-01.mp3"),
		inventoryGroupingAudio("loose-weak-02.mp3"),
	}

	result := inferFullInventoryTree(grouping.Tree{
		RootID: "audiobooks", Files: files,
	}, grouping.InferTree)

	counts := make(map[string]int)
	assigned := make(map[uuid.UUID]int)
	for _, book := range result.Books {
		counts[book.SourceFolder]++
		for _, fileID := range book.SourceFileIDs {
			if book.FileRoles[fileID] == catalog.FileRoleAudio {
				assigned[fileID]++
			}
		}
	}
	if counts["Author/Book"] != 1 || counts["Disc Saga"] != 1 ||
		counts["Mixed Shelf"] != 2 || counts[""] != 1 {
		t.Fatalf("book counts by scope = %#v", counts)
	}
	for _, file := range files {
		if assigned[file.ID] != 1 {
			t.Fatalf("audio %s assigned %d times", file.RelativePath, assigned[file.ID])
		}
	}
}

func inventoryGroupingAudio(relativePath string) catalog.SourceFile {
	return catalog.SourceFile{
		ID: uuid.New(), RootID: "audiobooks", RelativePath: relativePath,
		FileKind: catalog.FileKindAudio, ProbeStatus: catalog.ProbePending,
		Media: map[string]any{},
	}
}
