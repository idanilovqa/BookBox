package metadata

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"netstorage.local/server/internal/catalog"
)

const (
	AutoApplyThreshold = 0.90
	AutoApplyLead      = 0.08

	titleWeight  = 0.55
	authorWeight = 0.25
	isbnWeight   = 0.15
	yearWeight   = 0.05
)

type ScoredCandidate struct {
	Candidate
	Score float64
}

type Decision struct {
	Book        catalog.InferredBook
	Candidates  []ScoredCandidate
	Selected    *Candidate
	ReviewState string
}

type Matcher struct {
	providers []Provider
}

func NewMatcher(providers ...Provider) *Matcher {
	filtered := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil {
			filtered = append(filtered, provider)
		}
	}
	return &Matcher{providers: filtered}
}

func (matcher *Matcher) Match(
	ctx context.Context,
	local catalog.InferredBook,
) (Decision, error) {
	if local.MetadataSource == catalog.MetadataConfirmed ||
		local.ReviewState == catalog.ReviewConfirmed {
		return Decision{
			Book:        local,
			ReviewState: local.ReviewState,
		}, nil
	}
	query := Query{
		Title:  local.Title,
		Author: firstNonEmpty(local.Authors),
		ISBN:   inferredISBN(local),
	}
	var scored []ScoredCandidate
	if matcher != nil {
		for _, provider := range matcher.providers {
			candidates, err := provider.Search(ctx, query)
			if err != nil {
				return Decision{}, err
			}
			for _, candidate := range candidates {
				scored = append(scored, ScoredCandidate{
					Candidate: candidate,
					Score:     score(local, candidate),
				})
			}
		}
	}

	decision := decide(scored)
	decision.Book = local
	if len(decision.Candidates) == 0 {
		decision.ReviewState = local.ReviewState
		return decision, nil
	}
	if decision.Selected == nil {
		decision.Book.ReviewState = catalog.ReviewNeedsReview
		return decision, nil
	}

	decision.Book = applyProvider(local, *decision.Selected, decision.Candidates[0].Score)
	decision.Book.ReviewState = decision.ReviewState
	return decision, nil
}

func score(local catalog.InferredBook, candidate Candidate) float64 {
	var weightedScore float64
	var availableWeight float64

	if localTitle, candidateTitle := normalizeText(local.Title, false),
		normalizeText(candidate.Title, false); localTitle != "" && candidateTitle != "" {
		weightedScore += titleWeight * textSimilarity(localTitle, candidateTitle)
		availableWeight += titleWeight
	}

	localAuthors := normalizedAuthors(local.Authors)
	candidateAuthors := normalizedAuthors(candidate.Authors)
	if len(localAuthors) > 0 && len(candidateAuthors) > 0 {
		weightedScore += authorWeight * authorSimilarity(localAuthors, candidateAuthors)
		availableWeight += authorWeight
	}

	localISBN := inferredISBN(local)
	candidateISBN := normalizeISBN(candidate.ISBN)
	if localISBN != "" && candidateISBN != "" {
		if localISBN == candidateISBN {
			weightedScore += isbnWeight
		}
		availableWeight += isbnWeight
	}

	localYear := inferredYear(local)
	candidateYear := yearFromDate(candidate.PublishedDate)
	if localYear != 0 && candidateYear != 0 {
		if localYear == candidateYear {
			weightedScore += yearWeight
		}
		availableWeight += yearWeight
	}

	if availableWeight == 0 {
		return 0
	}
	return roundScore(weightedScore / availableWeight)
}

func decide(candidates []ScoredCandidate) Decision {
	sorted := append([]ScoredCandidate(nil), candidates...)
	sort.SliceStable(sorted, func(left, right int) bool {
		if sorted[left].Score != sorted[right].Score {
			return sorted[left].Score > sorted[right].Score
		}
		if sorted[left].Provider != sorted[right].Provider {
			return sorted[left].Provider < sorted[right].Provider
		}
		return sorted[left].ProviderID < sorted[right].ProviderID
	})

	decision := Decision{
		Candidates:  sorted,
		ReviewState: catalog.ReviewUnreviewed,
	}
	if len(sorted) == 0 {
		return decision
	}
	lead := sorted[0].Score
	if len(sorted) > 1 {
		lead = roundScore(lead - sorted[1].Score)
	}
	if sorted[0].Score >= AutoApplyThreshold && lead >= AutoApplyLead {
		selected := sorted[0].Candidate
		decision.Selected = &selected
		return decision
	}
	decision.ReviewState = catalog.ReviewNeedsReview
	return decision
}

