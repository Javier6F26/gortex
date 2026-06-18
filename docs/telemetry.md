# Telemetry & privacy

Gortex can collect **anonymous usage statistics** — coarse, bucketed counts of *which* tools and commands run. It is **opt-in and OFF by default**: nothing is recorded, buffered, or sent until you explicitly enable it, and even when enabled nothing is transmitted unless an ingest endpoint is configured. Telemetry never sees your code, file paths, file names, symbol names, or repository names.

## Quick control

```bash
gortex telemetry status   # is it on/off, why, what is collected, the anonymous install id
gortex telemetry on       # enable anonymous tool/command counts
gortex telemetry off      # disable and delete any buffered, unsent data
```

- `telemetry on` prints `Telemetry enabled — anonymous tool/command counts only (no code, paths, or names).`
- `telemetry off` prints `Telemetry disabled — buffered data cleared.` — it deletes all buffered daily rollups and the send marker (the anonymous install id is intentionally kept).
- `telemetry status` prints the state and the precedence rung that decided it (`decided by: env | do_not_track | config | default`), the ingest endpoint (`not configured — nothing is transmitted` when unset), the anonymous install id, and a one-line summary of what is collected.

`gortex install` also offers the choice: pass `--telemetry` / `--no-telemetry`, or use the **Anonymous telemetry** toggle in the interactive wizard. A toggle takes effect on a running daemon within a few seconds (it re-reads consent on the record and send paths) — no restart needed.

## How consent is decided

Consent resolves through a fixed four-rung precedence (**highest wins**):

| Rung | Signal | Effect |
| --- | --- | --- |
| 1 | `GORTEX_TELEMETRY` | Explicit per-process override — can force **on or off**. ON values `1/true/on/yes/enable/enabled`; OFF values `0/false/off/no/disable/disabled` (case-insensitive, trimmed). Highest, so it overrides even a global `DO_NOT_TRACK` for one invocation. |
| 2 | `DO_NOT_TRACK` | The cross-tool standard ([consoledonottrack.com](https://consoledonottrack.com)). Any set value other than `0`/`false` forces **off**. Can only ever disable, never enable. |
| 3 | Saved choice | The persisted `telemetry.enabled` value from `gortex telemetry on/off`. |
| 4 | Default | **Off.** |

An unrecognised or empty value at a rung falls through to the next rung rather than being treated as a decision. To turn telemetry off everywhere, set `DO_NOT_TRACK=1` or `GORTEX_TELEMETRY=0`.

## What is collected

Exactly **four** metric keys can ever be recorded — a hard allow-list; the aggregator physically cannot record anything else:

| Key | Meaning | Dimension |
| --- | --- | --- |
| `mcp_tool_call` | an MCP tool was invoked | tool name (e.g. `search_symbols`) |
| `cli_command` | a CLI subcommand ran | dotted command path (e.g. `daemon.start`, `review`) |
| `index` | an index pass completed | file-count bucket |
| `daemon_session` | a daemon session started | backend kind (e.g. `sqlite`) |

A recorded counter is `key` or `key:dimension` (e.g. `mcp_tool_call:search_symbols`, `index:1k-10k`). Values are **bucketed**, never exact — exact counts can narrow identification:

- File counts → `<100`, `100-1k`, `1k-10k`, `10k+`
- Durations → `<10s`, `10-60s`, `1-5m`, `5m+`

A dimension guard (`^[A-Za-z0-9_.<>+-]{1,32}$`) drops any token containing a path separator, whitespace, or over 32 characters, so even a caller that mistakenly passed a path or symbol name as a dimension cannot leak it — only the bare metric key is recorded.

### Never collected

Code or source content; file paths or names; symbol or repository names; IP-derived data; hostnames, usernames, or MAC addresses; exact counts.

## The anonymous install id

A random UUIDv4 minted on first use and stored at `~/.gortex/telemetry/install-id`. It is the only stable identifier telemetry carries, derived from nothing about your machine (no hostname, user, MAC, or path) — it merely ties one machine's daily aggregates together. `gortex telemetry status` prints it.

## Where state lives

All telemetry state is under `~/.gortex/telemetry/` (`<data-dir>/telemetry`; honours XDG / data-dir overrides). It lives under the durable data dir, not the cache, so a cache wipe never resets the install id or drops unsent days. Contents:

- per-UTC-day rollup files — the buffered daily aggregates
- `install-id` — the anonymous id
- `consent.json` — the persisted choice
- `last-send` — the once-per-day send marker

## Transmission

There is **no built-in default endpoint**. With `GORTEX_TELEMETRY_ENDPOINT` unset, telemetry is aggregated locally but **never transmitted** — the whole pipeline runs end-to-end except the final POST. When the endpoint is configured, and only while consent is enabled, Gortex sends:

- a single JSON `POST` to the endpoint, 5s timeout, **no retries** (a failed send leaves the days buffered)
- **at most once per UTC machine-day**
- only **completed** UTC days — never the day still accumulating; on a successful (`2xx`) response the sent days are deleted

Payload shape:

```json
{ "install_id": "…", "schema_version": 1, "gortex_version": "…",
  "os": "darwin", "arch": "arm64", "ci": false,
  "days": [ { "day": "2026-06-18", "counts": { "cli_command:review": 3, "index:1k-10k": 1 } } ] }
```

`os`/`arch` are the Go `runtime.GOOS`/`GOARCH`; `ci` reflects whether a CI environment was detected (`CI` env var set and not `0`/`false`); `counts` holds only the allow-listed, bucketed keys.

## First-run notice

The first time you run `gortex init` (non-dry-run), Gortex prints a one-time notice to stderr and records the default (off) choice so the notice fires at most once. It never enables anything:

> Gortex can collect anonymous usage stats (tool/command counts only — no code, paths, or names). It is OFF by default; enable with `gortex telemetry on`. See `gortex telemetry status`.
