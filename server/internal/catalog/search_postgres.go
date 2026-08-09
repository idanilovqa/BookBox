package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const searchCursorVersion = 3

type searchCursor struct {
	Version    int              `json:"v"`
	Scope      string           `json:"q"`
	Snapshot   string           `json:"s"`
	Root       int              `json:"r"`
	DocumentID uuid.UUID        `json:"d"`
	GroupID    uuid.UUID        `json:"g"`
	Kind       SearchEntityKind `json:"k"`

	Rank         int     `json:"-"`
	Score        float64 `json:"-"`
	SortValue    string  `json:"-"`
	Name         string  `json:"-"`
	RelativePath string  `json:"-"`
	RootID       string  `json:"-"`
}

func (repository *PostgresRepository) SearchLibrary(
	ctx context.Context,
	query SearchQuery,
) (SearchPage, error) {
	if query.Sort == "" {
		query.Sort = BookSortTitle
	}
	rootIDs, normalizedText, err := prepareSearchQuery(query)
	if err != nil {
		return SearchPage{}, err
	}
	if repository == nil || repository.pool == nil {
		return SearchPage{}, ErrInvalidData
	}
	cursor, err := decodeSearchCursor(query.Cursor)
	if err != nil {
		return SearchPage{}, err
	}
	if cursor != nil && !searchCursorMatchesQuery(*cursor, query, rootIDs, normalizedText) {
		return SearchPage{}, ErrInvalidPage
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return SearchPage{}, fmt.Errorf("begin library search: %w", err)
	}
	defer transaction.Rollback(ctx)

	var configuredCount int
	if err := transaction.QueryRow(ctx, `
		select count(*) from library_roots where id=any($1::text[])
	`, rootIDs).Scan(&configuredCount); err != nil {
		return SearchPage{}, fmt.Errorf("validate search roots: %w", err)
	}
	if configuredCount != len(rootIDs) {
		return SearchPage{}, ErrInvalidData
	}
	page := SearchPage{}
	snapshotRows, err := transaction.Query(ctx, `
		select root_id, scan_run_id, completed_at, document_count, catalog_generation
		from library_search_snapshots
		where root_id=any($1::text[])
		order by root_id
	`, rootIDs)
	if err != nil {
		return SearchPage{}, fmt.Errorf("load search snapshots: %w", err)
	}
	for snapshotRows.Next() {
		var snapshot SearchSnapshot
		if err := snapshotRows.Scan(
			&snapshot.RootID, &snapshot.ScanRunID, &snapshot.CompletedAt,
			&snapshot.DocumentCount, &snapshot.CatalogGeneration,
		); err != nil {
			snapshotRows.Close()
			return SearchPage{}, fmt.Errorf("scan search snapshot: %w", err)
		}
		page.Snapshots = append(page.Snapshots, snapshot)
	}
	if err := snapshotRows.Err(); err != nil {
		snapshotRows.Close()
		return SearchPage{}, fmt.Errorf("iterate search snapshots: %w", err)
	}
	snapshotRows.Close()
	if len(page.Snapshots) != len(rootIDs) {
		return SearchPage{}, ErrSearchIndexBuilding
	}
	if cursor != nil && !searchCursorMatchesSnapshots(*cursor, page.Snapshots) {
		return SearchPage{}, ErrInvalidPage
	}
	if cursor != nil {
		if err := resolveSearchCursorTx(ctx, transaction, query, rootIDs, normalizedText, cursor); err != nil {
			return SearchPage{}, err
		}
		if !validSearchCursorPosition(*cursor, query, rootIDs, normalizedText) {
			return SearchPage{}, ErrInvalidPage
		}
	}
	if query.Mode == SearchModeAudiobooks {
		page.Groups, page.NextCursor, err = searchAudiobookGroupsTx(
			ctx, transaction, query, rootIDs, normalizedText, page.Snapshots, cursor,
		)
		if err != nil {
			return SearchPage{}, err
		}
		if err := transaction.Commit(ctx); err != nil {
			return SearchPage{}, fmt.Errorf("commit library search: %w", err)
		}
		return page, nil
	}
	page.Groups, page.NextCursor, err = searchRawGroupsTx(
		ctx, transaction, query, rootIDs, normalizedText, page.Snapshots, cursor,
	)
	if err != nil {
		return SearchPage{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return SearchPage{}, fmt.Errorf("commit library search: %w", err)
	}
	return page, nil
}

func prepareSearchQuery(query SearchQuery) ([]string, string, error) {
	if query.Mode != SearchModeAudiobooks && query.Mode != SearchModeRaw {
		return nil, "", ErrInvalidData
	}
	if len(query.RootIDs) == 0 || query.Limit < 1 || query.Limit > maxPageSize ||
		len(query.Text) > maxBookQueryText || len(query.Author) > maxBookQueryText ||
		len(query.Genre) > maxBookQueryText || len(query.ReviewState) > maxBookQueryText {
		return nil, "", ErrInvalidData
	}
	rootSet := make(map[string]struct{}, len(query.RootIDs))
	for _, rootID := range query.RootIDs {
		rootID = strings.TrimSpace(rootID)
		if !rootIDPattern.MatchString(rootID) {
			return nil, "", ErrInvalidData
		}
		rootSet[rootID] = struct{}{}
	}
	rootIDs := sortedStringKeys(rootSet)
	if query.Mode == SearchModeRaw && len(rootIDs) != 1 {
		return nil, "", ErrInvalidData
	}
	if query.Mode == SearchModeRaw && (query.Author != "" || query.Genre != "" ||
		query.ReviewState != "" || query.PublicationYear != nil ||
		(query.Sort != "" && query.Sort != BookSortTitle)) {
		return nil, "", ErrInvalidData
	}
	if _, exists := searchDocumentSorts[query.Sort]; !exists {
		return nil, "", ErrInvalidData
	}
	if query.ReviewState != "" && !reviewStates[strings.TrimSpace(query.ReviewState)] {
		return nil, "", ErrInvalidData
	}
	if strings.TrimSpace(query.Author) != "" && searchCompact(query.Author) == "" {
		return nil, "", ErrInvalidData
	}
	if strings.TrimSpace(query.Genre) != "" && searchCompact(query.Genre) == "" {
		return nil, "", ErrInvalidData
	}
	if query.PublicationYear != nil &&
		(*query.PublicationYear < 1000 || *query.PublicationYear > 3000) {
		return nil, "", ErrInvalidData
	}
	normalizedText := searchCompact(strings.TrimSpace(query.Text))
	length := utf8.RuneCountInString(normalizedText)
	if length == 1 || (query.Mode == SearchModeRaw && length == 0) {
		return nil, "", ErrInvalidData
	}
	return rootIDs, normalizedText, nil
}

type searchDocumentSort struct {
	expression string
	direction  string
	cursorKind int
}

var searchDocumentSorts = map[string]searchDocumentSort{
	BookSortTitle:           {expression: "lower(title)", direction: "asc", cursorKind: cursorText},
	BookSortAuthor:          {expression: "lower(coalesce(authors[1],''))", direction: "asc", cursorKind: cursorText},
	BookSortYear:            {expression: "coalesce(publication_year,-2147483648)", direction: "desc", cursorKind: cursorYear},
	BookSortRecentlyAdded:   {expression: "coalesce(created_at,'epoch'::timestamptz)", direction: "desc", cursorKind: cursorTime},
	BookSortRecentlyScanned: {expression: "coalesce(recently_scanned_at,'epoch'::timestamptz)", direction: "desc", cursorKind: cursorTime},
}

func fuzzySearchGrams(normalizedText string) []string {
	runes := []rune(normalizedText)
	if len(runes) < 6 {
		return searchGrams(normalizedText)
	}
	seen := make(map[string]struct{}, len(runes)-2)
	grams := make([]string, 0, len(runes)-2)
	for position := 0; position+3 <= len(runes); position++ {
		gram := string(runes[position : position+3])
		if _, exists := seen[gram]; !exists {
			seen[gram] = struct{}{}
			grams = append(grams, gram)
		}
	}
	return grams
}

func fuzzyGramCondition(grams []string, addArgument func(any) string) string {
	conditions := make([]string, 0, len(grams))
	for _, gram := range grams {
		conditions = append(conditions,
			"search_grams @> array["+addArgument(gram)+"]::text[]")
	}
	return "(" + strings.Join(conditions, " or ") + ")"
}

type searchGroupReference struct {
	rootID  string
	groupID uuid.UUID
	count   int
	leader  searchCursor
}

type rawSearchStatement struct {
	SQL       string
	Arguments []any
}

func buildRawSearchStatement(
	query SearchQuery,
	rootIDs []string,
	normalizedText string,
	cursor *searchCursor,
) rawSearchStatement {
	arguments := []any{rootIDs[0], normalizedText}
	addArgument := func(value any) string {
		arguments = append(arguments, value)
		return fmt.Sprintf("$%d", len(arguments))
	}
	textPlaceholder := "$2"
	textCondition := "search_grams @> array[$2]::text[]"
	if utf8.RuneCountInString(normalizedText) >= 3 {
		textCondition = `(` + fuzzyGramCondition(fuzzySearchGrams(normalizedText), addArgument) + `
			and (strpos(search_compact,$2)>0 or word_similarity($2,search_compact)>=0.3))`
	}
	rankExpression := `case
		when compact_name=` + textPlaceholder + ` or compact_path=` + textPlaceholder + ` then 0
		when left(compact_name,length(` + textPlaceholder + `))=` + textPlaceholder + `
		  or left(compact_path,length(` + textPlaceholder + `))=` + textPlaceholder + ` then 1
		when strpos(compact_name,` + textPlaceholder + `)>0
		  or strpos(compact_path,` + textPlaceholder + `)>0
		  or exists(select 1 from unnest(filenames) f where strpos(catalog_search_compact(f),` + textPlaceholder + `)>0) then 2
		when strpos(compact_metadata,` + textPlaceholder + `)>0 then 3
		else 4 end`
	cursorCondition := "true"
	if cursor != nil {
		cursorCondition = lexicographicSearchAfter([]searchKeyPart{
			{expression: "rank_value", operator: ">", placeholder: addArgument(cursor.Rank)},
			{expression: "score_value", operator: "<", placeholder: addArgument(cursor.Score)},
			{expression: "sort_value", operator: ">", placeholder: addArgument(cursor.SortValue)},
			{expression: "relative_path", operator: ">", placeholder: addArgument(cursor.RelativePath)},
			{expression: "entity_kind", operator: ">", placeholder: addArgument(cursor.Kind)},
			{expression: "root_id", operator: ">", placeholder: addArgument(cursor.RootID)},
			{expression: "document_id", operator: ">", placeholder: addArgument(cursor.DocumentID)},
		})
	}
	limitPlaceholder := addArgument(query.Limit + 1)
	return rawSearchStatement{SQL: `
		with ranked as (
		  select root_id, document_id, entity_kind, book_id, relative_path, display_name,
		         size, modified_at, title, authors, compact_authors, series, narrators,
		         genres, compact_genres, coalesce(review_state,'') as review_state,
		         coalesce(review_reason,'') as review_reason,
		         publication_year, created_at, recently_scanned_at, filenames, duration_ms,
		         duplicate_group_id, duplicate_count, compact_name, compact_path,
		         compact_metadata, search_compact, search_terms, search_grams,
		         ` + rankExpression + ` as rank_value,
		         word_similarity($2,search_compact)::float8 as score_value,
		         compact_name as sort_value
		  from library_search_documents
		  where root_id=$1 and entity_kind in ('folder','file') and ` + textCondition + `
		)
		select root_id, document_id, entity_kind, book_id, relative_path, display_name,
		       size, modified_at, title, authors, compact_authors, series, narrators,
		       genres, compact_genres, review_state, review_reason, publication_year, created_at,
		       recently_scanned_at, filenames, duration_ms, duplicate_group_id,
		       duplicate_count, compact_name, compact_path, compact_metadata,
		       search_compact, search_terms, search_grams,
		       rank_value, score_value, sort_value, document_id
		from ranked
		where ` + cursorCondition + `
		order by rank_value, score_value desc, sort_value, relative_path,
		         entity_kind, root_id, document_id
		limit ` + limitPlaceholder,
		Arguments: arguments,
	}
}

func searchRawGroupsTx(
	ctx context.Context,
	transaction pgx.Tx,
	query SearchQuery,
	rootIDs []string,
	normalizedText string,
	snapshots []SearchSnapshot,
	cursor *searchCursor,
) ([]SearchGroup, string, error) {
	statement := buildRawSearchStatement(query, rootIDs, normalizedText, cursor)
	rows, err := transaction.Query(ctx, statement.SQL, statement.Arguments...)
	if err != nil {
		return nil, "", fmt.Errorf("query raw search documents: %w", err)
	}
	defer rows.Close()
	groups := make([]SearchGroup, 0, query.Limit)
	var last searchCursor
	hasMore := false
	for rows.Next() {
		var rank int
		var score float64
		var sortValue string
		var groupID uuid.UUID
		document, scanErr := scanSearchDocumentWithExtras(rows, &rank, &score, &sortValue, &groupID)
		if scanErr != nil {
			return nil, "", scanErr
		}
		if len(groups) == query.Limit {
			hasMore = true
			break
		}
		if groupID != document.ID {
			return nil, "", fmt.Errorf("%w: raw group is not singleton", ErrInvalidData)
		}
		groups = append(groups, SearchGroup{ID: document.ID, Documents: []SearchDocument{document}})
		last = newSearchCursor(query, rootIDs, normalizedText, snapshots, document,
			document.ID, rank, score, sortValue, sortValue)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate raw search documents: %w", err)
	}
	if !hasMore {
		return groups, "", nil
	}
	nextCursor, err := encodeSearchCursor(last)
	if err != nil {
		return nil, "", err
	}
	return groups, nextCursor, nil
}

func buildAudiobookSearchStatement(
	query SearchQuery,
	rootIDs []string,
	normalizedText string,
	cursor *searchCursor,
) (rawSearchStatement, error) {
	arguments := []any{rootIDs}
	conditions := []string{"root_id=any($1::text[])", "entity_kind='audiobook'"}
	addArgument := func(value any) string {
		arguments = append(arguments, value)
		return fmt.Sprintf("$%d", len(arguments))
	}
	textPlaceholder := ""
	rankExpression := "0::integer"
	scoreExpression := "0::float8"
	if normalizedText != "" {
		textPlaceholder = addArgument(normalizedText)
		if utf8.RuneCountInString(normalizedText) == 2 {
			conditions = append(conditions,
				"search_grams @> array["+textPlaceholder+"]::text[]")
		} else {
			conditions = append(conditions, `(`+
				fuzzyGramCondition(fuzzySearchGrams(normalizedText), addArgument)+`
				and (strpos(search_compact,`+textPlaceholder+`)>0
				     or word_similarity(`+textPlaceholder+`,search_compact)>=0.3)
			)`)
		}
		rankExpression = `case
			when compact_name=` + textPlaceholder + ` or compact_path=` + textPlaceholder + ` then 0
			when left(compact_name,length(` + textPlaceholder + `))=` + textPlaceholder + `
			  or left(compact_path,length(` + textPlaceholder + `))=` + textPlaceholder + ` then 1
			when strpos(compact_name,` + textPlaceholder + `)>0
			  or strpos(compact_path,` + textPlaceholder + `)>0
			  or exists(select 1 from unnest(filenames) f where strpos(catalog_search_compact(f),` + textPlaceholder + `)>0) then 2
			when strpos(compact_metadata,` + textPlaceholder + `)>0 then 3
			else 4 end`
		scoreExpression = "word_similarity(" + textPlaceholder + ",search_compact)::float8"
	}
	if author := strings.TrimSpace(query.Author); author != "" {
		placeholder := addArgument(searchCompact(author))
		conditions = append(conditions, "compact_authors @> array["+placeholder+"]::text[]")
	}
	if genre := strings.TrimSpace(query.Genre); genre != "" {
		placeholder := addArgument(searchCompact(genre))
		conditions = append(conditions, "compact_genres @> array["+placeholder+"]::text[]")
	}
	if reviewState := strings.TrimSpace(query.ReviewState); reviewState != "" {
		conditions = append(conditions, "review_state="+addArgument(reviewState))
	}
	if query.PublicationYear != nil {
		conditions = append(conditions, "publication_year="+addArgument(*query.PublicationYear))
	}
	sortSpec := searchDocumentSorts[query.Sort]
	includeListingSort := normalizedText == ""
	sortCursorExpression := "''::text"
	memberOrder := rankExpression + " asc, " + scoreExpression + " desc"
	outerOrder := "rank_value asc,score_value desc"
	if includeListingSort {
		sortCursorExpression = "sort_value"
		if sortSpec.cursorKind == cursorYear {
			sortCursorExpression = "sort_value::text"
		} else if sortSpec.cursorKind == cursorTime {
			sortCursorExpression = `to_char(sort_value at time zone 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`
		}
		memberOrder += ", " + sortSpec.expression + " " + sortSpec.direction
		outerOrder += ",sort_value " + sortSpec.direction
	}
	memberOrder += ", compact_name,relative_path,entity_kind,root_id,document_id"
	outerOrder += ",compact_name,relative_path,entity_kind,root_id,document_id"
	cursorCondition := "true"
	if cursor != nil {
		parts := []searchKeyPart{
			{expression: "rank_value", operator: ">", placeholder: addArgument(cursor.Rank)},
			{expression: "score_value", operator: "<", placeholder: addArgument(cursor.Score)},
		}
		if includeListingSort {
			sortValue, parseErr := parseBookCursorValue(cursor.SortValue, sortSpec.cursorKind)
			if parseErr != nil {
				return rawSearchStatement{}, parseErr
			}
			sortOperator := ">"
			if sortSpec.direction == "desc" {
				sortOperator = "<"
			}
			parts = append(parts, searchKeyPart{expression: "sort_value", operator: sortOperator, placeholder: addArgument(sortValue)})
		}
		parts = append(parts,
			searchKeyPart{expression: "compact_name", operator: ">", placeholder: addArgument(cursor.Name)},
			searchKeyPart{expression: "relative_path", operator: ">", placeholder: addArgument(cursor.RelativePath)},
			searchKeyPart{expression: "entity_kind", operator: ">", placeholder: addArgument(cursor.Kind)},
			searchKeyPart{expression: "root_id", operator: ">", placeholder: addArgument(cursor.RootID)},
			searchKeyPart{expression: "document_id", operator: ">", placeholder: addArgument(cursor.DocumentID)},
		)
		cursorCondition = lexicographicSearchAfter(parts)
	}
	headerLimit := addArgument(query.Limit + 1)
	if normalizedText == "" {
		branchOrder := "sort_value " + sortSpec.direction +
			",compact_name,relative_path,entity_kind,root_id,document_id"
		baseColumns := `root_id, document_id, entity_kind, book_id, relative_path, display_name,
		       size, modified_at, title, authors, compact_authors, series, narrators,
		       genres, compact_genres, review_state, review_reason, publication_year, created_at,
		       recently_scanned_at, filenames, duration_ms, duplicate_group_id,
		       duplicate_count, compact_name, compact_path, compact_metadata,
		       search_compact, search_terms, search_grams`
		return rawSearchStatement{SQL: `
			with requested_roots as (
			  select requested_root_id
			  from unnest($1::text[]) requested(requested_root_id)
			), singleton_leaders as (
			  select leader.*
			  from requested_roots requested
			  cross join lateral (
			    select *
			    from (
			      select ` + baseColumns + `,
			             document_id as group_id, 0::integer as rank_value,
			             0::float8 as score_value, ` + sortSpec.expression + ` as sort_value
			      from library_search_documents
			      where ` + strings.Join(conditions, " and ") + `
			        and root_id=requested.requested_root_id
			        and duplicate_group_id is null
			    ) singleton
			    where ` + cursorCondition + `
			    order by ` + branchOrder + `
			    limit ` + headerLimit + `
			  ) leader
			), duplicate_leaders as (
			  select leader.*
			  from requested_roots requested
			  cross join lateral (
			    select *
			    from (
			      select distinct on (duplicate_group_id) ` + baseColumns + `,
			             duplicate_group_id as group_id, 0::integer as rank_value,
			             0::float8 as score_value, ` + sortSpec.expression + ` as sort_value
			      from library_search_documents
			      where ` + strings.Join(conditions, " and ") + `
			        and root_id=requested.requested_root_id
			        and duplicate_group_id is not null
			      order by duplicate_group_id, ` + branchOrder + `
			    ) duplicate_leader
			    where ` + cursorCondition + `
			    order by ` + branchOrder + `
			    limit ` + headerLimit + `
			  ) leader
			), candidates as (
			  select * from singleton_leaders
			  union all
			  select * from duplicate_leaders
			)
			select root_id, document_id, entity_kind, book_id, relative_path, display_name,
			       size, modified_at, title, authors, compact_authors, series, narrators,
			       genres, compact_genres, coalesce(review_state,''), coalesce(review_reason,''), publication_year, created_at,
			       recently_scanned_at, filenames, duration_ms, duplicate_group_id,
			       duplicate_count, compact_name, compact_path, compact_metadata,
			       search_compact, search_terms, search_grams,
			       rank_value,score_value,` + sortCursorExpression + `,compact_name,group_id
			from candidates
			order by ` + outerOrder + `
			limit ` + headerLimit, Arguments: arguments}, nil
	}
	return rawSearchStatement{SQL: `
		with matched as (
		select root_id, document_id, entity_kind, book_id, relative_path, display_name,
		       size, modified_at, title, authors, compact_authors, series, narrators,
		       genres, compact_genres, coalesce(review_state,'') as review_state,
		       coalesce(review_reason,'') as review_reason, publication_year, created_at,
		       recently_scanned_at, filenames, duration_ms, duplicate_group_id,
		       duplicate_count, compact_name, compact_path, compact_metadata,
		       search_compact, search_terms, search_grams,
		       coalesce(duplicate_group_id,document_id) as group_id,
		       ` + rankExpression + ` as rank_value,
		       ` + scoreExpression + ` as score_value,
		       ` + sortSpec.expression + ` as sort_value,
		       row_number() over (
		         partition by root_id,coalesce(duplicate_group_id,document_id)
		         order by ` + memberOrder + `
		       ) as group_member_rank
		from library_search_documents
		where ` + strings.Join(conditions, " and ") + `
		), leaders as (
		  select * from matched where group_member_rank=1
		)
		select root_id, document_id, entity_kind, book_id, relative_path, display_name,
		       size, modified_at, title, authors, compact_authors, series, narrators,
		       genres, compact_genres, coalesce(review_state,''), coalesce(review_reason,''), publication_year, created_at,
		       recently_scanned_at, filenames, duration_ms, duplicate_group_id,
		       duplicate_count, compact_name, compact_path, compact_metadata,
		       search_compact, search_terms, search_grams,
		       rank_value,score_value,` + sortCursorExpression + `,compact_name,group_id
		from leaders
		where ` + cursorCondition + `
		order by ` + outerOrder + `
		limit ` + headerLimit, Arguments: arguments}, nil
}

func searchAudiobookGroupsTx(
	ctx context.Context,
	transaction pgx.Tx,
	query SearchQuery,
	rootIDs []string,
	normalizedText string,
	snapshots []SearchSnapshot,
	cursor *searchCursor,
) ([]SearchGroup, string, error) {
	statement, err := buildAudiobookSearchStatement(query, rootIDs, normalizedText, cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := transaction.Query(ctx, statement.SQL, statement.Arguments...)
	if err != nil {
		return nil, "", fmt.Errorf("query matching audiobook groups: %w", err)
	}
	var selected []searchGroupReference
	used := 0
	hasMore := false
	for rows.Next() {
		var rank int
		var score float64
		var sortValue string
		var normalizedName string
		var groupID uuid.UUID
		document, scanErr := scanSearchDocumentWithExtras(
			rows, &rank, &score, &sortValue, &normalizedName, &groupID,
		)
		if scanErr != nil {
			rows.Close()
			return nil, "", scanErr
		}
		count := max(1, document.DuplicateCount)
		if len(selected) > 0 && used+count > query.Limit {
			hasMore = true
			break
		}
		selected = append(selected, searchGroupReference{
			rootID: document.RootID, groupID: groupID, count: count,
			leader: newSearchCursor(
				query, rootIDs, normalizedText, snapshots, document,
				groupID, rank, score, sortValue, normalizedName,
			),
		})
		used += count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", fmt.Errorf("iterate matching audiobook groups: %w", err)
	}
	rows.Close()
	if len(selected) == 0 {
		return []SearchGroup{}, "", nil
	}
	selectedRoots := make([]string, len(selected))
	selectedIDs := make([]uuid.UUID, len(selected))
	groupIndexes := make(map[string]int, len(selected))
	groups := make([]SearchGroup, len(selected))
	for index, reference := range selected {
		selectedRoots[index], selectedIDs[index] = reference.rootID, reference.groupID
		groupIndexes[reference.rootID+"\x00"+reference.groupID.String()] = index
		groups[index].ID = reference.groupID
	}
	hydratedRows, err := transaction.Query(ctx, `
		select d.root_id, d.document_id, d.entity_kind, d.book_id,
		       d.relative_path, d.display_name, d.size, d.modified_at, d.title,
		       d.authors, d.compact_authors, d.series, d.narrators, d.genres,
		       d.compact_genres, coalesce(d.review_state,''), coalesce(d.review_reason,''), d.publication_year,
		       d.created_at, d.recently_scanned_at, d.filenames, d.duration_ms,
		       d.duplicate_group_id, d.duplicate_count, d.compact_name,
		       d.compact_path, d.compact_metadata, d.search_compact,
		       d.search_terms, d.search_grams
		from library_search_documents d
		join unnest($1::text[],$2::uuid[]) selected(root_id,group_id)
		  on selected.root_id=d.root_id
		 and selected.group_id=coalesce(d.duplicate_group_id,d.document_id)
		where d.entity_kind='audiobook'
		order by d.root_id, coalesce(d.duplicate_group_id,d.document_id),
		         d.duration_ms asc nulls last, d.compact_name,
		         d.relative_path, d.root_id, d.document_id
	`, selectedRoots, selectedIDs)
	if err != nil {
		return nil, "", fmt.Errorf("hydrate audiobook groups: %w", err)
	}
	for hydratedRows.Next() {
		document, scanErr := scanSearchDocument(hydratedRows)
		if scanErr != nil {
			hydratedRows.Close()
			return nil, "", scanErr
		}
		groupID := document.DuplicateGroupID
		if groupID == uuid.Nil {
			groupID = document.ID
		}
		index, exists := groupIndexes[document.RootID+"\x00"+groupID.String()]
		if !exists || document.DuplicateCount != selected[index].count ||
			(selected[index].count > 1 && document.DuplicateGroupID == uuid.Nil) ||
			(selected[index].count == 1 && document.DuplicateGroupID != uuid.Nil) {
			hydratedRows.Close()
			return nil, "", fmt.Errorf("%w: corrupt audiobook duplicate group", ErrInvalidData)
		}
		groups[index].Documents = append(groups[index].Documents, document)
	}
	if err := hydratedRows.Err(); err != nil {
		hydratedRows.Close()
		return nil, "", fmt.Errorf("iterate hydrated audiobook groups: %w", err)
	}
	hydratedRows.Close()
	for index := range groups {
		if len(groups[index].Documents) != selected[index].count {
			return nil, "", fmt.Errorf("%w: incomplete audiobook duplicate group", ErrInvalidData)
		}
		groups[index].Duplicate = len(groups[index].Documents) > 1
	}
	nextCursor := ""
	if hasMore {
		var encodeErr error
		nextCursor, encodeErr = encodeSearchCursor(selected[len(selected)-1].leader)
		if encodeErr != nil {
			return nil, "", encodeErr
		}
	}
	return groups, nextCursor, nil
}

func resolveSearchCursorTx(
	ctx context.Context,
	transaction pgx.Tx,
	query SearchQuery,
	rootIDs []string,
	normalizedText string,
	cursor *searchCursor,
) error {
	if cursor == nil || cursor.Root < 0 || cursor.Root >= len(rootIDs) {
		return ErrInvalidPage
	}
	cursor.RootID = rootIDs[cursor.Root]
	if query.Mode == SearchModeRaw {
		return resolveRawSearchCursorTx(ctx, transaction, query, normalizedText, cursor)
	}
	return resolveAudiobookSearchCursorTx(ctx, transaction, query, normalizedText, cursor)
}

func resolveRawSearchCursorTx(
	ctx context.Context,
	transaction pgx.Tx,
	query SearchQuery,
	normalizedText string,
	cursor *searchCursor,
) error {
	arguments := []any{cursor.RootID, cursor.DocumentID, normalizedText}
	addArgument := func(value any) string {
		arguments = append(arguments, value)
		return fmt.Sprintf("$%d", len(arguments))
	}
	textCondition := "search_grams @> array[$3]::text[]"
	if utf8.RuneCountInString(normalizedText) >= 3 {
		textCondition = `(` + fuzzyGramCondition(fuzzySearchGrams(normalizedText), addArgument) + `
			and (strpos(search_compact,$3)>0 or word_similarity($3,search_compact)>=0.3))`
	}
	rankExpression := `case
		when compact_name=$3 or compact_path=$3 then 0
		when left(compact_name,length($3))=$3 or left(compact_path,length($3))=$3 then 1
		when strpos(compact_name,$3)>0 or strpos(compact_path,$3)>0
		  or exists(select 1 from unnest(filenames) f where strpos(catalog_search_compact(f),$3)>0) then 2
		when strpos(compact_metadata,$3)>0 then 3
		else 4 end`
	row := transaction.QueryRow(ctx, `
		select `+rankExpression+`, word_similarity($3,search_compact)::float8,
		       compact_name, compact_name, relative_path, root_id, document_id,
		       document_id, entity_kind
		from library_search_documents
		where root_id=$1 and document_id=$2 and entity_kind in ('folder','file')
		  and `+textCondition, arguments...)
	return scanResolvedSearchCursor(row, cursor)
}

func resolveAudiobookSearchCursorTx(
	ctx context.Context,
	transaction pgx.Tx,
	query SearchQuery,
	normalizedText string,
	cursor *searchCursor,
) error {
	arguments := []any{cursor.RootID, cursor.GroupID}
	addArgument := func(value any) string {
		arguments = append(arguments, value)
		return fmt.Sprintf("$%d", len(arguments))
	}
	conditions := []string{
		"root_id=$1",
		"entity_kind='audiobook'",
		"coalesce(duplicate_group_id,document_id)=$2",
	}
	rankExpression := "0::integer"
	scoreExpression := "0::float8"
	if normalizedText != "" {
		textPlaceholder := addArgument(normalizedText)
		if utf8.RuneCountInString(normalizedText) == 2 {
			conditions = append(conditions, "search_grams @> array["+textPlaceholder+"]::text[]")
		} else {
			conditions = append(conditions, `(`+
				fuzzyGramCondition(fuzzySearchGrams(normalizedText), addArgument)+`
				and (strpos(search_compact,`+textPlaceholder+`)>0
				     or word_similarity(`+textPlaceholder+`,search_compact)>=0.3))`)
		}
		rankExpression = `case
			when compact_name=` + textPlaceholder + ` or compact_path=` + textPlaceholder + ` then 0
			when left(compact_name,length(` + textPlaceholder + `))=` + textPlaceholder + `
			  or left(compact_path,length(` + textPlaceholder + `))=` + textPlaceholder + ` then 1
			when strpos(compact_name,` + textPlaceholder + `)>0
			  or strpos(compact_path,` + textPlaceholder + `)>0
			  or exists(select 1 from unnest(filenames) f where strpos(catalog_search_compact(f),` + textPlaceholder + `)>0) then 2
			when strpos(compact_metadata,` + textPlaceholder + `)>0 then 3
			else 4 end`
		scoreExpression = "word_similarity(" + textPlaceholder + ",search_compact)::float8"
	}
	if author := strings.TrimSpace(query.Author); author != "" {
		conditions = append(conditions, "compact_authors @> array["+addArgument(searchCompact(author))+"]::text[]")
	}
	if genre := strings.TrimSpace(query.Genre); genre != "" {
		conditions = append(conditions, "compact_genres @> array["+addArgument(searchCompact(genre))+"]::text[]")
	}
	if reviewState := strings.TrimSpace(query.ReviewState); reviewState != "" {
		conditions = append(conditions, "review_state="+addArgument(reviewState))
	}
	if query.PublicationYear != nil {
		conditions = append(conditions, "publication_year="+addArgument(*query.PublicationYear))
	}

	sortSpec := searchDocumentSorts[query.Sort]
	sortCursorExpression := "''::text"
	memberOrder := rankExpression + " asc, " + scoreExpression + " desc"
	if normalizedText == "" {
		sortCursorExpression = sortSpec.expression
		if sortSpec.cursorKind == cursorYear {
			sortCursorExpression = "(" + sortSpec.expression + ")::text"
		} else if sortSpec.cursorKind == cursorTime {
			sortCursorExpression = `to_char((` + sortSpec.expression + `) at time zone 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`
		}
		memberOrder += ", " + sortSpec.expression + " " + sortSpec.direction
	}
	memberOrder += ", compact_name,relative_path,entity_kind,root_id,document_id"
	row := transaction.QueryRow(ctx, `
		select rank_value, score_value, sort_cursor, compact_name, relative_path,
		       root_id, document_id, group_id, entity_kind
		from (
		  select `+rankExpression+` as rank_value, `+scoreExpression+` as score_value,
		         `+sortCursorExpression+` as sort_cursor, compact_name, relative_path,
		         root_id, document_id, coalesce(duplicate_group_id,document_id) as group_id,
		         entity_kind
		  from library_search_documents
		  where `+strings.Join(conditions, " and ")+`
		  order by `+memberOrder+`
		  limit 1
		) leader`, arguments...)
	return scanResolvedSearchCursor(row, cursor)
}

func scanResolvedSearchCursor(row pgx.Row, cursor *searchCursor) error {
	var documentID, groupID uuid.UUID
	var kind SearchEntityKind
	err := row.Scan(
		&cursor.Rank, &cursor.Score, &cursor.SortValue, &cursor.Name,
		&cursor.RelativePath, &cursor.RootID, &documentID, &groupID, &kind,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidPage
	}
	if err != nil {
		return fmt.Errorf("resolve search cursor: %w", err)
	}
	if documentID != cursor.DocumentID || groupID != cursor.GroupID || kind != cursor.Kind {
		return ErrInvalidPage
	}
	return nil
}

type searchKeyPart struct {
	expression  string
	operator    string
	placeholder string
}

func lexicographicSearchAfter(parts []searchKeyPart) string {
	condition := parts[len(parts)-1].expression + parts[len(parts)-1].operator + parts[len(parts)-1].placeholder
	for index := len(parts) - 2; index >= 0; index-- {
		part := parts[index]
		condition = "(" + part.expression + part.operator + part.placeholder +
			" or (" + part.expression + "=" + part.placeholder + " and " + condition + "))"
	}
	return condition
}

func newSearchCursor(
	query SearchQuery,
	rootIDs []string,
	normalizedText string,
	snapshots []SearchSnapshot,
	document SearchDocument,
	groupID uuid.UUID,
	rank int,
	score float64,
	sortValue string,
	normalizedName string,
) searchCursor {
	rootOrdinal := -1
	for index, rootID := range rootIDs {
		if rootID == document.RootID {
			rootOrdinal = index
			break
		}
	}
	return searchCursor{
		Version: searchCursorVersion,
		Scope:   searchCursorScope(query, rootIDs, normalizedText), Snapshot: searchSnapshotDigest(snapshots),
		Root: rootOrdinal,
		Rank: rank, Score: score, SortValue: sortValue,
		Name: normalizedName, RelativePath: document.RelativePath, RootID: document.RootID,
		DocumentID: document.ID, GroupID: groupID, Kind: document.EntityKind,
	}
}

func encodeSearchCursor(cursor searchCursor) (string, error) {
	if cursor.Version != searchCursorVersion || cursor.Root < 0 || cursor.DocumentID == uuid.Nil ||
		cursor.GroupID == uuid.Nil || !validSearchDigest(cursor.Scope) ||
		!validSearchDigest(cursor.Snapshot) || !validSearchCursorKind(cursor.Kind) {
		return "", ErrInvalidPage
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode search cursor: %w", err)
	}
	if len(encoded) > 4096 {
		return "", ErrInvalidPage
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeSearchCursor(encoded string) (*searchCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) > 4096 {
		return nil, ErrInvalidPage
	}
	var cursor searchCursor
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return nil, ErrInvalidPage
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidPage
	}
	if cursor.Version != searchCursorVersion || cursor.Root < 0 ||
		cursor.DocumentID == uuid.Nil || cursor.GroupID == uuid.Nil ||
		!validSearchCursorKind(cursor.Kind) {
		return nil, ErrInvalidPage
	}
	if !validSearchDigest(cursor.Scope) || !validSearchDigest(cursor.Snapshot) {
		return nil, ErrInvalidPage
	}
	return &cursor, nil
}

func validSearchCursorKind(kind SearchEntityKind) bool {
	return kind == SearchEntityAudiobook || kind == SearchEntityFolder || kind == SearchEntityFile
}

func validSearchDigest(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validSearchCursorPosition(cursor searchCursor, query SearchQuery, rootIDs []string, normalizedText string) bool {
	rootFound := false
	for _, rootID := range rootIDs {
		rootFound = rootFound || cursor.RootID == rootID
	}
	if !rootFound || (normalizedText == "" && (cursor.Rank != 0 || cursor.Score != 0)) {
		return false
	}
	if query.Mode == SearchModeRaw {
		return (cursor.Kind == SearchEntityFolder || cursor.Kind == SearchEntityFile) &&
			cursor.GroupID == cursor.DocumentID && ValidateRelativePath(cursor.RelativePath) == nil &&
			cursor.SortValue == cursor.Name
	}
	if cursor.Kind != SearchEntityAudiobook || ValidateSourceFolder(cursor.RelativePath) != nil {
		return false
	}
	if normalizedText != "" {
		return cursor.SortValue == ""
	}
	spec := searchDocumentSorts[query.Sort]
	parsed, err := parseBookCursorValue(cursor.SortValue, spec.cursorKind)
	if err != nil {
		return false
	}
	switch spec.cursorKind {
	case cursorText:
		return utf8.ValidString(cursor.SortValue)
	case cursorYear:
		return strconv.Itoa(parsed.(int)) == cursor.SortValue
	case cursorTime:
		return parsed.(time.Time).UTC().Format("2006-01-02T15:04:05.000000Z") == cursor.SortValue
	default:
		return false
	}
}

func searchCursorMatchesQuery(
	cursor searchCursor,
	query SearchQuery,
	rootIDs []string,
	normalizedText string,
) bool {
	return cursor.Scope == searchCursorScope(query, rootIDs, normalizedText)
}

func searchCursorMatchesSnapshots(cursor searchCursor, snapshots []SearchSnapshot) bool {
	return cursor.Snapshot == searchSnapshotDigest(snapshots)
}

func searchCursorScope(query SearchQuery, rootIDs []string, normalizedText string) string {
	value := struct {
		Mode                                   SearchMode `json:"mode"`
		RootIDs                                []string   `json:"rootIds"`
		Text, Author, Genre, ReviewState, Sort string
		PublicationYear                        *int `json:"publicationYear"`
		Limit                                  int  `json:"limit"`
	}{
		Mode: query.Mode, RootIDs: rootIDs, Text: normalizedText,
		Author: searchCompact(strings.TrimSpace(query.Author)), Genre: searchCompact(strings.TrimSpace(query.Genre)),
		ReviewState: strings.TrimSpace(query.ReviewState), Sort: query.Sort,
		PublicationYear: copyInt(query.PublicationYear), Limit: query.Limit,
	}
	return searchDigest(value)
}

func searchSnapshotDigest(snapshots []SearchSnapshot) string {
	type identity struct {
		RootID    string    `json:"rootId"`
		ScanRunID uuid.UUID `json:"scanRunId"`
	}
	values := make([]identity, len(snapshots))
	for index, snapshot := range snapshots {
		values[index] = identity{snapshot.RootID, snapshot.ScanRunID}
	}
	return searchDigest(values)
}

func searchDigest(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func equalOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func scanSearchDocument(row scanner) (SearchDocument, error) {
	return scanSearchDocumentWithExtras(row)
}

func scanSearchDocumentWithExtras(row scanner, extras ...any) (SearchDocument, error) {
	var document SearchDocument
	var bookID, duplicateGroupID *uuid.UUID
	destinations := []any{
		&document.RootID, &document.ID, &document.EntityKind, &bookID,
		&document.RelativePath, &document.DisplayName, &document.Size, &document.ModifiedAt,
		&document.Title, &document.Authors, &document.CompactAuthors, &document.Series,
		&document.Narrators, &document.Genres, &document.CompactGenres, &document.ReviewState,
		&document.ReviewReason, &document.PublicationYear, &document.CreatedAt,
		&document.RecentlyScannedAt,
		&document.Filenames, &document.DurationMS, &duplicateGroupID, &document.DuplicateCount,
		&document.CompactName, &document.CompactPath, &document.CompactMetadata,
		&document.SearchCompact, &document.SearchTerms, &document.SearchGrams,
	}
	destinations = append(destinations, extras...)
	if err := row.Scan(destinations...); err != nil {
		return SearchDocument{}, fmt.Errorf("scan search document: %w", err)
	}
	if bookID != nil {
		document.BookID = *bookID
	}
	if duplicateGroupID != nil {
		document.DuplicateGroupID = *duplicateGroupID
	}
	return document, nil
}

func (repository *PostgresRepository) FinalizeSuccessfulScan(
	ctx context.Context,
	scanID uuid.UUID,
	inferred []InferredBook,
) error {
	if repository == nil || repository.pool == nil || scanID == uuid.Nil {
		return ErrInvalidData
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin successful scan finalization: %w", err)
	}
	defer transaction.Rollback(ctx)

	var rootHint string
	if err := transaction.QueryRow(ctx, `
		select root_id from scan_runs where id=$1
	`, scanID).Scan(&rootHint); err != nil {
		return mapNotFound(err, "locate successful scan root")
	}
	var rootID string
	if err := transaction.QueryRow(ctx, `
		select id from library_roots where id=$1 for update
	`, rootHint).Scan(&rootID); err != nil {
		return mapNotFound(err, "lock successful scan root")
	}
	var lockedRootID, status string
	if err := transaction.QueryRow(ctx, `
		select root_id, status from scan_runs where id=$1 for update
	`, scanID).Scan(&lockedRootID, &status); err != nil {
		return mapNotFound(err, "load successful scan")
	}
	if lockedRootID != rootID || status != "running" {
		return ErrConflict
	}
	completedAt := repository.now()
	if err := markUnseenSourceFilesMissingTx(ctx, transaction, rootID, scanID, completedAt); err != nil {
		return err
	}
	if err := replaceInferredBooksTx(ctx, transaction, rootID, inferred); err != nil {
		return err
	}
	projectionInput, err := loadSearchProjectionInputTx(
		ctx, transaction, rootID, scanID, completedAt,
	)
	if err != nil {
		return err
	}
	projection, err := BuildSearchProjection(projectionInput)
	if err != nil {
		return fmt.Errorf("build search projection: %w", err)
	}
	if err := publishSearchProjectionTx(ctx, transaction, projection); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `
		update library_roots set scanned_at=$2, updated_at=now() where id=$1
	`, rootID, completedAt); err != nil {
		return fmt.Errorf("update successful scan root: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		update scan_runs
		set finished_at=$2, status='succeeded', error=null
		where id=$1
	`, scanID, completedAt); err != nil {
		return fmt.Errorf("finish successful scan: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit successful scan finalization: %w", err)
	}
	return nil
}

func markUnseenSourceFilesMissingTx(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	scanID uuid.UUID,
	missingAt time.Time,
) error {
	rows, err := transaction.Query(ctx, `
		update source_files
		set missing_at=$3, updated_at=now()
		where root_id=$1 and scan_run_id is distinct from $2 and missing_at is null
		returning id
	`, rootID, scanID, missingAt)
	if err != nil {
		return fmt.Errorf("mark missing source files: %w", err)
	}
	var missingIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("read missing source file: %w", err)
		}
		missingIDs = append(missingIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read missing source files: %w", err)
	}
	rows.Close()
	for _, id := range missingIDs {
		if err := insertAvailabilityEvent(
			ctx, transaction, rootID, "source_file", id, "missing", &scanID,
		); err != nil {
			return err
		}
	}
	return nil
}

func loadSearchProjectionInputTx(
	ctx context.Context,
	transaction pgx.Tx,
	rootID string,
	scanID uuid.UUID,
	completedAt time.Time,
) (SearchProjectionInput, error) {
	input := SearchProjectionInput{
		RootID: rootID, ScanRunID: scanID, CompletedAt: completedAt,
		Assignments: make(map[uuid.UUID][]uuid.UUID),
		Evidence:    make(map[uuid.UUID]GroupingEvidence),
	}
	if err := transaction.QueryRow(ctx, `
		select generation, updated_at
		from catalog_generations where root_id=$1
	`, rootID).Scan(&input.Generation.Value, &input.Generation.UpdatedAt); err != nil {
		return input, mapNotFound(err, "load search catalog generation")
	}
	input.Generation.RootID = rootID
	bookRows, err := transaction.Query(ctx, `
		select `+bookColumns("b")+`
		from books b
		where b.root_id=$1 and b.missing_at is null
		order by b.id
	`, rootID)
	if err != nil {
		return input, fmt.Errorf("load active search books: %w", err)
	}
	for bookRows.Next() {
		book, scanErr := scanBook(bookRows)
		if scanErr != nil {
			bookRows.Close()
			return input, fmt.Errorf("scan active search book: %w", scanErr)
		}
		input.Books = append(input.Books, book)
	}
	if err := bookRows.Err(); err != nil {
		bookRows.Close()
		return input, fmt.Errorf("iterate active search books: %w", err)
	}
	bookRows.Close()

	fileRows, err := transaction.Query(ctx, `
		select id, root_id, relative_path, size, modified_at,
		       coalesce(sha256,''), media, scan_run_id, missing_at
		from source_files
		where root_id=$1 and missing_at is null
		order by id
	`, rootID)
	if err != nil {
		return input, fmt.Errorf("load active search files: %w", err)
	}
	for fileRows.Next() {
		file, scanErr := scanSourceFile(fileRows)
		if scanErr != nil {
			fileRows.Close()
			return input, fmt.Errorf("scan active search file: %w", scanErr)
		}
		input.Files = append(input.Files, file)
	}
	if err := fileRows.Err(); err != nil {
		fileRows.Close()
		return input, fmt.Errorf("iterate active search files: %w", err)
	}
	fileRows.Close()

	assignmentRows, err := transaction.Query(ctx, `
		select bf.book_id, bf.file_id
		from book_files bf
		join books b on b.id=bf.book_id and b.root_id=bf.root_id
		join source_files f on f.id=bf.file_id and f.root_id=bf.root_id
		where bf.root_id=$1 and b.missing_at is null and f.missing_at is null
		order by bf.book_id, bf.file_id
	`, rootID)
	if err != nil {
		return input, fmt.Errorf("load active search assignments: %w", err)
	}
	for assignmentRows.Next() {
		var bookID, fileID uuid.UUID
		if err := assignmentRows.Scan(&bookID, &fileID); err != nil {
			assignmentRows.Close()
			return input, fmt.Errorf("scan active search assignment: %w", err)
		}
		input.Assignments[bookID] = append(input.Assignments[bookID], fileID)
	}
	if err := assignmentRows.Err(); err != nil {
		assignmentRows.Close()
		return input, fmt.Errorf("iterate active search assignments: %w", err)
	}
	assignmentRows.Close()

	evidenceRows, err := transaction.Query(ctx, `
		select evidence.book_id, evidence.reason_code, b.confidence::float8,
		       b.review_state, evidence.evidence
		from book_grouping_evidence evidence
		join books b on b.id=evidence.book_id and b.root_id=evidence.root_id
		where evidence.root_id=$1 and b.missing_at is null
		order by evidence.book_id
	`, rootID)
	if err != nil {
		return input, fmt.Errorf("load search grouping evidence: %w", err)
	}
	for evidenceRows.Next() {
		var bookID uuid.UUID
		var evidence GroupingEvidence
		var values []byte
		if err := evidenceRows.Scan(
			&bookID, &evidence.ReasonCode, &evidence.Confidence,
			&evidence.ReviewState, &values,
		); err != nil {
			evidenceRows.Close()
			return input, fmt.Errorf("scan search grouping evidence: %w", err)
		}
		if err := json.Unmarshal(values, &evidence.Values); err != nil {
			evidenceRows.Close()
			return input, fmt.Errorf("decode search grouping evidence: %w", err)
		}
		input.Evidence[bookID] = evidence
	}
	if err := evidenceRows.Err(); err != nil {
		evidenceRows.Close()
		return input, fmt.Errorf("iterate search grouping evidence: %w", err)
	}
	evidenceRows.Close()
	return input, nil
}

func publishSearchProjectionTx(
	ctx context.Context,
	transaction pgx.Tx,
	projection SearchProjection,
) error {
	if _, err := transaction.Exec(ctx,
		`delete from library_search_documents where root_id=$1`,
		projection.Snapshot.RootID,
	); err != nil {
		return fmt.Errorf("clear prior search projection: %w", err)
	}
	for _, document := range projection.Documents {
		if _, err := transaction.Exec(ctx, `
			insert into library_search_documents(
				root_id, document_id, entity_kind, book_id, relative_path, display_name,
				size, modified_at, title, authors, compact_authors, series, narrators,
				genres, compact_genres, review_state, review_reason, publication_year, created_at,
				recently_scanned_at, filenames, duration_ms, duplicate_group_id,
				duplicate_count, compact_name, compact_path, compact_metadata,
				search_compact, search_terms, search_grams
			) values (
				$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
				$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30
			)
		`, document.RootID, document.ID, document.EntityKind,
			nullableUUID(document.BookID), document.RelativePath, document.DisplayName,
			document.Size, document.ModifiedAt, document.Title, nonNilStrings(document.Authors),
			nonNilStrings(document.CompactAuthors), document.Series,
			nonNilStrings(document.Narrators), nonNilStrings(document.Genres),
			nonNilStrings(document.CompactGenres), nullableText(document.ReviewState),
			nullableText(document.ReviewReason), document.PublicationYear,
			document.CreatedAt, document.RecentlyScannedAt, nonNilStrings(document.Filenames),
			document.DurationMS, nullableUUID(document.DuplicateGroupID), document.DuplicateCount,
			document.CompactName, document.CompactPath, document.CompactMetadata,
			document.SearchCompact, document.SearchTerms, nonNilStrings(document.SearchGrams),
		); err != nil {
			return classifyConstraint(err, "publish search document")
		}
	}
	if _, err := transaction.Exec(ctx, `
		insert into library_search_snapshots(
			root_id, scan_run_id, completed_at, document_count, catalog_generation
		) values ($1,$2,$3,$4,$5)
		on conflict (root_id) do update
		set scan_run_id=excluded.scan_run_id,
		    completed_at=excluded.completed_at,
		    document_count=excluded.document_count,
		    catalog_generation=excluded.catalog_generation
	`, projection.Snapshot.RootID, projection.Snapshot.ScanRunID,
		projection.Snapshot.CompletedAt, projection.Snapshot.DocumentCount,
		projection.Snapshot.CatalogGeneration); err != nil {
		return classifyConstraint(err, "publish search snapshot")
	}
	return nil
}

func nullableUUID(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
