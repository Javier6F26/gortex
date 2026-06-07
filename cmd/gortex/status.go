package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/daemon"
	"github.com/zzet/gortex/internal/progress"
	"github.com/zzet/gortex/internal/tui"
)

var statusIndex string

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the daemon's index status: tracked repos, node/edge counts, workspaces",
	RunE:  runStatus,
}

func init() {
	statusCmd.Flags().StringVar(&statusIndex, "index", ".", "repository path (informational; status reports the daemon's whole tracked set)")
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, _ []string) error {
	if !daemon.IsRunning() {
		return fmt.Errorf("no gortex daemon is running — start it with `gortex daemon start --detach`, then track a repo with `gortex track <path>`")
	}
	return runStatusViaDaemon(cmd)
}

// runStatusViaDaemon prints the daemon's aggregate status across every
// tracked repository.
func runStatusViaDaemon(cmd *cobra.Command) error {
	c, err := daemon.Dial(daemon.Handshake{Mode: daemon.ModeControl, ClientName: "cli"})
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	resp, err := c.Control(daemon.ControlStatus, nil)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("status rejected: %s %s", resp.ErrorCode, resp.ErrorMsg)
	}
	var st daemon.StatusResponse
	if err := json.Unmarshal(resp.Result, &st); err != nil {
		return fmt.Errorf("parse status: %w", err)
	}

	w := cmd.OutOrStdout()
	emitStatusBanner(cmd.ErrOrStderr(), "daemon view", "Aggregate status across every tracked repository.")

	if progress.IsTTY(cmd.ErrOrStderr()) && !noProgress {
		emitDaemonStatusCard(cmd.ErrOrStderr(), st)
	} else {
		_, _ = fmt.Fprintf(w, "daemon      %s (pid %d, uptime %s)\n",
			st.Version, st.PID, time.Duration(st.UptimeSeconds)*time.Second)
		_, _ = fmt.Fprintf(w, "sessions    %d\n", st.Sessions)
		if st.MemoryBytes > 0 {
			_, _ = fmt.Fprintf(w, "memory      %.1f MB\n", float64(st.MemoryBytes)/(1024*1024))
		}
	}
	if len(st.TrackedRepos) == 0 {
		_, _ = fmt.Fprintln(w, "tracked repos: (none — run `gortex track <path>` to add one)")
		return nil
	}

	// One-line workspace rollup so the workspace boundary state is visible
	// in the compact view: a single sentence when every repo is its own
	// default workspace, an explicit count when the user grouped some.
	if len(st.Workspaces) > 0 {
		multiRepoWorkspaces := 0
		for _, ws := range st.Workspaces {
			if len(ws.Repos) > 1 {
				multiRepoWorkspaces++
			}
		}
		if multiRepoWorkspaces == 0 {
			_, _ = fmt.Fprintf(w, "workspaces  %d (one per repo, default)\n", len(st.Workspaces))
		} else {
			_, _ = fmt.Fprintf(w, "workspaces  %d (%d shared, %d default singletons)\n",
				len(st.Workspaces), multiRepoWorkspaces, len(st.Workspaces)-multiRepoWorkspaces)
		}
	}

	_, _ = fmt.Fprintln(w, "tracked repos:")
	sort.Slice(st.TrackedRepos, func(i, j int) bool {
		return st.TrackedRepos[i].Prefix < st.TrackedRepos[j].Prefix
	})
	// Workspace column only appears when the user has explicit
	// declarations — otherwise every row just repeats the repo prefix.
	showWS := false
	for _, r := range st.TrackedRepos {
		if r.Workspace != "" && r.Workspace != r.Prefix {
			showWS = true
			break
		}
	}
	for _, r := range st.TrackedRepos {
		if showWS {
			ws := r.Workspace
			if r.WorkspaceProject != "" && r.WorkspaceProject != ws {
				ws = ws + "/" + r.WorkspaceProject
			}
			_, _ = fmt.Fprintf(w, "  %-24s [%-12s] %s  (%d files, %d nodes, %d edges)\n",
				r.Prefix, ws, r.Path, r.Files, r.Nodes, r.Edges)
		} else {
			_, _ = fmt.Fprintf(w, "  %-24s %s  (%d files, %d nodes, %d edges)\n",
				r.Prefix, r.Path, r.Files, r.Nodes, r.Edges)
		}
	}
	return nil
}

// emitStatusBanner prints the shared status banner on stderr (so stdout
// remains a clean key/value stream for scripts piping `gortex status`).
// TTY-only; non-TTY callers see nothing on stderr.
func emitStatusBanner(w io.Writer, mode, subtitle string) {
	if !progress.IsTTY(w) || noProgress {
		return
	}
	banner := tui.Banner{
		Title:    "gortex status — " + mode,
		Subtitle: subtitle,
	}.Render()
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, banner)
	_, _ = fmt.Fprintln(w)
}

// emitDaemonStatusCard renders the daemon view as a styled header card:
// pid, uptime, version, sessions, memory in a single stat strip.
func emitDaemonStatusCard(w io.Writer, st daemon.StatusResponse) {
	uptime := (time.Duration(st.UptimeSeconds) * time.Second).Truncate(time.Second)
	stats := []string{
		progress.Stat(st.Version, "version", progress.StatNeutral),
		progress.Stat(strconv.Itoa(st.PID), "pid", progress.StatNeutral),
		progress.Stat(uptime.String(), "uptime", progress.StatGood),
		progress.Stat(strconv.Itoa(st.Sessions), "sessions", progress.StatNeutral),
	}
	if st.MemoryBytes > 0 {
		stats = append(stats,
			progress.Stat(fmt.Sprintf("%.1f MB", float64(st.MemoryBytes)/(1024*1024)),
				"memory", progress.StatNeutral))
	}
	_, _ = fmt.Fprintln(w, "  "+progress.StyleOK.Render("●")+"  "+
		progress.StyleStrong.Render("daemon up"))
	_, _ = fmt.Fprintln(w, "     "+progress.StatStrip(stats...))
	_, _ = fmt.Fprintln(w)
}
