package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/zzet/gortex/internal/agents"
)

// resolveFilePath turns a user-supplied path into the absolute filesystem
// path the write should target. Accepts:
//   - absolute paths, used as-is
//   - repo-prefixed paths (e.g. "gortex/internal/foo.go" in multi-repo mode)
//   - paths relative to the single indexer's root
//   - paths relative to the process CWD (last-resort fallback)
//
// Returns the absolute path and the repo-relative form suitable for
// session bookkeeping (or the absolute path again when no repo matches).
func (s *Server) resolveFilePath(rawPath string) (absPath, relPath string) {
	if rawPath == "" {
		return "", ""
	}

	if filepath.IsAbs(rawPath) {
		abs := filepath.Clean(rawPath)
		return abs, s.repoRelative(abs)
	}

	if s.multiIndexer != nil {
		if p := s.multiIndexer.ResolveFilePath(rawPath); p != "" {
			return filepath.Clean(p), rawPath
		}
	}

	if s.indexer != nil {
		if root := s.indexer.RootPath(); root != "" {
			abs := filepath.Clean(filepath.Join(root, rawPath))
			return abs, rawPath
		}
	}

	abs, err := filepath.Abs(rawPath)
	if err != nil {
		abs = filepath.Clean(rawPath)
	}
	return abs, s.repoRelative(abs)
}

// repoRelative converts an absolute path to a repo-prefixed or root-relative
// string if it falls under any indexed repo, otherwise returns the absolute
// path unchanged.
func (s *Server) repoRelative(absPath string) string {
	if s.multiIndexer != nil {
		if prefix := s.multiIndexer.RepoForFile(absPath); prefix != "" {
			if idx := s.multiIndexer.GetIndexer(prefix); idx != nil {
				if rel, err := filepath.Rel(idx.RootPath(), absPath); err == nil {
					return filepath.ToSlash(filepath.Join(prefix, rel))
				}
			}
			return prefix
		}
	}
	if s.indexer != nil {
		if root := s.indexer.RootPath(); root != "" {
			if rel, err := filepath.Rel(root, absPath); err == nil && !strings.HasPrefix(rel, "..") {
				return filepath.ToSlash(rel)
			}
		}
	}
	return absPath
}

// reindexFile refreshes the graph for a single file after a write. Best-effort:
// non-source files or files outside any indexed repo are silently skipped.
func (s *Server) reindexFile(absPath string) bool {
	if s.multiIndexer != nil {
		if prefix := s.multiIndexer.RepoForFile(absPath); prefix != "" {
			if idx := s.multiIndexer.GetIndexer(prefix); idx != nil {
				if err := idx.IndexFile(absPath); err == nil {
					return true
				}
			}
		}
	}
	if s.indexer != nil {
		if root := s.indexer.RootPath(); root != "" {
			if rel, err := filepath.Rel(root, absPath); err == nil && !strings.HasPrefix(rel, "..") {
				if err := s.indexer.IndexFile(absPath); err == nil {
					return true
				}
			}
		}
	}
	return false
}

func (s *Server) handleEditFile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	rawPath, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError("path is required"), nil
	}
	oldString, err := req.RequireString("old_string")
	if err != nil {
		return mcp.NewToolResultError("old_string is required"), nil
	}
	newString, err := req.RequireString("new_string")
	if err != nil {
		return mcp.NewToolResultError("new_string is required"), nil
	}
	if oldString == newString {
		return mcp.NewToolResultError("old_string and new_string are identical"), nil
	}
	replaceAll := req.GetBool("replace_all", false)

	absPath, relPath := s.resolveFilePath(rawPath)
	if absPath == "" {
		return mcp.NewToolResultError("could not resolve path"), nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("could not read file: %v", err)), nil
	}
	fileStr := string(content)

	count := strings.Count(fileStr, oldString)
	if count == 0 {
		return mcp.NewToolResultError(
			"old_string not found in file. Use get_file_summary or get_editing_context to inspect the current content."), nil
	}
	if count > 1 && !replaceAll {
		return mcp.NewToolResultError(fmt.Sprintf(
			"old_string matches %d locations. Provide a larger fragment for uniqueness or pass replace_all=true.", count)), nil
	}

	var newContent string
	var replacements int
	if replaceAll {
		newContent = strings.ReplaceAll(fileStr, oldString, newString)
		replacements = count
	} else {
		newContent = strings.Replace(fileStr, oldString, newString, 1)
		replacements = 1
	}

	perm := os.FileMode(0o644)
	if info, err := os.Stat(absPath); err == nil {
		perm = info.Mode().Perm()
	}
	if err := agents.AtomicWriteFile(absPath, []byte(newContent), perm); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("could not write file: %v", err)), nil
	}

	sess := s.sessionFor(ctx)
	sess.recordModified(relPath)

	reindexed := s.reindexFile(absPath)

	return mcp.NewToolResultJSON(map[string]any{
		"path":          relPath,
		"status":        "applied",
		"replacements":  replacements,
		"bytes_written": len(newContent),
		"reindexed":     reindexed,
	})
}

func (s *Server) handleWriteFile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	rawPath, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError("path is required"), nil
	}
	content, err := req.RequireString("content")
	if err != nil {
		return mcp.NewToolResultError("content is required"), nil
	}

	absPath, relPath := s.resolveFilePath(rawPath)
	if absPath == "" {
		return mcp.NewToolResultError("could not resolve path"), nil
	}

	status := "created"
	perm := os.FileMode(0o644)
	if info, err := os.Stat(absPath); err == nil {
		status = "overwritten"
		perm = info.Mode().Perm()
	}

	if err := agents.AtomicWriteFile(absPath, []byte(content), perm); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("could not write file: %v", err)), nil
	}

	sess := s.sessionFor(ctx)
	sess.recordModified(relPath)

	reindexed := s.reindexFile(absPath)

	return mcp.NewToolResultJSON(map[string]any{
		"path":          relPath,
		"status":        status,
		"bytes_written": len(content),
		"reindexed":     reindexed,
	})
}
