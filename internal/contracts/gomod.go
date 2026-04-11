package contracts

import (
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// GoModExtractor detects go.mod dependencies and creates "dependency" contracts
// for each `require` directive. When tracked repo names are provided, it marks
// dependencies that point to other tracked repos, enabling cross-repo service
// dependency detection.
type GoModExtractor struct {
	// TrackedRepos maps known repo names to their module paths.
	// e.g., {"labrador": "github.com/FindHotel/labrador", "go-pkg": "github.com/FindHotel/go-pkg"}
	// When empty, all require directives are extracted.
	TrackedRepos map[string]string
}

func (e *GoModExtractor) SupportedLanguages() []string {
	return []string{"go"}
}

func (e *GoModExtractor) Extract(filePath string, src []byte, _ []*graph.Node, _ []*graph.Edge) []Contract {
	// Only process go.mod files
	if !strings.HasSuffix(filePath, "go.mod") {
		return nil
	}

	var result []Contract
	lines := strings.Split(string(src), "\n")
	inRequire := false

	for lineNum, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track require block
		if strings.HasPrefix(trimmed, "require (") || strings.HasPrefix(trimmed, "require(") {
			inRequire = true
			continue
		}
		if inRequire && trimmed == ")" {
			inRequire = false
			continue
		}

		// Single-line require: `require github.com/foo/bar v1.0.0`
		var modulePath, version string
		if strings.HasPrefix(trimmed, "require ") && !strings.Contains(trimmed, "(") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 3 {
				modulePath = parts[1]
				version = parts[2]
			}
		} else if inRequire {
			// Inside require block: `github.com/foo/bar v1.0.0`
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 && strings.Contains(parts[0], "/") {
				modulePath = parts[0]
				version = parts[1]
			}
		}

		if modulePath == "" {
			continue
		}

		// Skip indirect dependencies
		if strings.Contains(line, "// indirect") {
			continue
		}

		// Check if this dependency matches a tracked repo
		targetRepo := ""
		if e.TrackedRepos != nil {
			for repoName, repoModule := range e.TrackedRepos {
				// Match: module path starts with the tracked repo's module path
				// e.g., "github.com/FindHotel/go-pkg/cex" matches "github.com/FindHotel/go-pkg"
				if modulePath == repoModule || strings.HasPrefix(modulePath, repoModule+"/") {
					targetRepo = repoName
					break
				}
			}
		}

		// Extract the short name from the module path
		shortName := modulePath
		if idx := strings.LastIndex(modulePath, "/"); idx >= 0 {
			shortName = modulePath[idx+1:]
		}
		// Strip version suffix (e.g., /v2, /v4)
		if strings.HasPrefix(shortName, "v") && len(shortName) <= 3 {
			parts := strings.Split(modulePath, "/")
			if len(parts) >= 2 {
				shortName = parts[len(parts)-2]
			}
		}

		contract := Contract{
			ID:         "dep::" + modulePath,
			Type:       "dependency",
			Role:       RoleConsumer,
			FilePath:   filePath,
			Line:       lineNum + 1,
			Confidence: 1.0,
			Meta: map[string]any{
				"module":  modulePath,
				"version": version,
			},
		}

		if targetRepo != "" {
			contract.Meta["target_repo"] = targetRepo
			contract.ID = "dep::" + targetRepo + "::" + shortName
			contract.Confidence = 1.0
		}

		result = append(result, contract)
	}

	return result
}
