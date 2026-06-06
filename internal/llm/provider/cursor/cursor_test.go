package cursor

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
	bin = filepath.Join(dir, "fake-cursor.sh")
	argsLog = filepath.Join(dir, "args.txt")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsLog + "'\ncat >/dev/null\nprintf '%s' '" + stdout + "'\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsLog
}

func TestNew_NotFound(t *testing.T) {
	if _, err := New(llm.CLIConfig{Binary: "no-such-cursor-binary-xyz"}); err == nil {
		t.Fatal("expected an error when the binary is missing")
	}
}

func TestComplete_InvokesCursorWithTextFormatFlagPromptAndModel(t *testing.T) {
	bin, argsLog := fakeBin(t, "from cursor")
	p, err := New(llm.CLIConfig{Binary: bin, Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "cursor" {
		t.Errorf("Name()=%q", p.Name())
	}
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(readLines(t, argsLog), "|")
	if !strings.Contains(args, "--output-format|text") {
		t.Errorf("expected --output-format text, argv=%q", args)
	}
	if !strings.Contains(args, "--model|sonnet") {
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
