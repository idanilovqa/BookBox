package grouping

import (
	"bookbox.local/server/internal/catalog"
	"fmt"
	"testing"
)

func lastHeroTracks(ext string) []catalog.SourceFile {
	var files []catalog.SourceFile
	for n := 1; n <= 9; n++ {
		files = append(files, treeFile(fmt.Sprintf("Terry Pratchett/Discworld/(#27) The Last Hero/DW27 - The Last Hero - %02d (enhanced)%s", n, ext), catalog.FileKindAudio, "", map[string]any{}))
	}
	return files
}
func TestEnhancedNumberedFilesAreOneBook(t *testing.T) {
	files := lastHeroTracks(".m4a")
	result := InferTree(testTree("Terry Pratchett/Discworld/(#27) The Last Hero", files...))
	if len(result.Books) != 1 || result.Books[0].Title != "The Last Hero" || len(result.Books[0].SourceFileIDs) != 9 {
		t.Fatalf("split tracks: %+v", result.Books)
	}
}
func TestEnhancedNumberedFilesNeedCoherentFolderEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]catalog.SourceFile) []catalog.SourceFile
	}{
		{"m4b books", func(_ []catalog.SourceFile) []catalog.SourceFile { return lastHeroTracks(".m4b") }},
		{"gap", func(f []catalog.SourceFile) []catalog.SourceFile { return f[1:] }},
		{"different book", func(f []catalog.SourceFile) []catalog.SourceFile {
			f[8].RelativePath = "Terry Pratchett/Discworld/(#27) The Last Hero/DW28 - Another Book - 09 (enhanced).m4a"
			return f
		}},
		{"real titles", func(f []catalog.SourceFile) []catalog.SourceFile {
			for n := range f {
				f[n].Media = map[string]any{"album": fmt.Sprint("Different Book ", n)}
			}
			return f
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := InferTree(testTree("Terry Pratchett/Discworld/(#27) The Last Hero", tc.edit(lastHeroTracks(".m4a"))...))
			if len(result.Books) == 1 && result.Books[0].ReviewState != catalog.ReviewNeedsReview {
				t.Fatalf("unsafe grouping: %+v", result.Books)
			}
		})
	}
}

func TestNamedTracksPreserveMeaningfulAlphanumericTitles(t *testing.T) {
	for _, title := range []string{"HTML5 for Web Designers", "AB12345 History"} {
		var files []catalog.SourceFile
		for n := 1; n <= 2; n++ {
			files = append(files, treeFile(fmt.Sprintf("Author/%s/%s - %02d.mp3", title, title, n), catalog.FileKindAudio, "", map[string]any{}))
		}
		result := InferTree(testTree("Author/"+title, files...))
		if len(result.Books) != 1 || result.Books[0].Title != title {
			t.Fatalf("meaningful prefix lost: %+v", result.Books)
		}
	}
}
