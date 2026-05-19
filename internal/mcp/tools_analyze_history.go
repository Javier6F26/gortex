// Analyzer that mines git history rather than the graph: fixes_history
// surfaces the files most often touched by bug-fix commits — the
// fix-prone hotspots a reviewer should treat with extra care.
package mcp

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/zzet/gortex/internal/graph"
)

// fixSubjectRe matches a commit subject that describes a bug fix:
// conventional-commit `fix:` / `fix(scope):`, a `fix` / `fixes` /
// `fixed` word, or `bugfix` / `hotfix`. Word boundaries keep "prefix"
// and "fixture" from matching.
var fixSubjectRe = regexp.MustCompile(`(?i)(^fix(\([^)]*\))?!?:|\bfix(es|ed)?\b|\bbug ?fix\b|\bhotfix\b)`)

// fixCommit is one bug-fix commit mined from git log.
type fixCommit struct {
	subject string
	files   []string
}

// mineFixCommits runs `git log` in root and returns the commits whose
// subject describes a bug fix, capped at maxScan commits scanned.
// Returns nil on any git error (not a git repo, git not installed).
func mineFixCommits(ctx context.Context, root string, maxScan int) []fixCommit {
	if maxScan <= 0 {
		maxScan = 2000
	}
	// %x00 starts each commit record with the subject on its own
	// line; --name-only appends the changed files below it.
	cmd := exec.CommandContext(ctx, "git", "-C", root, "log", "--no-merges", //nolint:gosec // root is daemon-internal
		"-n", strconv.Itoa(maxScan), "--name-only", "--format=%x00%s")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var commits []fixCommit
	for _, chunk := range strings.Split(string(out), "\x00") {
		chunk = strings.Trim(chunk, "\n")
		if chunk == "" {
			continue
		}
		lines := strings.Split(chunk, "\n")
		if !fixSubjectRe.MatchString(lines[0]) {
			continue
		}
		fc := fixCommit{subject: lines[0]}
		for _, l := range lines[1:] {
			if l = strings.TrimSpace(l); l != "" {
				fc.files = append(fc.files, l)
			}
		}
		commits = append(commits, fc)
	}
	return commits
}

// handleAnalyzeFixesHistory ranks source files by how often a bug-fix
// commit touched them, with each file's symbols and a sample of recent
// fix-commit subjects — the fix-prone hotspots worth extra review.
//
// Filters: scope (repo prefix), limit (rows), max_commits (history
// depth scanned, default 2000).
func (s *Server) handleAnalyzeFixesHistory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	scope := stringArg(args, "scope")
	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	maxCommits := 2000
	if v, ok := args["max_commits"].(float64); ok && v > 0 {
		maxCommits = int(v)
	}

	roots := s.collectRepoRoots(scope)
	fixCounts := map[string]int{}
	recent := map[string][]string{}
	totalFixCommits := 0
	for prefix, root := range roots {
		for _, c := range mineFixCommits(ctx, root, maxCommits) {
			totalFixCommits++
			for _, f := range c.files {
				key := f
				if prefix != "" {
					key = prefix + "/" + f
				}
				fixCounts[key]++
				if len(recent[key]) < 5 {
					recent[key] = append(recent[key], c.subject)
				}
			}
		}
	}

	type fixRow struct {
		File       string   `json:"file"`
		FixCommits int      `json:"fix_commits"`
		Symbols    []string `json:"symbols,omitempty"`
		Recent     []string `json:"recent,omitempty"`
	}
	rows := make([]*fixRow, 0, len(fixCounts))
	for file, n := range fixCounts {
		rows = append(rows, &fixRow{
			File:       file,
			FixCommits: n,
			Symbols:    s.symbolNamesInFile(file),
			Recent:     recent[file],
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].FixCommits != rows[j].FixCommits {
			return rows[i].FixCommits > rows[j].FixCommits
		}
		return rows[i].File < rows[j].File
	})
	truncated := false
	if len(rows) > limit {
		rows = rows[:limit]
		truncated = true
	}

	if s.isGCX(ctx, req) {
		return s.gcxResponseWithBudget(req)(encodeAnalyze("fixes_history", rows))
	}
	if isCompact(req) {
		var b strings.Builder
		for _, r := range rows {
			fmt.Fprintf(&b, "%-3d %s\n", r.FixCommits, r.File)
		}
		if truncated {
			fmt.Fprintf(&b, "... truncated to %d\n", limit)
		}
		if len(rows) == 0 {
			b.WriteString("no bug-fix history found\n")
		}
		return mcp.NewToolResultText(b.String()), nil
	}
	return s.respondJSONOrTOON(ctx, req, map[string]any{
		"files":             rows,
		"total":             len(rows),
		"total_fix_commits": totalFixCommits,
		"truncated":         truncated,
	})
}

// symbolNamesInFile returns the sorted, de-duplicated names of the
// function / method / type symbols defined in filePath.
func (s *Server) symbolNamesInFile(filePath string) []string {
	var names []string
	seen := map[string]bool{}
	for _, n := range s.graph.GetFileNodes(filePath) {
		switch n.Kind {
		case graph.KindFunction, graph.KindMethod, graph.KindType, graph.KindInterface:
			if n.Name != "" && !seen[n.Name] {
				seen[n.Name] = true
				names = append(names, n.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}
