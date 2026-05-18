# Token-efficiency benchmark

Reproducible 3-pipeline comparison of how many tokens an agent has
to ingest to find the same information, plus recall@k by token
budget. Pipelines:

1. **ripgrep + full-read** — `rg --files-with-matches <pattern>`
   followed by reading every hit file completely. The naive
   shell-script baseline: cheap to set up, expensive to consume.
2. **ripgrep + context** — `rg -n -B 50 -A 50 <pattern>`. Captures
   the surrounding ±50 lines per hit, which is what a more
   thoughtful agent might do.
3. **gortex** — `search_symbols` → `get_symbol_source` on the top-K
   results. Matches the path the savings dashboard rewards.

For each query, the harness counts the tiktoken bytes the pipeline
returns and computes recall@2k and recall@10k against a hand-curated
ground-truth set (per-query expected file paths). The headline
median is honest about query mix — NL queries that don't appear
verbatim in code show no ripgrep matches and therefore inflate
gortex's relative cost. The per-row data shows the real picture:
gortex achieves 1.00 recall@2k on identifier queries where ripgrep
gets 0.00, because the file the agent actually wants doesn't fit in
2k tokens when read whole.

## Running

```sh
# Default: against the gortex repo itself
go run ./bench/token-efficiency

# Against a different corpus
go run ./bench/token-efficiency --repo ~/code/myrepo \
    --queries my-queries.json --groundtruth my-truth.json

# CI gate: gortex median tokens must be <50% of ripgrep+full-read
go run ./bench/token-efficiency --strict --budget-ratio 0.5

# JSON output for downstream tooling
go run ./bench/token-efficiency --format json --json bench/results/tokens-eff.json
```

Flags:

- `-repo PATH` — indexed corpus (default `.`)
- `-queries PATH` — JSON query set (default `queries.json` in this
  directory)
- `-groundtruth PATH` — JSON per-query expected file paths (default
  `groundtruth.json`)
- `-top-k N` — gortex pipeline candidate count (default 5)
- `-out PATH` — markdown output (default stdout)
- `-json PATH` — companion JSON metrics output
- `-format markdown|json` — primary output format
- `-budget-ratio R` — fail when gortex median tokens > R × ripgrep
  full-read median (default 0.5; 0 disables)
- `-strict` — exit 1 on budget violation
- `-skip-ripgrep` — render only the gortex column (useful in CI
  without rg on PATH)

## Extending the ground truth

Each entry in `groundtruth.json` is a query → expected file paths
map. The recall computation counts a query as "answered" when the
pipeline returns any of the expected files within the token budget.
Add new entries by appending to the `queries` map; the harness
picks them up on the next run.

Curation rules:
- Expected paths are repo-root-relative (no leading `./`)
- A query with no entry in `groundtruth.json` scores 0 on recall by
  definition — keep the query set and the truth set in sync
- Verify entries by running `gortex search_symbols <query>` against
  the corpus and confirming the top result is in the expected list