func MergeMetadata(
	confirmed Candidate,
	local Candidate,
	provider Candidate,
	inferred Candidate,
) Candidate {
	result := Candidate{
		Provider: firstString(
			confirmed.Provider,
			provider.Provider,
			local.Provider,
			inferred.Provider,
		),
		ProviderID: firstString(
			confirmed.ProviderID,
			provider.ProviderID,
			local.ProviderID,
			inferred.ProviderID,
		),
		Title:          firstString(confirmed.Title, local.Title, provider.Title, inferred.Title),
		Subtitle:       firstString(confirmed.Subtitle, local.Subtitle, provider.Subtitle, inferred.Subtitle),
		Publisher:      firstString(confirmed.Publisher, local.Publisher, provider.Publisher, inferred.Publisher),
		PublishedDate:  firstString(confirmed.PublishedDate, local.PublishedDate, provider.PublishedDate, inferred.PublishedDate),
		Authors:        firstStrings(confirmed.Authors, local.Authors, provider.Authors, inferred.Authors),
		Narrators:      firstStrings(confirmed.Narrators, local.Narrators, provider.Narrators, inferred.Narrators),
		Series:         firstString(confirmed.Series, local.Series, provider.Series, inferred.Series),
		SeriesPosition: firstString(confirmed.SeriesPosition, local.SeriesPosition, provider.SeriesPosition, inferred.SeriesPosition),
		ISBN:           firstString(confirmed.ISBN, local.ISBN, provider.ISBN, inferred.ISBN),
		CoverURL:       firstString(confirmed.CoverURL, local.CoverURL, provider.CoverURL, inferred.CoverURL),
	}
	result.Subjects = uniqueStrings(
		provider.Subjects,
		confirmed.Subjects,
		local.Subjects,
		inferred.Subjects,
	)
	result.Genres = normalizeGenres(uniqueStrings(
		confirmed.Genres,
		local.Genres,
		inferred.Genres,
		provider.Genres,
		genresFromSubjects(provider.Subjects),
	))
	return result
}

func applyProvider(
	book catalog.InferredBook,
	provider Candidate,
	confidence float64,
) catalog.InferredBook {
	current := candidateFromBook(book)
	var local Candidate
	var inferred Candidate
	if book.MetadataSource == catalog.MetadataLocal ||
		book.MetadataSource == catalog.MetadataMixed {
		local = current
	} else {
		inferred = current
	}
	merged := MergeMetadata(Candidate{}, local, provider, inferred)

	book.Title = merged.Title
	book.Subtitle = merged.Subtitle
	book.Authors = merged.Authors
	book.Narrators = merged.Narrators
	book.Series = merged.Series
	book.SeriesPosition = merged.SeriesPosition
	book.Publisher = merged.Publisher
	book.PublishedDate = merged.PublishedDate
	if year := yearFromDate(merged.PublishedDate); year != 0 {
		book.PublicationYear = &year
	}
	book.Genres = merged.Genres
	book.ProviderSubjects = merged.Subjects
	if book.Identifiers == nil {
		book.Identifiers = make(map[string]any)
	}
	if merged.ISBN != "" {
		book.Identifiers["isbn"] = merged.ISBN
	}
	if provider.Provider != "" && provider.ProviderID != "" {
		book.Identifiers[provider.Provider] = provider.ProviderID
	}
	if local.Title != "" || len(local.Authors) > 0 {
		book.MetadataSource = catalog.MetadataMixed
	} else {
		book.MetadataSource = provider.Provider
	}
	book.Confidence = confidence
	return book
}

func candidateFromBook(book catalog.InferredBook) Candidate {
	return Candidate{
		Title:          book.Title,
		Subtitle:       book.Subtitle,
		Publisher:      book.Publisher,
		PublishedDate:  book.PublishedDate,
		Authors:        append([]string(nil), book.Authors...),
		Narrators:      append([]string(nil), book.Narrators...),
		Genres:         append([]string(nil), book.Genres...),
		Subjects:       append([]string(nil), book.ProviderSubjects...),
		Series:         book.Series,
		SeriesPosition: book.SeriesPosition,
		ISBN:           inferredISBN(book),
	}
}

func inferredISBN(book catalog.InferredBook) string {
	type rankedISBN struct {
		value    string
		key      string
		priority int
	}
	var candidates []rankedISBN
	for key, value := range book.Identifiers {
		normalizedKey := strings.ReplaceAll(normalizeText(key, false), " ", "")
		if !strings.Contains(normalizedKey, "isbn") {
			continue
		}
		for _, isbn := range isbnValues(value) {
			candidates = append(candidates, rankedISBN{
				value:    isbn,
				key:      normalizedKey,
				priority: isbnPriority(normalizedKey, isbn),
			})
		}
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].priority != candidates[right].priority {
			return candidates[left].priority < candidates[right].priority
		}
		if candidates[left].key != candidates[right].key {
			return candidates[left].key < candidates[right].key
		}
		return candidates[left].value < candidates[right].value
	})
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].value
}

