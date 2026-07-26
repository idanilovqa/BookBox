package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"netstorage.local/server/internal/catalog"
	"netstorage.local/server/internal/library"
)

type BookCardResponse struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Authors         []string `json:"authors"`
	PublicationYear *int     `json:"publicationYear,omitempty"`
	Genres          []string `json:"genres"`
	Series          string   `json:"series,omitempty"`
	Narrators       []string `json:"narrators"`
	CoverURL        string   `json:"coverUrl,omitempty"`
	ReviewState     string   `json:"reviewState"`
	SourceFolder    string   `json:"sourceFolder"`
}

type BooksResponse struct {
	Items      []BookCardResponse     `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
	Filters    catalog.FilterSnapshot `json:"filters"`
}

type MetadataAttributionResponse struct {
	Provider    string `json:"provider"`
	DisplayName string `json:"displayName,omitempty"`
	SourceURL   string `json:"sourceUrl,omitempty"`
}

type BookDetailResponse struct {
	ID               string                       `json:"id"`
	RootID           string                       `json:"rootId"`
	Title            string                       `json:"title"`
	Authors          []string                     `json:"authors"`
	ReviewState      string                       `json:"reviewState"`
	SourceFolder     string                       `json:"sourceFolder"`
	Subtitle         string                       `json:"subtitle,omitempty"`
	Narrators        []string                     `json:"narrators"`
	Series           string                       `json:"series,omitempty"`
	SeriesPosition   string                       `json:"seriesPosition,omitempty"`
	Publisher        string                       `json:"publisher,omitempty"`
	PublishedDate    string                       `json:"publishedDate,omitempty"`
	PublicationYear  *int                         `json:"publicationYear,omitempty"`
	Genres           []string                     `json:"genres"`
	ProviderSubjects []string                     `json:"providerSubjects"`
	Identifiers      map[string]string            `json:"identifiers"`
	CoverURL         string                       `json:"coverUrl,omitempty"`
	MetadataSource   string                       `json:"metadataSource,omitempty"`
	Confidence       float64                      `json:"confidence,omitempty"`
	Attribution      *MetadataAttributionResponse `json:"attribution,omitempty"`
}

type BookFileResponse struct {
	ID           string    `json:"id"`
	RelativePath string    `json:"relativePath"`
	Name         string    `json:"name"`
	Size         int64     `json:"size"`
	ModifiedAt   time.Time `json:"modifiedAt"`
}

type BookFilesResponse struct {
	BookID       string             `json:"bookId"`
	RootID       string             `json:"rootId"`
	SourceFolder string             `json:"sourceFolder"`
	Files        []BookFileResponse `json:"files"`
}

func booksHandler(repository CatalogRepository) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if repository == nil {
			writeInternalError(response)
			return
		}
		query, err := parseBookQuery(request)
		if err != nil {
			writeCatalogError(response, err)
			return
		}
		page, err := repository.ListBooks(request.Context(), query)
		if err != nil {
			writeCatalogError(response, err)
			return
		}
		filters, err := repository.ListCatalogFilters(request.Context())
		if err != nil {
			writeCatalogError(response, err)
			return
		}
		result := BooksResponse{
			Items:      make([]BookCardResponse, 0, len(page.Items)),
			NextCursor: page.NextCursor,
			Filters:    filters,
		}
		for _, book := range page.Items {
			if !publicBook(book) {
				writeInternalError(response)
				return
			}
			result.Items = append(result.Items, bookCard(book))
		}
		writeJSON(response, http.StatusOK, result)
	})
}

func parseBookQuery(request *http.Request) (catalog.BookQuery, error) {
	values := request.URL.Query()
	query := catalog.BookQuery{
		Search: values.Get("q"), RootID: values.Get("rootId"),
		Author: values.Get("author"), Genre: values.Get("genre"),
		ReviewState: values.Get("reviewState"), Sort: values.Get("sort"),
		Cursor: values.Get("cursor"),
	}
	if yearText := values.Get("year"); yearText != "" {
		year, err := strconv.Atoi(yearText)
		if err != nil {
			return catalog.BookQuery{}, catalog.ErrInvalidData
		}
		query.PublicationYear = &year
	}
	if limitText := values.Get("limit"); limitText != "" {
		limit, err := strconv.Atoi(limitText)
		if err != nil {
			return catalog.BookQuery{}, catalog.ErrInvalidPage
		}
		query.Limit = limit
	}
	return query, nil
}

func bookDetailHandler(repository CatalogRepository) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		detail, ok := loadBook(response, request, repository)
		if !ok {
			return
		}
		writeJSON(response, http.StatusOK, bookDetail(detail))
	})
}

func bookFilesHandler(repository CatalogRepository) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		detail, ok := loadBook(response, request, repository)
		if !ok {
			return
		}
		result := BookFilesResponse{
			BookID: detail.Book.ID.String(), RootID: detail.Book.RootID,
			SourceFolder: detail.Book.SourceFolder,
			Files:        make([]BookFileResponse, 0, len(detail.Files)),
		}
		for _, file := range detail.Files {
			if file.MissingAt != nil {
				continue
			}
			if file.ID == uuid.Nil || file.RootID != detail.Book.RootID ||
				catalog.ValidateRelativePath(file.RelativePath) != nil ||
				!inSourceFolder(file.RelativePath, detail.Book.SourceFolder) {
				writeInternalError(response)
				return
			}
			result.Files = append(result.Files, BookFileResponse{
				ID: file.ID.String(), RelativePath: file.RelativePath,
				Name: path.Base(file.RelativePath), Size: file.Size,
				ModifiedAt: file.ModifiedAt.UTC(),
			})
		}
		writeJSON(response, http.StatusOK, result)
	})
}

func bookCoverHandler(repository CatalogRepository, resolver *library.Resolver) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if resolver == nil {
			writeInternalError(response)
			return
		}
		detail, ok := loadBook(response, request, repository)
		if !ok {
			return
		}
		coverRelative, err := currentBookCover(resolver, detail.Book)
		if err != nil {
			writeCatalogError(response, catalog.ErrNotFound)
			return
		}
		handle, resolved, err := resolver.OpenRegular(detail.Book.RootID, coverRelative)
		if err != nil || resolved.Relative != coverRelative {
			writeCatalogError(response, catalog.ErrNotFound)
			return
		}
		defer handle.Close()
		info, err := handle.Stat()
		if err != nil || !info.Mode().IsRegular() {
			writeCatalogError(response, catalog.ErrNotFound)
			return
		}
		response.Header().Set(
			"ETag",
			fmt.Sprintf(`W/"%x-%x"`, info.Size(), info.ModTime().UnixNano()),
		)
		response.Header().Set("Cache-Control", "private, max-age=3600, must-revalidate")
		http.ServeContent(response, request, path.Base(coverRelative), info.ModTime(), handle)
	})
}

func currentBookCover(
	resolver *library.Resolver,
	book catalog.Book,
) (string, error) {
	folder, err := resolver.Resolve(book.RootID, book.SourceFolder)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(folder.Absolute)
	if err != nil {
		return "", err
	}
	for _, base := range []string{"cover", "folder", "front"} {
		for _, extension := range []string{".jpg", ".jpeg", ".png", ".webp"} {
			for _, entry := range entries {
				if !entry.IsDir() &&
					strings.EqualFold(entry.Name(), base+extension) {
					return path.Join(book.SourceFolder, entry.Name()), nil
				}
			}
		}
	}
	return "", os.ErrNotExist
}

func loadBook(
	response http.ResponseWriter,
	request *http.Request,
	repository CatalogRepository,
) (catalog.BookDetail, bool) {
	if repository == nil {
		writeInternalError(response)
		return catalog.BookDetail{}, false
	}
	bookID, err := uuid.Parse(request.PathValue("bookId"))
	if err != nil || bookID == uuid.Nil {
		writeStableError(response, http.StatusBadRequest, "invalid_book_id", "book identifier is invalid")
		return catalog.BookDetail{}, false
	}
	detail, err := repository.GetBook(request.Context(), bookID)
	if err != nil {
		writeCatalogError(response, err)
		return catalog.BookDetail{}, false
	}
	if detail.Book.ID != bookID || !publicBook(detail.Book) {
		writeInternalError(response)
		return catalog.BookDetail{}, false
	}
	return detail, true
}

func publicBook(book catalog.Book) bool {
	return book.ID != uuid.Nil && book.MissingAt == nil &&
		book.RootID != "" && catalog.ValidateSourceFolder(book.SourceFolder) == nil
}

func bookCard(book catalog.Book) BookCardResponse {
	return BookCardResponse{
		ID: book.ID.String(), Title: book.Title, Authors: nonNil(book.Authors),
		PublicationYear: book.PublicationYear, Genres: nonNil(book.Genres),
		Series: book.Series, Narrators: nonNil(book.Narrators),
		CoverURL:    "/v1/books/" + book.ID.String() + "/cover",
		ReviewState: book.ReviewState, SourceFolder: book.SourceFolder,
	}
}

func bookDetail(detail catalog.BookDetail) BookDetailResponse {
	book := detail.Book
	return BookDetailResponse{
		ID: book.ID.String(), RootID: book.RootID, Title: book.Title,
		Authors: nonNil(book.Authors), ReviewState: book.ReviewState,
		SourceFolder: book.SourceFolder, Subtitle: book.Subtitle,
		Narrators: nonNil(book.Narrators), Series: book.Series,
		SeriesPosition: book.SeriesPosition, Publisher: book.Publisher,
		PublishedDate: book.PublishedDate, PublicationYear: book.PublicationYear,
		Genres: nonNil(book.Genres), ProviderSubjects: nonNil(book.ProviderSubjects),
		Identifiers:    stringifyIdentifiers(book.Identifiers),
		CoverURL:       "/v1/books/" + book.ID.String() + "/cover",
		MetadataSource: book.MetadataSource, Confidence: book.Confidence,
		Attribution: metadataAttribution(detail),
	}
}

func metadataAttribution(detail catalog.BookDetail) *MetadataAttributionResponse {
	source := detail.Book.MetadataSource
	if detail.Book.ConfirmedMatchID != nil {
		for _, match := range detail.Matches {
			if match.ID == *detail.Book.ConfirmedMatchID && match.Confirmed {
				source = match.Provider
				break
			}
		}
	}
	if source == "" {
		return nil
	}
	display := map[string]string{
		catalog.MetadataOpenLibrary: "Open Library",
		catalog.MetadataGoogleBooks: "Google Books",
		catalog.MetadataLocal:       "Local audio metadata",
		catalog.MetadataInferred:    "Folder and filename",
		catalog.MetadataConfirmed:   "Confirmed metadata",
		catalog.MetadataMixed:       "Combined metadata",
	}[source]
	return &MetadataAttributionResponse{Provider: source, DisplayName: display}
}

func stringifyIdentifiers(values map[string]any) map[string]string {
	result := make(map[string]string)
	for key, value := range values {
		if key == "" {
			continue
		}
		switch typed := value.(type) {
		case string:
			result[key] = typed
		case fmt.Stringer:
			result[key] = typed.String()
		case float64:
			result[key] = strconv.FormatFloat(typed, 'f', -1, 64)
		case int:
			result[key] = strconv.Itoa(typed)
		}
	}
	return result
}

func inSourceFolder(relative, folder string) bool {
	if folder == "" {
		return path.Dir(relative) == "."
	}
	return path.Dir(relative) == folder
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func writeCatalogError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, catalog.ErrNotFound):
		writeStableError(response, http.StatusNotFound, "book_not_found", "book not found")
	case errors.Is(err, catalog.ErrConflict):
		writeStableError(response, http.StatusConflict, "catalog_conflict", "catalog state changed")
	case errors.Is(err, catalog.ErrInvalidData), errors.Is(err, catalog.ErrInvalidPage),
		errors.Is(err, catalog.ErrInvalidPath):
		writeInvalidRequest(response)
	default:
		writeInternalError(response)
	}
}

func writeStableError(
	response http.ResponseWriter,
	status int,
	code string,
	message string,
) {
	writeJSON(response, status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}
