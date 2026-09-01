# Library Layout Inference Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate explainable audiobook hypotheses from paths, filenames, sequences, and tags, then validate them with a private development benchmark.

**Architecture:** Focused grouping units extract evidence, generate competing hypotheses, score them, and select a complete non-overlapping assignment. Existing `grouping.Infer` stays as the public adapter to catalog types.

**Tech Stack:** Go 1.26, scanner/grouping catalog types, JSON audit reports.

## Global Constraints

- Runtime performs no network or LLM calls.
- Weak evidence does not invent author, series, or provider queries.
- High confidence requires score 80, two strong sources, lead 15, and no hard contradiction.
- Review confidence begins at score 55.
- Private paths do not enter commits or CI.

---

### Task 1: Extract naming evidence

**Files:**
- Create: `server/internal/grouping/naming.go`
- Create: `server/internal/grouping/naming_test.go`
- Modify: `server/internal/grouping/group.go`

**Interfaces:**
- Produces `ExtractNamingEvidence(scanner.Folder) NamingEvidence`.

- [ ] **Step 1: Write failing tables for track prefixes, disc folders, release noise, Unicode, decimal series positions, numeric titles, and common stems**
- [ ] **Step 2: Run `go test ./internal/grouping -run Naming -count=1`; verify RED**
- [ ] **Step 3: Implement token-preserving normalization and sequence parsing without changing display text**
- [ ] **Step 4: Run tests; verify PASS**
- [ ] **Step 5: Commit `feat(grouping): extract layout naming evidence`**

### Task 2: Generate layout hypotheses

**Files:**
- Create: `server/internal/grouping/hypothesis.go`
- Create: `server/internal/grouping/hypothesis_test.go`

**Interfaces:**
- Produces `Hypothesis`, `Signal`, `Confidence`, and `GenerateHypotheses(scanner.Folder, NamingEvidence)`.

- [ ] **Step 1: Write failing fixtures for dedicated, author/book, author/series/book, disc, loose M4B, multipart, shared-author, and omnibus layouts**
- [ ] **Step 2: Run focused tests; verify RED**
- [ ] **Step 3: Implement each strategy as an independent producer and deduplicate by sorted file IDs plus normalized identity**
- [ ] **Step 4: Run tests; verify PASS**
- [ ] **Step 5: Commit `feat(grouping): generate audiobook hypotheses`**

### Task 3: Score and select

**Files:**
- Create: `server/internal/grouping/scoring.go`
- Create: `server/internal/grouping/scoring_test.go`
- Modify: `server/internal/grouping/evidence.go`

**Interfaces:**
- Produces `ScoreHypotheses([]Hypothesis, NamingEvidence)` and `SelectHypotheses([]Hypothesis) Selection`.

- [ ] **Step 1: Write failing threshold, margin, contradiction, overlap, and deterministic-order tests**
- [ ] **Step 2: Run focused tests; verify RED**
- [ ] **Step 3: Implement named weight constants and stable signal codes with complete audio assignment validation**
- [ ] **Step 4: Map review/weak selections to `needs_review` evidence**
- [ ] **Step 5: Run tests and commit `feat(grouping): score audiobook hypotheses`**

### Task 4: Integrate catalog inference

**Files:**
- Modify: `server/internal/grouping/group.go`
- Modify: `server/internal/grouping/group_test.go`
- Modify: `server/internal/grouping/evidence_test.go`
- Modify: `server/internal/catalog/search_projection.go`

**Interfaces:**
- Keeps `Infer(scanner.Folder) []catalog.InferredBook` stable.

- [ ] **Step 1: Write failing end-to-end tests for assignment, title, author, series, position, confidence, and evidence**
- [ ] **Step 2: Run grouping tests; verify RED**
- [ ] **Step 3: Convert selected hypotheses into inferred books with stable grouping keys**
- [ ] **Step 4: Ensure weak outputs omit author/series and provider-search eligibility**
- [ ] **Step 5: Run grouping/catalog tests and commit `feat(catalog): publish scored layout inference`**

### Task 5: Build and run the private benchmark

**Files:**
- Create: `server/cmd/bookbox-inference-audit/main.go`
- Create: `server/cmd/bookbox-inference-audit/main_test.go`
- Create: `server/internal/grouping/benchmark.go`
- Create: `server/internal/grouping/benchmark_test.go`
- Modify: `.gitignore`

**Interfaces:**
- Produces `bookbox-inference-audit -config <worker.yml> -output artifacts/private-inference`.

- [ ] **Step 1: Write failing metric tests for assignment precision/recall, exact grouping, fields, and confidence calibration**
- [ ] **Step 2: Run benchmark tests; verify RED**
- [ ] **Step 3: Implement read-only structural export and refuse unignored output locations**
- [ ] **Step 4: Run all 1,121 audio folders; LLM-review every ambiguous case and a stratified high-confidence sample**
- [ ] **Step 5: Convert corrected patterns into anonymized fixtures and commit `test(grouping): add inference benchmark`**
