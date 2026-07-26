package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	bookCursorVersion = 1
	maxBookQueryText  = 256
)

const (
	cursorText = iota
	cursorYear
	cursorTime
)

type bookSortSpec struct {
	valueSQL   string
	direction  string
	cursorKind int
}

var bookSortSpecs = map[string]bookSortSpec{
	BookSortTitle: {
		valueSQL:   "lower(b.title)",
		direction:  "asc",
		cursorKind: cursorText,
	},
	BookSortAuthor: {
		valueSQL:   "lower(coalesce(b.authors[1],''))",
		direction:  "asc",
		cursorKind: cursorText,
	},
	BookSortYear: {
		valueSQL:   "coalesce(b.publication_year,-2147483648)",
		direction:  "desc",
		cursorKind: cursorYear,
	},
	BookSortRecentlyAdded: {
		valueSQL:   "b.created_at",
		direction:  "desc",
		cursorKind: cursorTime,
	},
	BookSortRecentlyScanned: {
		valueSQL: `coalesce((
			select max(sf.updated_at)
			from book_files bf
			join source_files sf
			  on sf.id=bf.file_id and sf.root_id=bf.root_id
			where bf.book_id=b.id and sf.missing_at is null
		),'epoch'::timestamptz)`,
		direction:  "desc",
		cursorKind: cursorTime,
	},
}

func (repository *PostgresRepository) ListBooks(
	ctx context.Context,
	query BookQuery,
) (Page[Book], error) {
	if err := prepareBookQuery(&query); err != nil {
		return Page[Book]{}, err
	}
	limit := query.Limit
	if limit == 0 {
		limit = defaultPageSize
	}
	if limit < 1 || limit > maxPageSize {
		return Page[Book]{}, ErrInvalidPage
	}
	if query.Sort == "" {
		query.Sort = BookSortTitle
	}
	sortSpec, exists := bookSortSpecs[query.Sort]
	if !exists {
		return Page[Book]{}, ErrInvalidPage
	}
	if query.PublicationYear != nil &&
		(*query.PublicationYear < 1000 || *query.PublicationYear > 3000) {
		return Page[Book]{}, ErrInvalidData
	}

	arguments := make([]any, 0, 12)
	conditions := []string{"b.missing_at is null"}
	addArgument := func(value any) string {
		arguments = append(arguments, value)
		return fmt.Sprintf("$%d", len(arguments))
	}
	if query.RootID != "" {
		conditions = append(conditions, "b.root_id="+addArgument(query.RootID))
	}
	if query.Author != "" {
		placeholder := addArgument(query.Author)
		conditions = append(conditions,
			"exists(select 1 from unnest(b.authors) author where lower(author)=lower("+placeholder+"))")
	}
	if query.Genre != "" {
		placeholder := addArgument(query.Genre)
		conditions = append(conditions,
			"exists(select 1 from unnest(b.genres) genre where lower(genre)=lower("+placeholder+"))")
	}
	if query.ReviewState != "" {
		if !reviewStates[query.ReviewState] {
			return Page[Book]{}, ErrInvalidData
		}
		conditions = append(conditions, "b.review_state="+addArgument(query.ReviewState))
	}
	if query.PublicationYear != nil {
		conditions = append(conditions, "b.publication_year="+addArgument(*query.PublicationYear))
	}
	if query.Search != "" {
		placeholder := addArgument(query.Search)
		conditions = append(conditions, `(
			catalog_book_search_document(
				b.title,
				b.authors,
				b.series,
				b.narrators,
				b.genres,
				b.source_folder,
				b.publication_year
			) @@ websearch_to_tsquery('simple',`+placeholder+`)
			or similarity(lower(b.title),lower(`+placeholder+`)) >= 0.3
			or exists(
				select 1
				from book_files bf
				join source_files sf
				  on sf.id=bf.file_id and sf.root_id=bf.root_id
				where bf.book_id=b.id
				  and sf.missing_at is null
				  and to_tsvector(
				      'simple',
				      regexp_replace(sf.relative_path,'[^[:alnum:]]+',' ','g')
				  )
				      @@ websearch_to_tsquery('simple',`+placeholder+`)
			)
		)`)
	}

	cursor, err := decodeBookCursor(query.Cursor)
	if err != nil {
		return Page[Book]{}, err
	}
	if cursor != nil {
		if cursor.Sort != query.Sort {
			return Page[Book]{}, ErrInvalidPage
		}
		cursorValue, err := parseBookCursorValue(cursor.Value, sortSpec.cursorKind)
		if err != nil {
			return Page[Book]{}, err
		}
		valuePlaceholder := addArgument(cursorValue)
		idPlaceholder := addArgument(cursor.ID)
		operator := ">"
		if sortSpec.direction == "desc" {
			operator = "<"
		}
		conditions = append(conditions,
			"("+sortSpec.valueSQL+",b.id) "+operator+
				" ("+valuePlaceholder+","+idPlaceholder+")")
	}

	limitPlaceholder := addArgument(limit + 1)
	rows, err := repository.pool.Query(ctx, `
		select `+bookColumns("b")+`, `+sortSpec.valueSQL+`
		from books b
		where `+strings.Join(conditions, " and ")+`
		order by `+sortSpec.valueSQL+` `+sortSpec.direction+
		`, b.id `+sortSpec.direction+`
		limit `+limitPlaceholder, arguments...)
	if err != nil {
		return Page[Book]{}, fmt.Errorf("list books: %w", err)
	}
	defer rows.Close()

	items := make([]Book, 0, limit+1)
	cursorValues := make([]string, 0, limit+1)
	for rows.Next() {
		book, cursorValue, err := scanBookSearchRow(rows, sortSpec.cursorKind)
		if err != nil {
			return Page[Book]{}, err
		}
		items = append(items, book)
		cursorValues = append(cursorValues, cursorValue)
	}
	if err := rows.Err(); err != nil {
		return Page[Book]{}, fmt.Errorf("iterate books: %w", err)
	}

	page := Page[Book]{Items: items}
	if len(items) <= limit {
		return page, nil
	}
	page.Items = items[:limit]
	last := page.Items[len(page.Items)-1]
	page.NextCursor, err = encodeBookCursor(bookCursor{
		Version: bookCursorVersion,
		Sort:    query.Sort,
		Value:   cursorValues[limit-1],
		ID:      last.ID,
	})
	if err != nil {
		return Page[Book]{}, err
	}
	return page, nil
}

