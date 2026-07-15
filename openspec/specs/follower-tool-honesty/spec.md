# follower-tool-honesty Specification

## Purpose
TBD - created by archiving change fix-follower-correctness-and-docs-search. Update Purpose after archive.
## Requirements
### Requirement: detect_changes errors without a working tree
On a daemon without a git working tree (follow mode / diskless), `detect_changes` SHALL fail with the `follow_no_disk:` error used by `review` and `review_pack`. It SHALL NOT return an empty changeset ("no indexed symbols affected", `risk: NONE`) that misrepresents the absence of a tree as the absence of changes.

#### Scenario: Follower rejects detect_changes
- **WHEN** `detect_changes` is called on a follow-mode daemon with no working tree
- **THEN** the call errors with a `follow_no_disk:` message naming the tool, and no changeset payload is returned

### Requirement: list_repos reports graph repos in unbound mode
When the daemon serves a multi-repo store without a bound workspace, `list_repos` SHALL enumerate the repository prefixes present in the graph (name and, when available, tracked branch) instead of returning `{"mode":"unbound","repos":[]}`.

#### Scenario: Follower lists indexed repos
- **WHEN** `list_repos` is called on a follow-mode daemon whose store contains repositories
- **THEN** the response lists each indexed repository prefix, and `mode` reflects the unbound/follower serving mode

