# docs-corpus-search Specification

## Purpose
TBD - created by archiving change fix-follower-correctness-and-docs-search. Update Purpose after archive.
## Requirements
### Requirement: Docs-corpus queries match section body text
`search_symbols` with `corpus=docs` (and `corpus=all`) SHALL match Markdown prose-section (`KindDoc`) nodes by their body text, not only by heading/slug tokens. A query whose terms appear verbatim in a section's body SHALL return that section even when no term appears in its heading. The requirement holds END-TO-END through the daemon's MCP search surface in every serving mode — including a postgres follower — not only at the store layer: the query engine's backend SHALL surface body matches so the corpus filter has hits to keep.

#### Scenario: Body-only phrase is found
- **WHEN** an indexed Markdown section's body contains `branch_track repository_url` and no heading does, and `search_symbols` is called with `corpus=docs` and that query
- **THEN** the section is returned as a hit

#### Scenario: Body match through a follow-mode daemon
- **WHEN** the same store is served by a diskless postgres follower and `search_symbols` is called over MCP with `corpus=docs` and a body-only phrase
- **THEN** the section is returned — the store-level body channel is reachable from the follower's live search path

#### Scenario: Heading matches keep working
- **WHEN** a query matches a section heading (e.g. "ramas agrupadoras")
- **THEN** the section is returned, ranked at least as high as body-only matches

#### Scenario: Repo-scoped body search
- **WHEN** `corpus=docs` is combined with `repo=<r>` and a body match exists in `<r>`
- **THEN** the match is returned under the same scope semantics as code searches

### Requirement: Stored section text preserves identifier tokens
The indexer SHALL store prose-section body text (`section_text`) with identifier-significant characters intact — underscores in particular MUST NOT be stripped (`branch_track` stays `branch_track`, never `branchtrack`) — so identifier-shaped phrases from documentation match at both the SQL and search layers.

#### Scenario: Underscored identifier survives extraction
- **WHEN** a Markdown section body contains `branch_track(repository_url, branch)` and the file is indexed
- **THEN** the stored `section_text` contains the literal tokens `branch_track` and `repository_url`, and a body search for "branch_track repository_url" returns the section

### Requirement: Tool description reflects actual matching
The `search_symbols` tool description SHALL accurately state what docs-corpus queries match. Until body matching is implemented, the description SHALL NOT claim "a prose query matches by body text".

#### Scenario: Description and behavior agree
- **WHEN** a client reads the `corpus` parameter documentation and issues a docs query
- **THEN** the observed matching scope (headings and/or body) is the one the description states

### Requirement: scope_note computed after all filters
The `scope_note` "N candidate(s) match outside it" hint SHALL count only candidates that would survive every non-scope filter of the request (corpus, kind, flavor). The note SHALL NOT suggest widening the scope when widening would still return zero results.

#### Scenario: No misleading widen hint
- **WHEN** a query with `corpus=docs` and `repo=<r>` yields zero results and the candidates outside `<r>` would also be dropped by the remaining filters
- **THEN** the response reports zero results without claiming candidates match outside the scope