func isbnValues(value any) []string {
	switch typed := value.(type) {
	case string:
		if isbn := normalizeISBN(typed); isbn != "" {
			return []string{isbn}
		}
	case []string:
		var result []string
		for _, item := range typed {
			if isbn := normalizeISBN(item); isbn != "" {
				result = append(result, isbn)
			}
		}
		return result
	case []any:
		var result []string
		for _, item := range typed {
			result = append(result, isbnValues(item)...)
		}
		return result
	}
	return nil
}

func isbnPriority(key, isbn string) int {
	switch {
	case strings.Contains(key, "13") && len(isbn) == 13:
		return 0
	case len(isbn) == 13:
		return 1
	case key == "isbn":
		return 2
	case strings.Contains(key, "10"):
		return 3
	default:
		return 4
	}
}

func normalizeISBN(value string) string {
	var result strings.Builder
	for _, character := range strings.ToUpper(value) {
		if unicode.IsDigit(character) || character == 'X' {
			result.WriteRune(character)
		}
	}
	normalized := result.String()
	if len(normalized) != 10 && len(normalized) != 13 {
		return ""
	}
	return normalized
}

func inferredYear(book catalog.InferredBook) int {
	if book.PublicationYear != nil && *book.PublicationYear > 0 {
		return *book.PublicationYear
	}
	return yearFromDate(book.PublishedDate)
}

func yearFromDate(value string) int {
	value = strings.TrimSpace(value)
	if len(value) < 4 {
		return 0
	}
	year, err := strconv.Atoi(value[:4])
	if err != nil || year < 1 {
		return 0
	}
	return year
}

func normalizedAuthors(authors []string) []string {
	result := make([]string, 0, len(authors))
	for _, author := range authors {
		if normalized := normalizeText(author, true); normalized != "" {
			result = append(result, normalized)
		}
	}
	sort.Strings(result)
	return result
}

func normalizeText(value string, sortTokens bool) string {
	tokens := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(value)), func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character)
	})
	if sortTokens {
		sort.Strings(tokens)
	}
	return strings.Join(tokens, " ")
}

func authorSimilarity(left, right []string) float64 {
	var total float64
	for _, local := range left {
		total += bestAuthorSimilarity(local, right)
	}
	for _, candidate := range right {
		total += bestAuthorSimilarity(candidate, left)
	}
	return total / float64(len(left)+len(right))
}

func bestAuthorSimilarity(author string, candidates []string) float64 {
	var best float64
	for _, candidate := range candidates {
		if similarity := textSimilarity(author, candidate); similarity > best {
			best = similarity
		}
	}
	return best
}

func textSimilarity(left, right string) float64 {
	if left == right {
		return 1
	}
	leftTokens := strings.Fields(left)
	rightTokens := strings.Fields(right)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return 0
	}
	leftCounts := make(map[string]int, len(leftTokens))
	for _, token := range leftTokens {
		leftCounts[token]++
	}
	intersection := 0
	for _, token := range rightTokens {
		if leftCounts[token] > 0 {
			intersection++
			leftCounts[token]--
		}
	}
	return float64(2*intersection) / float64(len(leftTokens)+len(rightTokens))
}

func firstString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func firstStrings(values ...[]string) []string {
	for _, value := range values {
		if cleaned := cleanStrings(value); len(cleaned) > 0 {
			return cleaned
		}
	}
	return []string{}
}

func uniqueStrings(groups ...[]string) []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, value := range cleanStrings(group) {
			key := strings.ToLower(value)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func genresFromSubjects(subjects []string) []string {
	genres := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		subject = strings.TrimSpace(subject)
		if subject == "" {
			continue
		}
		// Provider categories commonly use slash-delimited taxonomy paths.
		for _, part := range strings.Split(subject, "/") {
			if part = strings.TrimSpace(part); part != "" &&
				!strings.EqualFold(part, "fiction") &&
				!strings.EqualFold(part, "general") {
				genres = append(genres, part)
			}
		}
	}
	return genres
}

func normalizeGenres(genres []string) []string {
	aliases := map[string]string{
		"sci fi":                  "Science Fiction",
		"science fiction":         "Science Fiction",
		"sf":                      "Science Fiction",
		"detective fiction":       "Mystery",
		"crime fiction":           "Mystery",
		"biography autobiography": "Biography",
	}
	result := make([]string, 0, len(genres))
	seen := make(map[string]struct{})
	for _, genre := range cleanStrings(genres) {
		normalizedKey := normalizeText(genre, false)
		if alias, exists := aliases[normalizedKey]; exists {
			genre = alias
		}
		key := strings.ToLower(genre)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, genre)
	}
	return result
}

func roundScore(score float64) float64 {
	return math.Round(score*1e9) / 1e9
}
