package transfer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/library"
)

func TestBookManifestPreservesFolderOrUsesM4BStemForSplit(t *testing.T) {
	t.Parallel()

	t.Run("normal folder", func(t *testing.T) {
		t.Parallel()
		fixture := newBookManifestFixture(t,
			"Author/Original Folder/chapter.mp3",
			"Author/Original Folder/cover.jpg",
		)
		book := fixture.addBook("Author/Original Folder", "folder-key", "Original Book",
			"Author/Original Folder/chapter.mp3",
			"Author/Original Folder/cover.jpg",
		)
		manifest := fixture.build(t, book.ID)
		if got, want := destinations(manifest), []string{
			"Original Folder/chapter.mp3",
			"Original Folder/cover.jpg",
		}; !slices.Equal(got, want) {
			t.Fatalf("destinations = %v, want %v", got, want)
		}
	})

	t.Run("split m4b", func(t *testing.T) {
		t.Parallel()
		fixture := newBookManifestFixture(t,
			"Author/Omnibus/Dune Messiah.m4b",
			"Author/Omnibus/Dune Messiah.cue",
			"Author/Omnibus/Children of Dune.m4b",
			"Author/Omnibus/cover.jpg",
			"Author/Omnibus/back.jpg",
			"Author/Omnibus/unrelated.nfo",
		)
		book := fixture.addBook("Author/Omnibus", "m4b-split", "Dune Messiah",
			"Author/Omnibus/Dune Messiah.m4b",
			"Author/Omnibus/cover.jpg",
		)
		manifest := fixture.build(t, book.ID)
		if got, want := destinations(manifest), []string{
			"Dune Messiah/Dune Messiah.cue",
			"Dune Messiah/Dune Messiah.m4b",
			"Dune Messiah/cover.jpg",
		}; !slices.Equal(got, want) {
			t.Fatalf("destinations = %v, want %v", got, want)
		}
		for _, file := range manifest.Files {
			if file.SourcePath == "" || filepath.IsAbs(file.SourcePath) ||
				file.FileID == "" || file.SHA256 == "" {
				t.Fatalf("unsafe manifest file = %#v", file)
			}
		}
	})

	t.Run("split m4b at library root", func(t *testing.T) {
		t.Parallel()
		fixture := newBookManifestFixture(t,
			"Root Book.m4b",
			"Other Root Book.m4b",
			"cover.jpg",
		)
		book := fixture.addBook("", "m4b-root-split", "Root Book",
			"Root Book.m4b",
		)
		manifest := fixture.build(t, book.ID)
		if got, want := destinations(manifest), []string{
			"Root Book/Root Book.m4b",
			"Root Book/cover.jpg",
		}; !slices.Equal(got, want) {
			t.Fatalf("destinations = %v, want %v", got, want)
		}
	})
}

func TestBookManifestSanitizesSplitDestinationAndFallsBackToTitle(t *testing.T) {
	t.Parallel()

	fixture := newBookManifestFixture(t,
		`Omnibus/Bad：Name?.m4b`,
		`Omnibus/Other.m4b`,
	)
	book := fixture.addBook("Omnibus", "m4b-split", `Fallback: Book?`,
		`Omnibus/Bad：Name?.m4b`,
	)
	manifest := fixture.build(t, book.ID)
	if len(manifest.Files) != 1 ||
		manifest.Files[0].DestinationPath != "Bad：Name_/Bad：Name_.m4b" {
		t.Fatalf("files = %#v", manifest.Files)
	}

	fallbackFixture := newBookManifestFixture(t,
		"Omnibus/... .m4b",
		"Omnibus/Other.m4b",
	)
	fallback := fallbackFixture.addBook("Omnibus", "m4b-split", `Fallback: Book?`,
		"Omnibus/... .m4b",
	)
	fallbackManifest := fallbackFixture.build(t, fallback.ID)
	if len(fallbackManifest.Files) != 1 ||
		fallbackManifest.Files[0].DestinationPath !=
			"Fallback_ Book_/Fallback_ Book_.m4b" {
		t.Fatalf("fallback files = %#v", fallbackManifest.Files)
	}

	if got := SanitizeAndroidName(" ... "); got != "Audiobook" {
		t.Fatalf("SanitizeAndroidName() = %q", got)
	}
	if got := SanitizeAndroidName(`A\B/C:D*E?F"G<H>I|J. `); got != "A_B_C_D_E_F_G_H_I_J" {
		t.Fatalf("SanitizeAndroidName() = %q", got)
	}
}

