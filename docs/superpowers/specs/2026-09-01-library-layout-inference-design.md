# Library Layout Inference Design

## Goal

BookBox will infer audiobook identity and grouping from folder names, filenames, file order, and embedded tags. Runtime inference will remain deterministic. A development-only LLM benchmark will measure the algorithm against the observed library layouts.

## Observed Library Shape

The development library contains 1,380 folders and 22,419 audio files across 1,121 audio-containing folders. It includes single-file books, multipart recordings, generic track names, shared folders, disc subfolders, and nesting up to six levels. The engine therefore cannot treat every leaf folder as one audiobook or every file as a separate audiobook.

## Evidence Model

The engine will normalize evidence without discarding the original display text. It will extract:

- embedded title, album, author, album artist, series, track, and disc values;
- meaningful folder segments after removing container and disc labels;
- filename stems after removing extensions, track/disc prefixes, codec labels, and release noise;
- common meaningful filename prefixes and numbering sequences;
- sibling layouts, parent-child relationships, duration totals, and nearby cover candidates.

It will generate competing audiobook hypotheses before selecting an assignment. Each hypothesis will contain assigned audio files, inferred title, optional author and series, series position, positive evidence, contradictions, score, and confidence class.

## Selection and Confidence

Strong independent evidence will include consistent embedded album/title values, consistent author identity, ISBN, or agreement between a descriptive folder and cleaned filename prefix. Supporting evidence will include sequence continuity, disc continuation, series-shaped ancestors, duration consistency, and cover-name agreement.

Hard contradictions will include conflicting non-empty album identities, conflicting author identities, overlapping file assignments, and distinct descriptive filename clusters. Softer penalties will cover numbering restarts, weak generic names, and distant ancestor-only agreement.

The selector will use these confidence classes:

- `high`: score at least 80, at least two independent strong evidence sources, lead of at least 15 over the next hypothesis, and no hard contradiction;
- `review`: score from 55 through 79, a smaller lead, or one unresolved contradiction;
- `weak`: score below 55 or evidence limited to generic numbering and location.

High-confidence hypotheses may publish inferred grouping. Review hypotheses will publish with `needs_review` and full evidence. Weak layouts will use conservative physical boundaries and remain `needs_review`; BookBox will not invent an author, series, or provider query from weak evidence.

## Layout Strategies

The candidate generator will support dedicated book folders, author/book nesting, author/series/book nesting, disc subfolders, loose single-file books, multipart filename clusters, shared author folders, omnibus folders, and mixed descriptive/generic filenames. Strategy order will not decide the result; all applicable strategies will contribute hypotheses to the same scorer.

## Development Benchmark

A development command will export source-relative structural evidence without absolute paths or audio contents. The benchmark will store reviewed expected hypotheses separately from production code. LLM-assisted review will label representative cases and every algorithmically ambiguous case. Human corrections will override LLM suggestions.

The evaluation report will include file-assignment precision and recall, exact grouping accuracy, title/author/series field accuracy, confidence calibration, and a private list of disagreements. CI fixtures will use synthetic or anonymized cases derived from the failure patterns; CI will not contain private library paths.

## Test Coverage

Tests will cover each layout strategy, Unicode and punctuation, numeric titles, series positions including decimals, disc continuation, shared folders, generic tracks, mixed metadata quality, contradictions, deterministic ordering, stable hypotheses, confidence thresholds, and benchmark metric calculation.

