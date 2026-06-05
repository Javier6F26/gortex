package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/daemon"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/progress"
	"github.com/zzet/gortex/internal/tui"
)

var (
	trackName       string
	trackAsWorktree bool
)

var trackCmd = &cobra.Command{
	Use:   "track <path>",
	Short: "Add a repository to the tracked workspace",
	Long:  "Resolves the path to absolute, validates it exists, and adds it to the global config.",
	Args:  cobra.ExactArgs(1),
	RunE:  runTrack,
}

var untrackCmd = &cobra.Command{
	Use:   "untrack <path>",
	Short: "Remove a repository from the tracked workspace",
	Long:  "Resolves the path and removes the matching entry from the global config.",
	Args:  cobra.ExactArgs(1),
	RunE:  runUntrack,
}

func init() {
	trackCmd.Flags().StringVar(&trackName, "name", "",
		"Explicit repo prefix override (default: directory basename)")
	trackCmd.Flags().BoolVar(&trackAsWorktree, "as-worktree", false,
		"Track a linked git worktree as an independent instance even when its repo is already tracked elsewhere")
	rootCmd.AddCommand(trackCmd)
	rootCmd.AddCommand(untrackCmd)
}

// declaredWorkspace reads the `workspace:` slug from a repo's own
// `.gortex.yaml`, or "" when the file is absent or declares none. Used by
// the daemon-less track path to derive a stable worktree-instance prefix
// the same way the daemon would.
func declaredWorkspace(repoPath string) string {
	cfgFile := filepath.Join(repoPath, ".gortex.yaml")
	if _, err := os.Stat(cfgFile); err != nil {
		return ""
	}
	cfg, err := config.Load(cfgFile)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Workspace
}

func runTrack(cmd *cobra.Command, args []string) error {
	rawPath := args[0]
	w := cmd.ErrOrStderr()

	// Resolve to absolute path.
	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		return fmt.Errorf("resolving path %s: %w", rawPath, err)
	}

	// Validate path exists and is a directory.
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("path does not exist: %s", absPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", absPath)
	}

	emitTrackBanner(w, absPath, daemon.IsRunning())

	// Daemon-first: if a daemon is running, it's the source of truth for
	// tracked repos and it'll index immediately. Falls through to the
	// config-only behavior below when no daemon is listening.
	if daemon.IsRunning() {
		c, err := daemon.Dial(daemon.Handshake{Mode: daemon.ModeControl, ClientName: "cli"})
		if err == nil {
			defer func() { _ = c.Close() }()
			resp, ctlErr := c.Control(daemon.ControlTrack, daemon.TrackParams{
				Path:       absPath,
				Name:       trackName,
				AsWorktree: trackAsWorktree,
			})
			if ctlErr != nil {
				return ctlErr
			}
			if !resp.OK {
				return fmt.Errorf("track rejected: %s %s", resp.ErrorCode, resp.ErrorMsg)
			}
			tr := trackResult{viaDaemon: true}
			var meta struct {
				Prefix string `json:"prefix"`
				Status string `json:"status"`
			}
			if len(resp.Result) > 0 && json.Unmarshal(resp.Result, &meta) == nil {
				tr.prefix = meta.Prefix
				tr.alreadyTracked = meta.Status == "already_tracked"
			}
			emitTrackSummary(w, absPath, tr)
			return nil
		}
	}

	// Standalone fallback: update the config file directly. The daemon
	// (if later started) will pick this up on its next startup.
	gc, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("loading global config: %w", err)
	}

	for _, existing := range gc.Repos {
		existingAbs, _ := filepath.Abs(existing.Path)
		if existingAbs == absPath {
			emitTrackSummary(w, absPath, trackResult{alreadyTracked: true, repoCount: len(gc.Repos)})
			return nil
		}
	}

	entry := config.RepoEntry{Path: absPath}
	switch {
	case trackName != "":
		entry.Name = trackName
	case trackAsWorktree:
		// The AsWorktree flag is not persisted, so pin the derived
		// instance prefix as the entry Name now. The daemon reproduces it
		// intrinsically for a declared-workspace worktree, but a forced
		// (branch-tagged) instance must be recorded here to survive.
		base := config.ResolvePrefix(entry)
		if name, sep := indexer.WorktreeInstanceName(absPath, base, declaredWorkspace(absPath), true); sep {
			entry.Name = name
		}
	}
	if err := gc.AddRepo(entry); err != nil {
		return err
	}
	if err := gc.Save(); err != nil {
		return fmt.Errorf("saving global config: %w", err)
	}
	emitTrackSummary(w, absPath, trackResult{configOnly: true, repoCount: len(gc.Repos) + 1, prefix: config.ResolvePrefix(entry)})
	return nil
}

