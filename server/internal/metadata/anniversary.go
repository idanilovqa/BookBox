package metadata

import (
	"regexp"
	"strings"
)

const anniversaryWords = `(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|thirty|forty|fifty|sixty|seventy|eighty|ninety|hundred|thousand|and|first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|eleventh|twelfth|thirteenth|fourteenth|fifteenth|sixteenth|seventeenth|eighteenth|nineteenth|twentieth|thirtieth|fortieth|fiftieth|sixtieth|seventieth|eightieth|ninetieth|hundredth|thousandth)`
const anniversaryNumber = `(?:[0-9]{1,4}(?:st|nd|rd|th)?|[ivxlcdm]{1,12}|` + anniversaryWords + `(?:[ -]+` + anniversaryWords + `){0,5})`
const anniversaryCore = anniversaryNumber + `(?:[ -]+year)?[ -]+anniversary`
const anniversaryModifiers = `(?:[ ,:-]+(?:special|deluxe|revised|expanded|updated|illustrated|commemorative|limited|collector['’]?s|author['’]?s|definitive|and|fully|newly))*`
const anniversaryEdition = anniversaryCore + anniversaryModifiers + `[ -]+(?:edition|ed\.?)`

var anniversaryLabels = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:\s*:\s*|\s+-\s+)` + anniversaryCore + `\s*$`),
	regexp.MustCompile(`(?i)^` + anniversaryCore + `\s*:\s*`),
	regexp.MustCompile(`(?i)\s*[\[(]\s*(?:` + anniversaryEdition + `|` + anniversaryCore + `)\s*[\])]\s*$`),
	regexp.MustCompile(`(?i)^\s*[\[(]\s*(?:` + anniversaryEdition + `|` + anniversaryCore + `)\s*[\])][ :\-]*`),
	regexp.MustCompile(`(?i)[ :\-]+` + anniversaryEdition + `\s*$`),
	regexp.MustCompile(`(?i)^` + anniversaryEdition + `\s*[:\-]\s*`),
}

// Add a work-title variant for explicit anniversary labels. Keep the original
// when the wording is itself the title, or no recognized edition label exists.
func anniversaryTitleVariant(title string) string {
	normalized := strings.NewReplacer("_", " ", "–", "-", "—", "-", "‑", "-").Replace(title)
	for _, label := range anniversaryLabels {
		if !label.MatchString(normalized) {
			continue
		}
		clean := strings.Trim(label.ReplaceAllString(normalized, ""), " ,:-\t\r\n")
		switch strings.ToLower(clean) {
		case "", "the", "a", "an":
			continue
		}
		return clean
	}
	return title
}

func workTitle(title string) string { return anniversaryTitleVariant(SearchTitle(title)) }
