package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderBadgeSVG_ValidAndContainsGrade(t *testing.T) {
	for _, grade := range []string{"A", "B", "C", "D", "F"} {
		r := auditReport{Grade: grade, MeanScore: 75.5, SymbolCount: 100}
		svg := renderBadgeSVG(r)
		if !strings.HasPrefix(svg, "<svg ") {
			t.Errorf("badge for grade %q doesn't start with <svg: %q", grade, svg[:50])
		}
		if !strings.Contains(svg, ">"+grade+"</text>") {
			t.Errorf("badge for grade %q doesn't surface the grade letter: %s", grade, svg)
		}
		if !strings.Contains(svg, "gortex audit") {
			t.Errorf("badge for grade %q missing label: %s", grade, svg)
		}
	}
}

func TestRenderBadgeSVG_GradeColours(t *testing.T) {
	cases := map[string]string{
		"A": "#4c1",
		"B": "#97ca00",
		"C": "#dfb317",
		"D": "#fe7d37",
		"F": "#e05d44",
	}
	for grade, colour := range cases {
		svg := renderBadgeSVG(auditReport{Grade: grade, MeanScore: 50})
		if !strings.Contains(svg, colour) {
			t.Errorf("grade %q badge missing colour %q", grade, colour)
		}
	}
}

func TestGradeColour(t *testing.T) {
	if gradeColour("A") != "#4c1" {
		t.Errorf("A colour wrong")
	}
	// Unknown grade falls through to red — the safe default for a
	// missing / corrupt grade.
	if gradeColour("Z") != "#e05d44" {
		t.Errorf("unknown grade should fall through to red")
	}
}

func TestRenderAuditJSON_RoundTrip(t *testing.T) {
	r := auditReport{
		SymbolCount:  42,
		MeanScore:    72.3,
		Grade:        "B",
		GradeCounts:  map[string]int{"A": 10, "B": 20, "C": 5, "D": 5, "F": 2},
		WorstSymbols: []symbolScore{{ID: "f.go::Bad", Score: 12.5, Grade: "F", File: "f.go", Line: 99}},
	}
	body := renderAuditJSON(r)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("render produced invalid JSON: %v\n%s", err, body)
	}
	if got["grade"] != "B" || got["symbol_count"].(float64) != 42 {
		t.Errorf("round-trip lost data: %+v", got)
	}
	worst, ok := got["worst_symbols"].([]any)
	if !ok || len(worst) != 1 {
		t.Errorf("worst_symbols wrong shape: %+v", got["worst_symbols"])
	}
}

func TestAuditCmd_Registered(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "audit" {
			found = true
			break
		}
	}
	if !found {
		t.Error("rootCmd missing `audit` subcommand")
	}
}