// trackResult bundles the outcome of runTrack so emitTrackSummary can pick the
// right summary card variant without re-deriving facts from the call sites.
type trackResult struct {
	viaDaemon      bool
	configOnly     bool
	alreadyTracked bool
	repoCount      int    // tracked repo count *after* this call (configOnly path)
	prefix         string // the prefix the repo was registered under (may differ from basename for worktree instances)
}

// emitTrackBanner prints the gortex mesh banner + subtitle indicating which
// path will be tracked and whether a daemon will pick it up immediately. Only
// emitted when stderr is a TTY — non-TTY runs (CI scripts) stay quiet so
// existing piped output still parses.
func emitTrackBanner(w io.Writer, absPath string, daemonUp bool) {
	if !progress.IsTTY(w) {
		return
	}
	sub := "Adding repository to the workspace."
	if daemonUp {
		sub = "Adding repository — daemon is up, indexing will start immediately."
	}
	banner := tui.Banner{
		Title:    "gortex track",
		Subtitle: sub,
	}.Render()
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, banner)
	_, _ = fmt.Fprintln(w, "  "+progress.Row("path", absPath, 6))
	_, _ = fmt.Fprintln(w)
}

// emitTrackSummary prints the post-track summary card. Three variants: via
// daemon (indexing is live), config-only (daemon will pick it up later), or
// already tracked (idempotent no-op).
func emitTrackSummary(w io.Writer, absPath string, r trackResult) {
	if !progress.IsTTY(w) {
		// Preserve the legacy one-line output for non-TTY callers so
		// scripts that grep this line keep working.
		suffix := ""
		if r.prefix != "" && r.prefix != filepath.Base(absPath) {
			suffix = fmt.Sprintf(" as %q", r.prefix)
		}
		switch {
		case r.viaDaemon:
			_, _ = fmt.Fprintf(w, "[gortex] tracked %s%s (via daemon)\n", absPath, suffix)
		case r.alreadyTracked:
			_, _ = fmt.Fprintf(w, "[gortex] already tracked: %s\n", absPath)
		case r.configOnly:
			_, _ = fmt.Fprintf(w, "[gortex] tracked %s%s (config only — start daemon to index)\n", absPath, suffix)
		}
		return
	}

	var stats []string
	if r.prefix != "" && r.prefix != filepath.Base(absPath) {
		stats = append(stats, progress.Stat("prefix", r.prefix, progress.StatGood))
	}
	switch {
	case r.viaDaemon:
		stats = append(stats, progress.Stat("via daemon", "", progress.StatGood))
		stats = append(stats, progress.Stat("indexing", "live", progress.StatGood))
	case r.alreadyTracked:
		stats = append(stats, progress.Stat("already", "tracked", progress.StatNeutral))
		if r.repoCount > 0 {
			stats = append(stats, progress.Stat(strconv.Itoa(r.repoCount), "tracked repos", progress.StatNeutral))
		}
	case r.configOnly:
		stats = append(stats, progress.Stat("written to", "global config", progress.StatGood))
		stats = append(stats, progress.Stat("daemon", "offline — start to index", progress.StatWarn))
	}

	_, _ = fmt.Fprintln(w, "  "+progress.StyleOK.Render("✓")+"  "+progress.StyleStrong.Render(absPath))
	_, _ = fmt.Fprintln(w, "     "+progress.StatStrip(stats...))

	switch {
	case r.viaDaemon:
		_, _ = fmt.Fprintln(w, "\n     "+progress.Caption("watch progress: `gortex daemon status --watch`"))
	case r.configOnly:
		_, _ = fmt.Fprintln(w, "\n     "+progress.Caption("next: `gortex daemon start --detach` to index this repo"))
	}
	_, _ = fmt.Fprintln(w)
}

