package agents

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAppendInstructions_CreateThenSkip pins the behaviour every
// doc-aware adapter depends on: first call creates the file, second
// call with the same body is a no-op ActionSkip. If this regresses,
// running `gortex init` twice would append the block twice to every
// agent's rules file — the user-visible pain we extracted this helper
// to eliminate.
func TestAppendInstructions_CreateThenSkip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "rules.md")

	var buf bytes.Buffer
	action, err := AppendInstructions(&buf, path, InstructionsBody, InstructionsSentinel, ApplyOpts{})
	require.NoError(t, err)
	assert.Equal(t, ActionCreate, action.Action)

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(contents), InstructionsSentinel,
		"first write must land the full sentinel-bearing block")

	// Second call is idempotent — no duplicate append.
	action, err = AppendInstructions(&buf, path, InstructionsBody, InstructionsSentinel, ApplyOpts{})
	require.NoError(t, err)
	assert.Equal(t, ActionSkip, action.Action)
	assert.Equal(t, "block-present", action.Reason)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, len(contents), len(after),
		"second call must not grow the file")
}

// TestAppendInstructions_PreservesExistingContent guards the merge
// path — the helper must not clobber a hand-written file, it must
// append the block after the user's content with a blank-line gap.
func TestAppendInstructions_PreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	existing := "# Team conventions\n\nUse tabs, not spaces.\n"
	require.NoError(t, os.WriteFile(path, []byte(existing), 0o644))

	action, err := AppendInstructions(nil, path, InstructionsBody, InstructionsSentinel, ApplyOpts{})
	require.NoError(t, err)
	assert.Equal(t, ActionMerge, action.Action)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)

	assert.True(t, strings.HasPrefix(text, existing),
		"user content must remain at the top of the file")
	assert.Contains(t, text, InstructionsSentinel,
		"block must be appended below the user's content")
}

// TestAppendInstructions_SharedSentinelAcrossAdapters is the scenario
// that matters when two adapters target the same file (Codex and
// Opencode both write AGENTS.md). The second adapter must detect the
// first adapter's write via the shared InstructionsSentinel and skip,
// rather than duplicating the block. This is why the sentinel lives
// in the shared package, not each adapter.
func TestAppendInstructions_SharedSentinelAcrossAdapters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	// Simulate Codex writing first.
	_, err := AppendInstructions(nil, path, InstructionsBody, InstructionsSentinel, ApplyOpts{})
	require.NoError(t, err)

	// Simulate Opencode running afterwards against the same repo.
	action, err := AppendInstructions(nil, path, InstructionsBody, InstructionsSentinel, ApplyOpts{})
	require.NoError(t, err)
	assert.Equal(t, ActionSkip, action.Action,
		"second adapter targeting the same file must skip, not append again")
}