func TestBookManifestRejectsDestinationCollisionsAfterSanitization(t *testing.T) {
	t.Parallel()

	fixture := newBookManifestFixture(t,
		"Omnibus/A?.m4b",
		"Omnibus/A*.m4b",
		"Omnibus/Other.m4b",
	)
	book := fixture.addBook("Omnibus", "m4b-split", "A",
		"Omnibus/A?.m4b",
		"Omnibus/A*.m4b",
	)
	if _, err := fixture.builder.Build(t.Context(), Request{Selections: []Selection{{
		Kind: "book", BookID: book.ID.String(),
	}}}); !errors.Is(err, ErrDestinationCollision) {
		t.Fatalf("Build() error = %v, want ErrDestinationCollision", err)
	}
}

func TestBookManifestCopiesSharedCoverIntoEachSelectedSplitBook(t *testing.T) {
	t.Parallel()

	fixture := newBookManifestFixture(t,
		"Omnibus/Book One.m4b",
		"Omnibus/Book Two.m4b",
		"Omnibus/cover.jpg",
	)
	first := fixture.addBook("Omnibus", "m4b-first", "Book One",
		"Omnibus/Book One.m4b",
	)
	second := fixture.addBook("Omnibus", "m4b-second", "Book Two",
		"Omnibus/Book Two.m4b",
	)

	manifest, err := fixture.builder.Build(t.Context(), Request{Selections: []Selection{
		{Kind: "book", BookID: first.ID.String()},
		{Kind: "book", BookID: second.ID.String()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := destinations(manifest), []string{
		"Book One/Book One.m4b",
		"Book One/cover.jpg",
		"Book Two/Book Two.m4b",
		"Book Two/cover.jpg",
	}; !slices.Equal(got, want) {
		t.Fatalf("destinations = %v, want %v", got, want)
	}
}

func TestBookManifestRejectsMissingBooksAndCatalogPathMismatches(t *testing.T) {
	t.Parallel()

	fixture := newBookManifestFixture(t, "Book/book.m4b")
	missingID := uuid.New()
	if _, err := fixture.builder.Build(t.Context(), Request{Selections: []Selection{{
		Kind: "book", BookID: missingID.String(),
	}}}); err == nil {
		t.Fatal("missing book accepted")
	}

	book := fixture.addBook("Book", "folder-key", "Book", "Book/book.m4b")
	detail := fixture.repository.details[book.ID]
	detail.Files[0].RootID = "other"
	fixture.repository.details[book.ID] = detail
	if _, err := fixture.builder.Build(t.Context(), Request{Selections: []Selection{{
		Kind: "book", BookID: book.ID.String(),
	}}}); err == nil {
		t.Fatal("cross-root catalog file accepted")
	}
}

type bookManifestFixture struct {
	root       string
	builder    *CatalogBuilder
	repository *bookManifestRepository
}

func newBookManifestFixture(t *testing.T, paths ...string) bookManifestFixture {
	t.Helper()
	root := createTransferTree(t, paths...)
	resolver, err := library.NewResolver([]config.Root{{
		ID: "audiobooks", Name: "Audiobooks", ContainerPath: root,
	}})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewFileSigner(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repository := &bookManifestRepository{details: make(map[uuid.UUID]catalog.BookDetail)}
	return bookManifestFixture{
		root: root, repository: repository,
		builder: NewCatalogBuilder(resolver, signer, repository),
	}
}

func (fixture bookManifestFixture) addBook(
	folder string,
	groupingKey string,
	title string,
	paths ...string,
) catalog.Book {
	book := catalog.Book{
		ID: uuid.New(), RootID: "audiobooks", SourceFolder: folder,
		GroupingKey: groupingKey, Title: title,
	}
	files := make([]catalog.SourceFile, 0, len(paths))
	for _, relative := range paths {
		info, err := os.Stat(filepath.Join(fixture.root, filepath.FromSlash(relative)))
		if err != nil {
			panic(err)
		}
		files = append(files, catalog.SourceFile{
			ID: uuid.New(), RootID: "audiobooks", RelativePath: relative,
			Size: info.Size(), ModifiedAt: info.ModTime(),
		})
	}
	fixture.repository.details[book.ID] = catalog.BookDetail{Book: book, Files: files}
	return book
}

func (fixture bookManifestFixture) build(t *testing.T, bookID uuid.UUID) Manifest {
	t.Helper()
	manifest, err := fixture.builder.Build(t.Context(), Request{Selections: []Selection{{
		Kind: "book", BookID: bookID.String(),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

type bookManifestRepository struct {
	details map[uuid.UUID]catalog.BookDetail
}

func (repository *bookManifestRepository) GetBook(
	_ context.Context,
	id uuid.UUID,
) (catalog.BookDetail, error) {
	detail, ok := repository.details[id]
	if !ok {
		return catalog.BookDetail{}, catalog.ErrNotFound
	}
	return detail, nil
}

func destinations(manifest Manifest) []string {
	result := make([]string, len(manifest.Files))
	for index := range manifest.Files {
		result[index] = manifest.Files[index].DestinationPath
	}
	return result
}
