# Onboarding — First 15 Minutes with Gortex

Just installed Gortex? This is the shortest path from "it's on my machine" to "it's helping my AI agent work faster." Follow it top-to-bottom the first time; skip sections you've already done on return visits.

## Prerequisites

- Gortex is installed (run `gortex version` — if that prints a version, you're good). Not installed? → see [README Installation](../README.md#installation).
- A repository you want to work in. We'll call it `~/projects/myapp` in the examples — substitute your own path.
- An AI coding assistant installed. Gortex auto-integrates with Claude Code, Cursor, Kiro, Windsurf, GitHub Copilot (VS Code), Continue.dev, Cline, OpenCode, and Antigravity.

## Two Commands

Setup is split into two commands so codebase-agnostic machinery lives once per user, not once per repo:

- **`gortex install`** — run **once per machine**. Writes user-level artifacts: `~/.claude.json` (MCP config), `~/.claude/skills/gortex-*` (tool-usage skills), `~/.claude/commands/gortex-*.md` (slash commands), `~/.gemini/antigravity/` Knowledge Items, and (optionally) user-level Claude Code hooks. Also sets up the daemon — pass `--start` to spawn it, `--track` to register the current directory.
- **`gortex init`** — run **once per repo**. Writes per-repo artifacts: `.mcp.json`, `.claude/settings.{json,local.json}`, `CLAUDE.md` with the codebase overview and community routing, `.claude/skills/generated/` per-community SKILL.md files, and a marker-guarded community-routing block in every other detected agent's per-repo instructions file.

You can run them independently — `gortex init` doesn't require `gortex install` first; it just writes less if the user-level wiring isn't there.

```bash
# One-time: machine-wide user-level setup
gortex install --start --track         # install, start daemon, track current dir

# Per repo: drop into the repo and wire it up
cd ~/projects/myapp
gortex init                            # default: indexes repo, generates community routing
```

For CI, scripts, or explicit control, both commands accept the usual flags (`--yes`, `--json`, `--dry-run`, `--agents`, `--agents-skip`, `--force`).

## 30-Second Version

```bash
# Once per machine
gortex install --start --track

# Once per repo
cd ~/projects/myapp
gortex init
```

With `--start`, the daemon is already running and Claude Code will find Gortex on its next run. If you skipped `--start`, you can either spawn the daemon (`gortex daemon start --detach`) or run a per-repo server (`gortex serve --index . --watch`).

Open your AI assistant in that repo and ask it to do something real. It'll use Gortex tools automatically. If that worked, the rest of this document is optional detail.

## Step-by-Step

### 1. One-time: user-level setup

```bash
gortex install                    # MCP config, skills, slash commands, KIs at ~/
gortex install --start --track    # also spawn the daemon + track current dir
gortex install --no-hooks         # skip user-level Claude Code hooks
```

This writes under `$HOME` only. It's idempotent — re-running it is safe. Think of it like `brew install`.

### 2. Per repo: wire it up

```bash
cd ~/projects/myapp
gortex init
```

`gortex init` creates tool-specific config files (auto-detecting which tools you have installed) and runs community detection on the graph so each agent gets codebase-specific routing. Commit the output — your teammates get Gortex for free when they pull.

**Key files `gortex init` creates:**

- `.mcp.json` — tells MCP clients (Claude Code, Cursor, VS Code) how to start the Gortex server
- `CLAUDE.md` — codebase overview (with `--analyze`) plus a marker-guarded community-routing block
- `.claude/settings.local.json` — installs three hooks: `PreToolUse` (redirects `Read` / `Grep` / `Glob` / `Task`), `PreCompact` (injects orientation snapshot before context compaction), `Stop` (post-task diagnostics)
- `.claude/skills/generated/<DirName>/SKILL.md` — one per detected community (via `--skills`, default on)
- `.cursor/mcp.json`, `.kiro/settings/mcp.json`, `.vscode/mcp.json`, etc. — per-agent MCP configs
- Marker-guarded "Gortex Communities" routing block in each detected agent's per-repo instructions file (`AGENTS.md`, `.windsurfrules`, `GEMINI.md`, `.cursor/rules/gortex-communities.mdc`, etc.)

**Tune the community generator:**

```bash
gortex init --analyze                         # include a richer codebase overview in CLAUDE.md
gortex init --no-skills                       # skip community generation entirely
gortex init --skills-min-size 5 --skills-max 10   # raise the floor / lower the ceiling
```

### 3. Start the MCP server

Two ways — pick whichever fits your workflow.

**Option A — you start it and leave it running.** Useful when multiple AI tools point at the same graph, or when you want the web UI:

```bash
gortex serve --index . --watch
```

`--watch` re-indexes changed files live via fsnotify. `--cache-dir ~/.cache/gortex` (default) saves snapshots between restarts so subsequent starts are ~200ms instead of 3-5s.

To also get the web UI + HTTP bridge:

```bash
gortex bridge --index . --web --watch
```

Open `http://localhost:4747` for the force-directed graph explorer.

**Option B — your IDE starts it automatically.** The `.mcp.json` that `gortex init` created tells the IDE how to spawn `gortex serve`. You don't run anything yourself. Claude Code, Cursor, and VS Code all work this way. Downside: each tool gets its own server process (memory cost scales with number of tools).

If you're unsure, start with Option A. You can always remove the `.mcp.json` → switch to Option B later.

### 4. Verify the integration

Open your AI assistant in the repo. Ask it something concrete that requires understanding the code:

> "What does the authentication flow look like? Trace it from the HTTP handler through to the database."

**What should happen:**

- The assistant calls `graph_stats` or `get_repo_outline` to orient itself
- Then `search_symbols "auth"` or `smart_context "authentication flow"` to find relevant code
- Then `get_call_chain` or `find_usages` on the specific handler
- Finally `get_symbol_source` on the specific functions — not `Read` on whole files

**What should NOT happen:**

- The assistant calls `Read` on 5 files and hunts for auth logic manually. If you see this, the hooks aren't wired up — run `gortex init --hooks-only` to reinstall just the hooks.

**Quick sanity check from the CLI:**

```bash
gortex status --index .
```

Prints node/edge counts, language breakdown, and per-repo stats. If this shows 0 nodes, the index didn't build — check for errors in `gortex serve` output.

### 5. Your first calls (if you're driving Gortex directly)

For debugging, writing custom agents, or working with the bridge HTTP API — the "good first calls" in order:

1. **`get_repo_outline`** — zero-arg narrative overview: primary languages, top communities, load-bearing hotspots, most-imported files, entry points. Takes ~1k tokens, covers "what is this repo?"
2. **`plan_turn` with your task description** — returns ranked recommended next calls. Example:
   ```json
   {"tool": "plan_turn", "args": {"task": "add rate limiting to auth handler"}}
   ```
   You get back a list like "smart_context → get_editing_context → find_usages" with pre-filled args.
3. **`smart_context` with the task** — does what `plan_turn` recommended as step 1, but assembles the actual context (relevant symbols, entry file structure, related tests) rather than just pointing at tools.
4. **Before editing any file — `get_editing_context` on its path.** Returns all symbols, signatures, direct dependencies, immediate callers. You don't need to read the file.

### 6. What the hooks do automatically

Once installed, three things happen without you lifting a finger:

- **PreToolUse on `Read` / `Grep` / `Glob`** — Gortex suggests the right graph tool instead and, for indexed source files, blocks whole-file reads.
- **PreToolUse on `Task`** — spawned subagents get a task-scoped briefing with `smart_context` results + a tool-swap table, so they don't inherit the bad habit of reaching for `Read`.
- **PreCompact** — just before Claude Code compacts the conversation, Gortex injects an orientation snapshot (recent edits, hotspots, feedback-ranked symbols) so the agent survives compaction without re-exploring.
- **Stop** — after the agent finishes a turn, Gortex runs `detect_changes` → `get_test_targets`, `check_guards`, `analyze dead_code`, `contracts check` on the symbols that changed, and feeds the results back so the agent self-corrects before handoff.

All four degrade silently when the bridge is unreachable — they never block your normal flow.

## Troubleshooting

**"Gortex MCP server failed to start" in the IDE.**
Check that `gortex` is on your `PATH` (`which gortex` should resolve). If you installed via Homebrew, restart the IDE — it caches PATH at launch.

**The AI still uses `Read` / `Grep` on source files.**
The hooks didn't install. Re-run `gortex init --hooks-only` and restart the AI tool. On Claude Code, also check that `.claude/settings.local.json` exists and contains `"gortex hook"` invocations under `hooks`.

**`graph_stats` returns `total_nodes: 0`.**
The index is empty. Either `gortex serve` isn't watching the right directory, or `.gortex.yaml` excludes everything. Run `gortex status --index /absolute/path/to/repo` to verify the paths.

**Indexing a big repo takes forever.**
First-time index of a 100k-symbol repo is ~20-30 seconds. On restart, it's ~200ms because the snapshot gets restored and only changed files re-index. Make sure `--cache-dir` isn't being deleted between runs.

**Semantic search isn't working.**
On first use, Gortex downloads the MiniLM-L6-v2 model (~90 MB) to `~/.cache/gortex/models/`. Needs network the first time; after that, fully offline. Check `~/.cache/gortex/models/sentence-transformers_all-MiniLM-L6-v2/` exists.

**"Cannot be opened because Apple cannot check it for malicious software" on macOS.**
You downloaded the binary from GitHub Releases via a browser. Either install via Homebrew (`brew install zzet/tap/gortex`) or run once: `xattr -d com.apple.quarantine /usr/local/bin/gortex`.

## Next Steps

Once the basics are working:

- **Multi-repo workspaces** — index several repos into one graph for cross-repo impact analysis. See [Multi-Repo Workspaces](../README.md#multi-repo-workspaces) in the README.
- **Guard rules** — add `.gortex.yaml` to declare architectural invariants (e.g., "UI must not import DB directly"). `check_guards` enforces them on every change. See `.gortex.yaml` in this repo for an example.
- **Per-community skills** — already generated by `gortex init --skills` (default on). Each skill auto-activates when the agent asks about that area. Re-run `gortex init` to regenerate after the graph changes; pass `--no-skills` if you want to skip that step.
- **Token savings + cost tracking** — `gortex savings` prints cumulative tokens saved + dollars avoided per model across all sessions. Accumulates automatically; no setup.
- **Compact wire format (GCX1)** — every list-shaped tool accepts `format: "gcx"` for a round-trippable compact response. Median **−27.4% tokens** vs JSON on the benchmark, 100% round-trip integrity. Spec: [docs/wire-format.md](wire-format.md). TypeScript decoder on npm: [`@gortex/wire`](https://www.npmjs.com/package/@gortex/wire). Agents pick it up automatically — the PreToolUse and subagent hooks surface the opt-in. Applies to: `search_symbols`, `find_usages`, `analyze`, `contracts`, `batch_symbols`, `get_callers` / `get_call_chain` / `get_dependencies` / `get_dependents` / `find_implementations`, `get_file_summary`, `get_editing_context`, `smart_context`.
- **Feedback loop** — after a successful task, call the `feedback` MCP tool with `action: "record"`. Future `smart_context` results rerank based on what was actually useful.
- **Custom bridge integration** — `gortex bridge --index . --cors-origin '*'` exposes every MCP tool as HTTP. Good for editor plugins, CI hooks, custom dashboards.

## Daemon Mode

The daemon is a long-living process that holds the graph for every tracked repo. All MCP clients (Claude Code windows, Cursor, Kiro, etc.) connect to it via a Unix socket, so:

- Memory scales with workspace size, not open-editor count — one process instead of one per project.
- Cross-repo queries work by default: an agent in `frontend` can find callers in `backend` without extra config.
- Each session gets isolated per-client state (recent activity, token stats) via handshake-assigned session IDs.

### Setup

```bash
# One-time: user-level MCP config, skills, slash commands, hooks, daemon spawn + track.
gortex install --start --track

# Track additional repos any time:
gortex track ~/projects/backend
gortex track ~/projects/shared-lib

# Remove a repo from the workspace:
gortex untrack backend        # by prefix, or by absolute path

# See state:
gortex status                 # tracked repos, node/edge counts, memory, sessions (via daemon if running)
gortex daemon status          # PID, uptime, socket path
gortex savings                # cumulative tokens saved + $ avoided across all sessions
```

### Daemon lifecycle

```bash
gortex daemon start --detach  # spawn in background
gortex daemon stop            # graceful shutdown + final snapshot
gortex daemon restart         # stop + start
gortex daemon reload          # re-read config, pick up new/removed repos
gortex daemon logs -n 50      # tail the log
```

### Auto-start at login (optional)

Let the OS supervise the daemon so it starts at login and restarts on crash. No sudo required — the unit lives under `$HOME`.

```bash
gortex daemon install-service   # launchd (macOS) or systemd --user (Linux)
gortex daemon service-status    # check installed state + active/inactive
gortex daemon uninstall-service # remove unit, stop service
```

On macOS the unit lands at `~/Library/LaunchAgents/com.zzet.gortex.plist`; on Linux at `~/.config/systemd/user/com.zzet.gortex.service`. After `install-service`, plain `gortex daemon start / stop` still work — they just fight the service for socket ownership, so prefer `gortex daemon service-status` and `launchctl` / `systemctl --user` commands for lifecycle.

### How it works

- `gortex serve` (what Claude Code spawns via `.mcp.json`) auto-detects the daemon. If reachable, it acts as a thin stdio ↔ socket proxy (~5 MB per client). If not, it falls back to the embedded server — global mode is never "required."
- Every tracked repo gets its own fsnotify watcher so edits on disk flow into the graph live; no manual reload needed. `gortex track` attaches a watcher as part of the track operation; `gortex untrack` detaches it before evicting nodes.
- Graph state is snapshotted to `~/.cache/gortex/daemon.gob.gz` on shutdown and every 10 minutes. Daemon restarts load it back and re-index only changed files.
- Opening Claude Code in an untracked directory returns a structured `repo_not_tracked` error on every tool call. The agent surfaces it; you run `gortex track .` to include it.
- Per-session state is isolated by a handshake-assigned session ID — two Claude Code windows see their own recent-activity and token-savings counters, not a merged view. Cumulative savings in `~/.cache/gortex/savings.json` are still shared.

### Fallback rules

| Invocation | Daemon running | Daemon not running |
|---|---|---|
| Claude Code spawns `gortex serve` | Proxies through daemon | Embedded server (current behavior) |
| `gortex track /path` | Immediate re-index + watcher attached via daemon | Writes config; takes effect on next daemon/server start |
| `gortex untrack /path` | Immediate graph eviction + watcher detached | Removes from config |
| `gortex status` | Aggregate across tracked repos | One-shot local index |
| `gortex daemon status` | PID, uptime, memory, sessions | "not running" |

Full architectural spec: [`spec-daemon.md`](../spec-daemon.md).

## Getting Help

- File issues and feature requests at [github.com/zzet/gortex/issues](https://github.com/zzet/gortex/issues).
- Full tool reference lives in `CLAUDE.md` (created by `gortex init`). Your AI agent already reads it; you can too.