// TestAppendInstructions_DryRunReportsAction verifies --dry-run never
// touches the filesystem and reports ActionWouldCreate / ActionWouldMerge
// correctly. Users rely on the planning output to preview what init
// will do; a silent write during dry-run would be a real footgun.
func TestAppendInstructions_DryRunReportsAction(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "NEW.md")
	existingPath := filepath.Join(dir, "EXISTING.md")
	require.NoError(t, os.WriteFile(existingPath, []byte("preexisting\n"), 0o644))

	action, err := AppendInstructions(nil, newPath, InstructionsBody, InstructionsSentinel, ApplyOpts{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, ActionWouldCreate, action.Action)
	_, err = os.Stat(newPath)
	assert.True(t, os.IsNotExist(err), "dry-run must not create the file")

	action, err = AppendInstructions(nil, existingPath, InstructionsBody, InstructionsSentinel, ApplyOpts{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, ActionWouldMerge, action.Action)
	data, _ := os.ReadFile(existingPath)
	assert.Equal(t, "preexisting\n", string(data),
		"dry-run must not mutate an existing file")
}

// TestCursorMDCFrontmatter proves the MDC wrapper emits the two keys
// Cursor needs — `description` so users can see the rule in the UI
// and `alwaysApply: true` so it attaches to every chat turn. Without
// alwaysApply Cursor gates rules on keyword heuristics and the
// Gortex-preference block would fire only sporadically.
func TestCursorMDCFrontmatter(t *testing.T) {
	out := CursorMDCFrontmatter("BODY")
	assert.True(t, strings.HasPrefix(out, "---\n"),
		"MDC file must start with YAML frontmatter fence")
	assert.Contains(t, out, "alwaysApply: true",
		"MDC block must opt into always-apply so Cursor attaches it on every turn")
	assert.Contains(t, out, "description:")
	assert.Contains(t, out, "BODY")
}

// Single-home markers: content that must live in exactly one place. These
// strings anchor the reference blocks that were relocated OUT of the CLAUDE.md
// sections into the guide (provider matrix, analyze catalog) or the server
// instructions (wire-format deep-dive). Their absence from the slim bodies is
// the enforcement side of the single-home principle; their presence in the
// guide / server-instructions is asserted in the mcp package.
const (
	providerMatrixMarker = "`local` / `anthropic` / `openai` / `azure` / `ollama` / `claudecli` / `codex` / `copilot` / `cursor` / `opencode` / `gemini` / `bedrock` / `deepseek`"
	analyzeCatalogMarker = "Tarjan's SCC" // from the analyze `cycles` kind doc
	formatDeepDiveMarker = "compact tabular text, lossy"
)

// TestInstructionsBody_PolicyCoreAndSingleHome smoke-tests the slim project
// rule block: it keeps the mandatory graph-tools mapping + the memory-workflow
// pointers, and it does NOT re-carry the relocated reference content.
func TestInstructionsBody_PolicyCoreAndSingleHome(t *testing.T) {
	for _, token := range []string{
		// Graph-tools policy core.
		"search_symbols", "find_usages", "get_callers",
		"get_symbol_source", "get_editing_context", "get_file_summary",
		"read_file", "smart_context", "edit_file", "compress_bodies",
		// Memory workflow (pointer form).
		"distill_session", "surface_memories", "save_note", "store_memory",
		"query_notes", "query_memories",
		// Discovery pointers.
		"tools_search", "gortex://guide",
	} {
		if !strings.Contains(InstructionsBody, token) {
			t.Errorf("InstructionsBody no longer mentions %q — policy core regression", token)
		}
	}
	for _, banned := range []string{providerMatrixMarker, analyzeCatalogMarker, formatDeepDiveMarker} {
		if strings.Contains(InstructionsBody, banned) {
			t.Errorf("InstructionsBody re-carries relocated content %q — single-home violation", banned)
		}
	}
}

// TestGlobalInstructionsBody_PolicyCoreAndSingleHome mirrors the check for the
// per-machine block written by `gortex install` into ~/.claude/CLAUDE.md — the
// single home for the full memory-workflow triggers.
func TestGlobalInstructionsBody_PolicyCoreAndSingleHome(t *testing.T) {
	for _, token := range []string{
		// Graph-tools policy core.
		"search_symbols", "find_usages", "get_callers", "get_call_chain",
		"get_symbol_source", "get_editing_context", "read_file",
		"smart_context", "edit_file", "rename_symbol", "compress_bodies",
		// Full memory triggers (the single home).
		"distill_session", "surface_memories", "save_note", "store_memory",
		"query_notes", "query_memories",
		// Discovery pointers.
		"tools_search", "gortex://guide", "gortex daemon start",
	} {
		if !strings.Contains(GlobalInstructionsBody, token) {
			t.Errorf("GlobalInstructionsBody no longer mentions %q", token)
		}
	}
	for _, banned := range []string{
		providerMatrixMarker, analyzeCatalogMarker, formatDeepDiveMarker,
		// Reference catalogs that relocated to the guide / schema resource.
		// (A bare "search_ast" pointer is allowed; the DETECTOR catalog — named
		// detectors like error-not-wrapped — must not be inlined.)
		"k8s_resources", "error-not-wrapped", "gortex://report",
	} {
		if strings.Contains(GlobalInstructionsBody, banned) {
			t.Errorf("GlobalInstructionsBody re-carries relocated content %q — single-home violation", banned)
		}
	}
}
