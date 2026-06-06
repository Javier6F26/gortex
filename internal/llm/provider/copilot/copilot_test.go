package copilot

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/llm"
)

// fakeBin writes a shell script that logs its argv to a file and echoes
// a canned answer, standing in for the real `copilot` CLI.
func fakeBin(t *testing.T, stdout string) (bin, argsLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary harness uses /bin/sh")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "fake-copilot.sh")
	argsLog = filepath.Join(dir, "args.txt")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsLog + "'\ncat >/dev/null\nprintf '%s' '" + stdout + "'\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsLog
}

func TestNew_NotFound(t *testing.T) {
	if _, err := New(llm.CLIConfig{Binary: "no-such-copilot-binary-xyz"}); err == nil {
		t.Fatal("expected an error when the binary is missing")
	}
}

func TestComplete_InvokesCopilotWithFlagPromptAndModel(t *testing.T) {
	bin, argsLog := fakeBin(t, "from copilot")
	p, err := New(llm.CLIConfig{Binary: bin, Model: "claude-opus-4.1"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "copilot" {
		t.Errorf("Name()=%q", p.Name())
	}
	resp, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "from copilot" {
		t.Errorf("text=%q", resp.Text)
	}
	args := strings.Join(readLines(t, argsLog), "|")
	if !strings.Contains(args, "--model|claude-opus-4.1") {
		t.Errorf("expected --model flag, argv=%q", args)
	}
	if !strings.Contains(args, "-p|User: hi") {
		t.Errorf("expected -p prompt delivery, argv=%q", args)
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
