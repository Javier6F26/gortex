package opencode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/llm"
)

func fakeBin(t *testing.T, stdout string) (bin, argsLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary harness uses /bin/sh")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "fake-opencode.sh")
	argsLog = filepath.Join(dir, "args.txt")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsLog + "'\ncat >/dev/null\nprintf '%s' '" + stdout + "'\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsLog
}

func TestNew_NotFound(t *testing.T) {
	if _, err := New(llm.CLIConfig{Binary: "no-such-opencode-binary-xyz"}); err == nil {
		t.Fatal("expected an error when the binary is missing")
	}
}

func TestComplete_InvokesOpencodeRunWithPositionalPromptAndModel(t *testing.T) {
	bin, argsLog := fakeBin(t, "from opencode")
	p, err := New(llm.CLIConfig{Binary: bin, Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "opencode" {
		t.Errorf("Name()=%q", p.Name())
	}
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	args := readLines(t, argsLog)
	joined := strings.Join(args, "|")
	if args[0] != "run" {
		t.Errorf("expected `run` subcommand first, argv=%q", joined)
	}
	if !strings.Contains(joined, "--model|anthropic/claude-sonnet-4-6") {
		t.Errorf("expected --model flag, argv=%q", joined)
	}
	// The prompt is the trailing positional argument.
	if args[len(args)-1] != "User: hi" {
		t.Errorf("expected prompt as the trailing positional, argv=%q", joined)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}
