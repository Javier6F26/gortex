# concept-query-ranking Specification

## Purpose
TBD - created by archiving change fix-follower-correctness-and-docs-search. Update Purpose after archive.
## Requirements
### Requirement: Concept queries suppress structural noise
For queries classified as `concept`, the search pipeline SHALL exclude or rank strictly below all substantive hits the following node kinds: `param`, `generic_param`, and `module` nodes originating from dependency lockfiles (e.g. `package-lock.json`). Substantive kinds (function, method, type, class, doc, file) SHALL fill the result head.

#### Scenario: Params do not crowd out symbols
- **WHEN** a concept-class query matches both function/method symbols and `param`/`generic_param` nodes
- **THEN** the returned head contains the function/method symbols, with param-kind nodes excluded or trailing

#### Scenario: Lockfile modules do not surface
- **WHEN** a concept-class query lexically matches `module` nodes from a dependency lockfile and no explicit `kind=module` filter is set
- **THEN** those lockfile module nodes are excluded from or ranked below substantive results

