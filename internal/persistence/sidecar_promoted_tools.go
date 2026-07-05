package persistence

import (
	"fmt"
	"time"
)

// PromotedToolEntry is one workspace-learned tool promotion: a tool that
// was deferred behind tools_search but has been promoted into the eager
// surface for this repo/workspace after use. Persisted so the learned
// surface survives daemon restarts. last_used_epoch drives demotion — a
// promotion unused for N session epochs is dropped back to deferred.
type PromotedToolEntry struct {
	Tool          string
	WorkspaceID   string
	PromotedEpoch int64
	LastUsedEpoch int64
	UseCount      int64
	UpdatedAt     time.Time
}

// LoadPromotedTools reads every learned promotion for a repo_key.
func (s *SidecarStore) LoadPromotedTools(repoKey string) ([]PromotedToolEntry, error) {
	rows, err := s.db.Query(`
		SELECT tool, workspace_id, promoted_epoch, last_used_epoch, use_count, updated_at
		FROM promoted_tools WHERE repo_key = ?
		ORDER BY tool ASC`, repoKey)
	if err != nil {
		return nil, fmt.Errorf("persistence: query promoted_tools: %w", err)
	}
	defer rows.Close()
	var out []PromotedToolEntry
	for rows.Next() {
		var e PromotedToolEntry
		var updatedAt int64
		if err := rows.Scan(&e.Tool, &e.WorkspaceID, &e.PromotedEpoch,
			&e.LastUsedEpoch, &e.UseCount, &updatedAt); err != nil {
			return out, fmt.Errorf("persistence: scan promoted_tool: %w", err)
		}
		e.UpdatedAt = fromUnix(updatedAt)
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertPromotedTool writes (or replaces) one learned promotion.
func (s *SidecarStore) UpsertPromotedTool(repoKey string, e PromotedToolEntry) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO promoted_tools
		(repo_key, tool, workspace_id, promoted_epoch, last_used_epoch, use_count, updated_at)
		VALUES (?,?,?,?,?,?,?)`,
		repoKey, e.Tool, e.WorkspaceID, e.PromotedEpoch, e.LastUsedEpoch,
		e.UseCount, unixOrZero(e.UpdatedAt))
	if err != nil {
		return fmt.Errorf("persistence: upsert promoted_tool: %w", err)
	}
	return nil
}

// DeletePromotedTool drops one learned promotion (demotion). Missing rows
// are not errors.
func (s *SidecarStore) DeletePromotedTool(repoKey, tool string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`DELETE FROM promoted_tools WHERE repo_key = ? AND tool = ?`, repoKey, tool)
	return err
}

// BumpSessionEpoch increments and returns the repo_key's session epoch — a
// monotonic counter advanced once per server startup, so "unused for N
// sessions" can be measured against last_used_epoch.
func (s *SidecarStore) BumpSessionEpoch(repoKey string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`
		INSERT INTO tool_surface_meta (repo_key, session_epoch) VALUES (?, 1)
		ON CONFLICT(repo_key) DO UPDATE SET session_epoch = session_epoch + 1`, repoKey); err != nil {
		return 0, fmt.Errorf("persistence: bump session epoch: %w", err)
	}
	var epoch int64
	if err := s.db.QueryRow(`SELECT session_epoch FROM tool_surface_meta WHERE repo_key = ?`, repoKey).Scan(&epoch); err != nil {
		return 0, fmt.Errorf("persistence: read session epoch: %w", err)
	}
	return epoch, nil
}