func prepareBookQuery(query *BookQuery) error {
	query.Search = strings.TrimSpace(query.Search)
	query.RootID = strings.TrimSpace(query.RootID)
	query.Author = strings.TrimSpace(query.Author)
	query.Genre = strings.TrimSpace(query.Genre)
	query.ReviewState = strings.TrimSpace(query.ReviewState)
	for _, value := range []string{
		query.Search,
		query.RootID,
		query.Author,
		query.Genre,
		query.ReviewState,
	} {
		if len(value) > maxBookQueryText {
			return ErrInvalidData
		}
	}
	return nil
}

type bookCursor struct {
	Version int       `json:"version"`
	Sort    string    `json:"sort"`
	Value   string    `json:"value"`
	ID      uuid.UUID `json:"id"`
}

func encodeBookCursor(cursor bookCursor) (string, error) {
	if cursor.Version != bookCursorVersion || cursor.ID == uuid.Nil {
		return "", ErrInvalidPage
	}
	if _, exists := bookSortSpecs[cursor.Sort]; !exists {
		return "", ErrInvalidPage
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode book cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeBookCursor(encoded string) (*bookCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) > 1024 {
		return nil, ErrInvalidPage
	}
	var cursor bookCursor
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil ||
		cursor.Version != bookCursorVersion || cursor.ID == uuid.Nil {
		return nil, ErrInvalidPage
	}
	if _, exists := bookSortSpecs[cursor.Sort]; !exists {
		return nil, ErrInvalidPage
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidPage
	}
	return &cursor, nil
}

func parseBookCursorValue(value string, kind int) (any, error) {
	switch kind {
	case cursorText:
		return value, nil
	case cursorYear:
		year, err := strconv.Atoi(value)
		if err != nil {
			return nil, ErrInvalidPage
		}
		return year, nil
	case cursorTime:
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil, ErrInvalidPage
		}
		return parsed, nil
	default:
		return nil, ErrInvalidPage
	}
}

func scanBookSearchRow(
	row scanner,
	cursorKind int,
) (Book, string, error) {
	switch cursorKind {
	case cursorText:
		var value string
		book, err := scanBookWithExtras(row, &value)
		if err != nil {
			return Book{}, "", err
		}
		return book, value, nil
	case cursorYear:
		var value int
		book, err := scanBookWithExtras(row, &value)
		if err != nil {
			return Book{}, "", err
		}
		return book, strconv.Itoa(value), nil
	case cursorTime:
		var value time.Time
		book, err := scanBookWithExtras(row, &value)
		if err != nil {
			return Book{}, "", err
		}
		return book, value.UTC().Format(time.RFC3339Nano), nil
	default:
		return Book{}, "", ErrInvalidPage
	}
}
