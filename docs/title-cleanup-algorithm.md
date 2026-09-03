# Title cleanup and identification

The server retains source tags and generates bounded search alternatives. It confirms a work only after matching title and author evidence, or independently supplied provider series and position. A rule suggests searches; it does not prove identity by itself.

1. Preserve the original title. Remove explicit catalog prefixes and edition labels in alternatives, retaining meaningful numbers such as 1984 and Catch-22.
2. Extract series and order before removing series text: bracketed series labels, Book/Volume/Novella, trailing (#n), decimal positions, and canonical Roman numerals I–XX.
3. Strip an author prefix only when its normal or reversed name agrees with recorded authors or folder evidence. Query author-folder variants when an embedded artist may be a narrator.
4. Parse nested author/series/title folder labels only when the dirty title agrees with that folder. Use the innermost explicit series and position; never interpret a space as a decimal point without the intact folder evidence.
5. For a series-only title, search series plus author and author bibliographies. A full-title folder can add a search query, but automatic selection still requires independent matching provider series and order unless an explicit matching series/number folder already corroborates it.
6. Compare exact normalized title alternatives and corroborated authors. Do not automatically choose a sequel, a same-title work by another author, or the first author search result. Preserve ambiguous choices for review.
7. Group chapter files separately from metadata matching. A complete unique 1..N sequence must agree with the enclosing book title; dated author-prefixed tracks also require the same-folder author context. Mixed books, gaps, conflicting embedded identities, and M4B books retain the existing rejection rules.
8. Process current available books with audio. Retired historical IDs are not current unresolved books. Background matching uses the same parser as API matching.

## Corpus

`server/internal/metadata/testdata/user_title_cases_144_207.json` contains all 64 supplied entries. The two entries named only “Skyward Series” are deliberately ambiguous. The 62 positive tests verify matching when the required correct provider response exists; they do not claim every provider currently has that response. Sunreach requires provider series/order evidence. The Andromeda tests use the verified six actual filename patterns.

`TitleCleanupInterpretations` exposes the additional title, series, position, rule name, and whether an interpretation is search-only. Source tags, filenames, and manual grouping choices are retained.

## Examples

| Input | Search title | Series/order evidence |
|---|---|---|
| Mort (#4) | Mort | Discworld folder, 4 |
| The Dark Tower VI: Song Of Susannah | Song Of Susannah | The Dark Tower, 6 |
| Michael Crichton - Sphere | Sphere | Michael Crichton author folder |
| Skyward: The Skyward Series, Book 1 (USA) | Skyward | The Skyward Series, 1 |
| Sanderson, Brandon / Cosmere 5 / Mistborn 3.5 / Secret History | Secret History; Mistborn: Secret History | Intact matching folder, Mistborn 3.5 |
| Skyward Flight: Novella 1 | Series search; Sunreach folder hint | Provider must independently confirm Skyward Flight, 1 |
