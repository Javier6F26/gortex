package mcp

import (
	"context"
	"encoding/json"
	"math"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/zzet/gortex/internal/graph"
)

// AuditReport is the per-repo complexity-axis health summary backing
// `gortex audit` and its README badge. Exported so the CLI can decode it.
type AuditReport struct {
	SymbolCount  int                `json:"symbol_count"`
	MeanScore    float64            `json:"mean_score"`
	Grade        string             `json:"grade"`
	GradeCounts  map[string]int     `json:"grade_counts"`
	WorstSymbols []AuditSymbolScore `json:"worst_symbols,omitempty"`
}

// AuditSymbolScore is one callable symbol's complexity-axis score.
type AuditSymbolScore struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
	Grade string  `json:"grade"`
	File  string  `json:"file"`
	Line  int     `json:"line"`
}

func (s *Server) registerAuditTool() {
	s.addTool(
		mcp.NewTool("audit_health",
			mcp.WithDescription("Compute a repo-level complexity-axis health grade (A-F) from the graph: per-callable fan-in/fan-out health, the mean grade, the per-grade distribution, and the worst-scored symbols. Backs `gortex audit` and its README badge."),
		),
		s.handleAuditHealth,
	)
}

func (s *Server) handleAuditHealth(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	g := s.graph
	if g == nil {
		return mcp.NewToolResultError("audit: graph is not initialised"), nil
	}
	data, err := json.Marshal(ComputeAuditReport(g))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// ComputeAuditReport walks the graph and produces the repo grade.
// Complexity-axis-only math matching the health_score analyzer's
// complexity component:
//
//	raw               = fan_in*2 + fan_out*1.5   (per callable symbol)
//	complexity_health = 100 / (1 + raw/20)
//	mean              = mean across callable symbols
//	grade             = auditScoreGrade(mean)
func ComputeAuditReport(g graph.Store) AuditReport {
	type entry struct {
		id, file string
		line     int
		score    float64
	}
	var entries []entry
	for _, n := range g.AllNodes() {
		if n == nil {
			continue
		}
		if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
			continue
		}
		fanIn := 0
		fanOut := 0
		for _, e := range g.GetInEdges(n.ID) {
			if e.Kind == graph.EdgeCalls || e.Kind == graph.EdgeReferences {
				fanIn++
			}
		}
		for _, e := range g.GetOutEdges(n.ID) {
			if e.Kind == graph.EdgeCalls {
				fanOut++
			}
		}
		raw := float64(fanIn)*2.0 + float64(fanOut)*1.5
		complexity := 100.0 / (1.0 + raw/20.0)
		entries = append(entries, entry{id: n.ID, file: n.FilePath, line: n.StartLine, score: complexity})
	}

	report := AuditReport{SymbolCount: len(entries), GradeCounts: map[string]int{}}
	if len(entries) == 0 {
		report.Grade = auditScoreGrade(0)
		return report
	}

	var sum float64
	for _, e := range entries {
		sum += e.score
	}
	report.MeanScore = math.Round((sum/float64(len(entries)))*10) / 10
	report.Grade = auditScoreGrade(report.MeanScore)
	for _, e := range entries {
		report.GradeCounts[auditScoreGrade(e.score)]++
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].score != entries[j].score {
			return entries[i].score < entries[j].score
		}
		if entries[i].file != entries[j].file {
			return entries[i].file < entries[j].file
		}
		return entries[i].line < entries[j].line
	})
	limit := min(5, len(entries))
	report.WorstSymbols = make([]AuditSymbolScore, 0, limit)
	for i := range limit {
		e := entries[i]
		report.WorstSymbols = append(report.WorstSymbols, AuditSymbolScore{
			ID:    e.id,
			Score: math.Round(e.score*10) / 10,
			Grade: auditScoreGrade(e.score),
			File:  e.file,
			Line:  e.line,
		})
	}
	return report
}

// auditScoreGrade mirrors the health_score analyzer's scoreGrade.
func auditScoreGrade(score float64) string {
	switch {
	case score >= 85:
		return "A"
	case score >= 70:
		return "B"
	case score >= 55:
		return "C"
	case score >= 40:
		return "D"
	default:
		return "F"
	}
}