func runUntrack(cmd *cobra.Command, args []string) error {
	rawPath := args[0]
	w := cmd.ErrOrStderr()

	// Argument can be either a path or a repo prefix; the daemon accepts
	// both. Resolve to absolute only when it looks like a path (starts
	// with / or . or has a path separator); otherwise treat as a prefix.
	target := rawPath
	if filepath.IsAbs(rawPath) || rawPath == "." || rawPath == ".." {
		abs, err := filepath.Abs(rawPath)
		if err != nil {
			return fmt.Errorf("resolving path %s: %w", rawPath, err)
		}
		target = abs
	}

	emitUntrackBanner(w, target, daemon.IsRunning())

	if daemon.IsRunning() {
		c, err := daemon.Dial(daemon.Handshake{Mode: daemon.ModeControl, ClientName: "cli"})
		if err == nil {
			defer func() { _ = c.Close() }()
			resp, ctlErr := c.Control(daemon.ControlUntrack, daemon.UntrackParams{PathOrPrefix: target})
			if ctlErr != nil {
				return ctlErr
			}
			if !resp.OK {
				return fmt.Errorf("untrack rejected: %s %s", resp.ErrorCode, resp.ErrorMsg)
			}
			emitUntrackSummary(w, target, untrackResult{viaDaemon: true})
			return nil
		}
	}

	// Standalone fallback.
	gc, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("loading global config: %w", err)
	}
	if err := gc.RemoveRepo(target); err != nil {
		return err
	}
	if err := gc.Save(); err != nil {
		return fmt.Errorf("saving global config: %w", err)
	}
	emitUntrackSummary(w, target, untrackResult{configOnly: true, repoCount: len(gc.Repos)})
	return nil
}

// untrackResult mirrors trackResult — kept distinct so the two summaries can
// drift apart later (e.g. untrack might want to show whether the repo had
// pending edits before removal) without one breaking the other.
type untrackResult struct {
	viaDaemon  bool
	configOnly bool
	repoCount  int // tracked repo count *after* removal (configOnly path)
}

// emitUntrackBanner prints the gortex mesh banner + subtitle indicating which
// path is being untracked and where the change will land (daemon vs config).
func emitUntrackBanner(w io.Writer, target string, daemonUp bool) {
	if !progress.IsTTY(w) {
		return
	}
	sub := "Removing repository from the workspace."
	if daemonUp {
		sub = "Removing repository — daemon will drop the index immediately."
	}
	banner := tui.Banner{
		Title:    "gortex untrack",
		Subtitle: sub,
	}.Render()
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, banner)
	_, _ = fmt.Fprintln(w, "  "+progress.Row("target", target, 8))
	_, _ = fmt.Fprintln(w)
}

// emitUntrackSummary prints the post-untrack summary card. Same TTY vs.
// non-TTY split as the track sibling so script parsers keep working.
func emitUntrackSummary(w io.Writer, target string, r untrackResult) {
	if !progress.IsTTY(w) {
		switch {
		case r.viaDaemon:
			_, _ = fmt.Fprintf(w, "[gortex] untracked %s (via daemon)\n", target)
		case r.configOnly:
			_, _ = fmt.Fprintf(w, "[gortex] untracked %s (config only)\n", target)
		}
		return
	}

	var stats []string
	switch {
	case r.viaDaemon:
		stats = append(stats, progress.Stat("via daemon", "", progress.StatGood))
		stats = append(stats, progress.Stat("index", "dropped", progress.StatGood))
	case r.configOnly:
		stats = append(stats, progress.Stat("removed from", "global config", progress.StatGood))
		if r.repoCount >= 0 {
			stats = append(stats, progress.Stat(strconv.Itoa(r.repoCount), "repos remain", progress.StatNeutral))
		}
	}

	_, _ = fmt.Fprintln(w, "  "+progress.StyleOK.Render("✓")+"  "+progress.StyleStrong.Render(target))
	_, _ = fmt.Fprintln(w, "     "+progress.StatStrip(stats...))
	_, _ = fmt.Fprintln(w)
}
