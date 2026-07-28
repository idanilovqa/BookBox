package catalog

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"bookbox.local/server/internal/database"
	"bookbox.local/server/internal/testpostgres"
)

func TestPostgresBookSearchFindsEveryCatalogField(t *testing.T) {
	fixture := newSearchFixture(t)

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "title", query: "Dune", want: "Dune"},
		{name: "misspelled title", query: "Neuromncer", want: "Neuromancer"},
		{name: "author", query: "William Gibson", want: "Neuromancer"},
		{name: "narrator", query: "Robertson Dean", want: "Neuromancer"},
		{name: "series", query: "Sprawl", want: "Neuromancer"},
		{name: "genre", query: "Cyberpunk", want: "Neuromancer"},
		{name: "year", query: "1984", want: "Neuromancer"},
		{name: "folder", query: "Gibson", want: "Neuromancer"},
		{name: "source filename", query: "cyberspace case", want: "Neuromancer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := fixture.repository.ListBooks(fixture.ctx, BookQuery{
				Search: test.query,
				Limit:  100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !pageContainsTitle(page, test.want) {
				t.Fatalf("search %q returned titles %v", test.query, pageTitles(page))
			}
		})
	}
}

func TestPostgresBookSearchFilters(t *testing.T) {
	fixture := newSearchFixture(t)

	tests := []struct {
		name  string
		query BookQuery
		want  []string
	}{
		{
			name:  "author is case insensitive",
			query: BookQuery{Author: "  frank herbert  "},
			want:  []string{"Dune", "Dune Messiah"},
		},
		{
			name:  "genre is case insensitive",
			query: BookQuery{Genre: "  science fiction  "},
			want:  []string{"Dune", "Dune Messiah"},
		},
		{
			name:  "publication year",
			query: BookQuery{PublicationYear: searchPointer(1937)},
			want:  []string{"The Hobbit"},
		},
		{
			name:  "root",
			query: BookQuery{RootID: "  archive  "},
			want:  []string{"Shared Work", "The Hobbit", "éCLAIR"},
		},
		{
			name:  "review state",
			query: BookQuery{ReviewState: "  " + ReviewNeedsReview + "  "},
			want:  []string{"Neuromancer"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.query.Limit = 100
			page, err := fixture.repository.ListBooks(fixture.ctx, test.query)
			if err != nil {
				t.Fatal(err)
			}
			got := pageTitles(page)
			slices.Sort(got)
			slices.Sort(test.want)
			if !slices.Equal(got, test.want) {
				t.Fatalf("titles = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPostgresBookSearchSortsAndPagesTiesWithoutDuplicates(t *testing.T) {
	fixture := newSearchFixture(t)

	sorts := []string{
		BookSortTitle,
		BookSortAuthor,
		BookSortYear,
		BookSortRecentlyAdded,
		BookSortRecentlyScanned,
	}
	for _, sortKey := range sorts {
		t.Run(sortKey, func(t *testing.T) {
			all, err := fixture.repository.ListBooks(fixture.ctx, BookQuery{
				Sort:  sortKey,
				Limit: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertBookSort(t, sortKey, all.Items)

			var paged []Book
			var cursor string
			for {
				page, err := fixture.repository.ListBooks(fixture.ctx, BookQuery{
					Sort:   sortKey,
					Limit:  2,
					Cursor: cursor,
				})
				if err != nil {
					t.Fatal(err)
				}
				paged = append(paged, page.Items...)
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
				if len(paged) > len(all.Items) {
					t.Fatal("cursor paging did not terminate")
				}
			}
			if len(paged) != len(all.Items) {
				t.Fatalf("paged count = %d, want %d", len(paged), len(all.Items))
			}
			seen := make(map[uuid.UUID]struct{}, len(paged))
			for _, book := range paged {
				if _, exists := seen[book.ID]; exists {
					t.Fatalf("duplicate book %s for sort %q", book.ID, sortKey)
				}
				seen[book.ID] = struct{}{}
			}
		})
	}
}

func TestPostgresBookSearchUsesScanTimeAndDatabaseSortValue(t *testing.T) {
	fixture := newSearchFixture(t)

	if _, err := fixture.pool.Exec(fixture.ctx, `
		update books
		set updated_at='2030-01-01T00:00:00Z'
		where title='Dune'
	`); err != nil {
		t.Fatal(err)
	}
	recentlyScanned, err := fixture.repository.ListBooks(fixture.ctx, BookQuery{
		Sort:  BookSortRecentlyScanned,
		Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recentlyScanned.Items) == 0 || recentlyScanned.Items[0].Title == "Dune" {
		t.Fatalf("metadata updated_at changed scan order: %v", pageTitles(recentlyScanned))
	}

	accented, err := fixture.repository.ListBooks(fixture.ctx, BookQuery{
		Search: "éclair",
		Sort:   BookSortTitle,
		Limit:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(accented.Items) != 1 || accented.NextCursor == "" {
		t.Fatalf("accented first page = %#v", accented)
	}
	cursor, err := decodeBookCursor(accented.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	var databaseValue string
	if err := fixture.pool.QueryRow(
		fixture.ctx,
		`select lower(title) from books where id=$1`,
		accented.Items[0].ID,
	).Scan(&databaseValue); err != nil {
		t.Fatal(err)
	}
	if cursor.Value != databaseValue {
		t.Fatalf("cursor value = %q, PostgreSQL value = %q", cursor.Value, databaseValue)
	}
	second, err := fixture.repository.ListBooks(fixture.ctx, BookQuery{
		Search: "éclair",
		Sort:   BookSortTitle,
		Limit:  1,
		Cursor: accented.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID == accented.Items[0].ID {
		t.Fatalf("accented second page = %#v after %#v", second, accented)
	}
}

func TestPostgresBookSearchTreatsHostileInputAsDataAndRejectsInvalidCursors(
	t *testing.T,
) {
	fixture := newSearchFixture(t)

	whitespace, err := fixture.repository.ListBooks(fixture.ctx, BookQuery{
		Search: " \t\r\n ",
		Limit:  100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(whitespace.Items) != len(fixture.books) {
		t.Fatalf("whitespace search returned %d books, want %d", len(whitespace.Items), len(fixture.books))
	}
	for name, query := range map[string]BookQuery{
		"search": {Search: strings.Repeat("a", 257)},
		"author": {Author: strings.Repeat("a", 257)},
		"genre":  {Genre: strings.Repeat("a", 257)},
		"root":   {RootID: strings.Repeat("a", 257)},
	} {
		t.Run("oversized "+name, func(t *testing.T) {
			query.Limit = 100
			if _, err := fixture.repository.ListBooks(fixture.ctx, query); !errors.Is(
				err,
				ErrInvalidData,
			) {
				t.Fatalf("error = %v, want ErrInvalidData", err)
			}
		})
	}

	for _, value := range []string{
		"%",
		"_",
		"'",
		`"`,
		`"unterminated`,
		`Dune' OR true --`,
	} {
		page, err := fixture.repository.ListBooks(fixture.ctx, BookQuery{
			Search: value,
			Limit:  100,
		})
		if err != nil {
			t.Fatalf("search %q: %v", value, err)
		}
		if value == `Dune' OR true --` && len(page.Items) == len(fixture.books) {
			t.Fatalf("hostile search returned every book: %v", pageTitles(page))
		}
	}
	for _, value := range []string{"%", "_", `' OR true --`} {
		if _, err := fixture.repository.ListBooks(fixture.ctx, BookQuery{
			Author: value,
			Genre:  value,
			RootID: value,
			Limit:  100,
		}); err != nil {
			t.Fatalf("filter %q: %v", value, err)
		}
	}

	titlePage, err := fixture.repository.ListBooks(fixture.ctx, BookQuery{
		Sort:  BookSortTitle,
		Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]BookQuery{
		"malformed base64": {
			Sort: BookSortTitle, Cursor: "not+a+base64url+cursor",
		},
		"unknown cursor field": {
			Sort: BookSortTitle,
			Cursor: base64.RawURLEncoding.EncodeToString([]byte(
				`{"version":1,"sort":"title","value":"dune","id":"00000000-0000-0000-0000-000000000001","extra":true}`,
			)),
		},
		"sort mismatch": {
			Sort: BookSortYear, Cursor: titlePage.NextCursor,
		},
		"invalid year value": {
			Sort: BookSortYear,
			Cursor: base64.RawURLEncoding.EncodeToString([]byte(
				`{"version":1,"sort":"year","value":"not-a-year","id":"00000000-0000-0000-0000-000000000001"}`,
			)),
		},
		"invalid timestamp value": {
			Sort: BookSortRecentlyAdded,
			Cursor: base64.RawURLEncoding.EncodeToString([]byte(
				`{"version":1,"sort":"recently_added","value":"not-a-time","id":"00000000-0000-0000-0000-000000000001"}`,
			)),
		},
		"unknown sort": {
			Sort: "title desc; drop table books",
		},
	} {
		t.Run(name, func(t *testing.T) {
			query.Limit = 10
			if _, err := fixture.repository.ListBooks(fixture.ctx, query); !errors.Is(
				err,
				ErrInvalidPage,
			) {
				t.Fatalf("error = %v, want ErrInvalidPage", err)
			}
		})
	}

	tamperedDataCursor := base64.RawURLEncoding.EncodeToString([]byte(
		`{"version":1,"sort":"title","value":"x') OR true --","id":"00000000-0000-0000-0000-000000000001"}`,
	))
	if _, err := fixture.repository.ListBooks(fixture.ctx, BookQuery{
		Sort:   BookSortTitle,
		Cursor: tamperedDataCursor,
		Limit:  10,
	}); err != nil {
		t.Fatalf("tampered cursor value was not treated as data: %v", err)
	}
}

type bookSearchFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	repository *PostgresRepository
	books      []Book
}

func newSearchFixture(t *testing.T) bookSearchFixture {
	t.Helper()
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into library_roots(id, display_name)
		values ('audiobooks','Audiobooks'), ('archive','Archive')
	`); err != nil {
		t.Fatal(err)
	}

	type fixtureBook struct {
		root, folder, filename, title, author, narrator, series, genre, review string
		year                                                                   int
		createdAt, scannedAt                                                   time.Time
	}
	tieCreated := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	tieScanned := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	definitions := []fixtureBook{
		{
			root: "audiobooks", folder: "Classics/Dune", filename: "Classics/Dune/rare-spice-track.m4b",
			title: "Dune", author: "Frank Herbert", narrator: "Simon Vance",
			series: "Chronicles of Dune", genre: "Science Fiction", year: 1965,
			review:    ReviewConfirmed,
			createdAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			scannedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			root: "audiobooks", folder: "Gibson/Neuromancer", filename: "Gibson/Neuromancer/cyberspace-case.mp3",
			title: "Neuromancer", author: "William Gibson", narrator: "Robertson Dean",
			series: "Sprawl", genre: "Cyberpunk", year: 1984,
			review:    ReviewNeedsReview,
			createdAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			scannedAt: time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC),
		},
		{
			root: "archive", folder: "Tolkien/The Hobbit", filename: "Tolkien/The Hobbit/chapter-one.flac",
			title: "The Hobbit", author: "J.R.R. Tolkien", narrator: "Andy Serkis",
			series: "Middle-earth", genre: "Fantasy", year: 1937,
			review:    ReviewUnreviewed,
			createdAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
			scannedAt: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC),
		},
		{
			root: "audiobooks", folder: "Classics/Dune Messiah", filename: "Classics/Dune Messiah/book.m4b",
			title: "Dune Messiah", author: "Frank Herbert", narrator: "Simon Vance",
			series: "Chronicles of Dune", genre: "Science Fiction", year: 1969,
			review:    ReviewUnreviewed,
			createdAt: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
			scannedAt: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			root: "audiobooks", folder: "Ties/Shared One", filename: "Ties/Shared One/part-one.mp3",
			title: "Shared Work", author: "Tie Author", narrator: "Tie Narrator",
			series: "Tie Series", genre: "Mystery", year: 2000,
			review: ReviewUnreviewed, createdAt: tieCreated, scannedAt: tieScanned,
		},
		{
			root: "archive", folder: "Ties/Shared Two", filename: "Ties/Shared Two/part-two.mp3",
			title: "Shared Work", author: "Tie Author", narrator: "Tie Narrator",
			series: "Tie Series", genre: "Mystery", year: 2000,
			review: ReviewUnreviewed, createdAt: tieCreated, scannedAt: tieScanned,
		},
		{
			root: "audiobooks", folder: "Accents/Eclair One", filename: "Accents/Eclair One/one.mp3",
			title: "Éclair", author: "Accent Author", narrator: "Accent Narrator",
			series: "Accent Series", genre: "Fiction", year: 2001,
			review: ReviewUnreviewed, createdAt: tieCreated, scannedAt: tieScanned,
		},
		{
			root: "archive", folder: "Accents/Eclair Two", filename: "Accents/Eclair Two/two.mp3",
			title: "éCLAIR", author: "Accent Author", narrator: "Accent Narrator",
			series: "Accent Series", genre: "Fiction", year: 2001,
			review: ReviewUnreviewed, createdAt: tieCreated, scannedAt: tieScanned,
		},
	}

	books := make([]Book, 0, len(definitions))
	for _, definition := range definitions {
		bookID := uuid.New()
		fileID := uuid.New()
		if _, err := pool.Exec(ctx, `
			insert into source_files(
				id, root_id, relative_path, size, modified_at, created_at, updated_at
			) values ($1,$2,$3,1024,$4,$4,$4)
		`, fileID, definition.root, definition.filename, definition.scannedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			insert into books(
				id, root_id, source_folder, grouping_key, title, authors,
				narrators, series, publication_year, genres, metadata_source,
				confidence, review_state, created_at, updated_at
			) values (
				$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'local',0.900,$11,$12,$13
			)
		`, bookID, definition.root, definition.folder, "fixture-"+bookID.String(),
			definition.title, []string{definition.author}, []string{definition.narrator},
			definition.series, definition.year, []string{definition.genre},
			definition.review, definition.createdAt, definition.scannedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			insert into book_files(book_id,file_id,root_id,role)
			values ($1,$2,$3,'audio')
		`, bookID, fileID, definition.root); err != nil {
			t.Fatal(err)
		}
	}
	repository := NewPostgresRepository(pool)
	page, err := repository.ListBooks(ctx, BookQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	books = append(books, page.Items...)
	return bookSearchFixture{
		ctx:        ctx,
		pool:       pool,
		repository: repository,
		books:      books,
	}
}

func assertBookSort(t *testing.T, sortKey string, books []Book) {
	t.Helper()
	for index := 1; index < len(books); index++ {
		previous := books[index-1]
		current := books[index]
		var correctlyOrdered bool
		switch sortKey {
		case BookSortTitle:
			correctlyOrdered = strings.ToLower(previous.Title) <= strings.ToLower(current.Title)
		case BookSortAuthor:
			correctlyOrdered = strings.ToLower(previous.Authors[0]) <=
				strings.ToLower(current.Authors[0])
		case BookSortYear:
			correctlyOrdered = *previous.PublicationYear >= *current.PublicationYear
		case BookSortRecentlyAdded:
			correctlyOrdered = !previous.CreatedAt.Before(current.CreatedAt)
		case BookSortRecentlyScanned:
			correctlyOrdered = !previous.UpdatedAt.Before(current.UpdatedAt)
		default:
			t.Fatalf("unknown sort %q", sortKey)
		}
		if !correctlyOrdered {
			t.Fatalf("%q order is incorrect at %q then %q", sortKey, previous.Title, current.Title)
		}
	}
}

func pageContainsTitle(page Page[Book], title string) bool {
	for _, book := range page.Items {
		if book.Title == title {
			return true
		}
	}
	return false
}

func pageTitles(page Page[Book]) []string {
	titles := make([]string, 0, len(page.Items))
	for _, book := range page.Items {
		titles = append(titles, book.Title)
	}
	return titles
}

func searchPointer[T any](value T) *T {
	return &value
}
