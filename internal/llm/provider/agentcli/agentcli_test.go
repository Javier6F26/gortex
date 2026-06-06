package agentcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zzet/gortex/internal/llm"
)

type fakeOpts struct {
	stdout   string
	stderr   string
	exitCode int
	sleep    time.Duration
}

// fakeBin writes a shell script standing in for a coding-agent CLI. It
// logs its argv (one per line) and stdin to sidecar files, echoes a
// canned stdout, and exits with a chosen code — enough to exercise
// every engine path (arg construction, stdin piping, extraction,
// stderr/exit handling, timeout) without a real CLI install.
func fakeBin(t *testing.T, opts fakeOpts) (bin, argsLog, stdinLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary harness uses /bin/sh")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "fake-cli.sh")
	argsLog = filepath.Join(dir, "args.txt")
	stdinLog = filepath.Join(dir, "stdin.txt")
	q := func(s string) string { return strings.ReplaceAll(s, "'", "'\\''") }

	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > '" + argsLog + "'\n" +
		"cat > '" + stdinLog + "'\n"
	if opts.sleep > 0 {
		body += fmt.Sprintf("sleep %d\n", int(opts.sleep.Seconds())+1)
	}
	if opts.stderr != "" {
		body += "printf '%s' '" + q(opts.stderr) + "' >&2\n"
	}
	if opts.stdout != "" {
		body += "printf '%s' '" + q(opts.stdout) + "'\n"
	}
	if opts.exitCode != 0 {
		body += fmt.Sprintf("exit %d\n", opts.exitCode)
	}
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsLog, stdinLog
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	s := strings.TrimRight(string(raw), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestComplete_FlagDeliveryBuildsArgv(t *testing.T) {
	bin, argsLog, stdinLog := fakeBin(t, fakeOpts{stdout: "the answer"})
	p, err := New(Spec{
		ProviderID: "cursor",
		Model:      "claude-x",
		ModelFlag:  "--model",
		BaseArgs:   []string{"--output-format", "text"},
		Delivery:   DeliveryFlag,
		PromptFlag: "-p",
		ExtraArgs:  []string{"--trust"},
	}, bin, "cursor-agent")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "the answer" {
		t.Errorf("text=%q", resp.Text)
	}
	args := readLines(t, argsLog)
	want := []string{"--output-format", "text", "--model", "claude-x", "--trust", "-p", "User: hello"}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Errorf("argv=%v\nwant %v", args, want)
	}
	// Flag delivery must not feed the prompt on stdin.
	if got := readLines(t, stdinLog); len(got) != 0 {
		t.Errorf("stdin should be empty for flag delivery, got %v", got)
	}
}

func TestComplete_ArgDeliveryAppendsPositional(t *testing.T) {
	bin, argsLog, _ := fakeBin(t, fakeOpts{stdout: "ok"})
	p, err := New(Spec{
		ProviderID: "opencode",
		Model:      "anthropic/claude-sonnet-4-6",
		ModelFlag:  "--model",
		BaseArgs:   []string{"run"},
		Delivery:   DeliveryArg,
	}, bin, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	args := readLines(t, argsLog)
	want := []string{"run", "--model", "anthropic/claude-sonnet-4-6", "User: hi"}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Errorf("argv=%v\nwant %v", args, want)
	}
}

func TestComplete_StdinDeliveryPipesPrompt(t *testing.T) {
	bin, argsLog, stdinLog := fakeBin(t, fakeOpts{stdout: "ok"})
	p, err := New(Spec{
		ProviderID: "copilot",
		Delivery:   DeliveryStdin,
	}, bin, "copilot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "piped"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, argsLog); len(got) != 0 {
		t.Errorf("stdin delivery should pass no prompt arg, got %v", got)
	}
	if got := strings.TrimSpace(strings.Join(readLines(t, stdinLog), "\n")); got != "User: piped" {
		t.Errorf("stdin=%q want 'User: piped'", got)
	}
}

func TestComplete_StructuredExtractsJSONFromChattyReply(t *testing.T) {
	bin, argsLog, _ := fakeBin(t, fakeOpts{stdout: "Here you go:\n{\"terms\":[\"jwt\"]}\nhope that helps"})
	p, err := New(Spec{ProviderID: "copilot", Delivery: DeliveryFlag, PromptFlag: "-p"}, bin, "copilot")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "auth"}},
		Shape:    llm.ShapeExpandTerms,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"terms":["jwt"]}` {
		t.Errorf("text=%q want the extracted JSON", resp.Text)
	}
	// The structured prompt must carry the schema rider (the prompt arg
	// spans multiple lines, so check the whole logged argv).
	args := strings.Join(readLines(t, argsLog), "\n")
	if !strings.Contains(args, "JSON Schema") {
		t.Errorf("expected a schema rider in the prompt arg, got %q", args)
	}
}

func TestComplete_NonZeroExitErrorsWithStderr(t *testing.T) {
	bin, _, _ := fakeBin(t, fakeOpts{stderr: "not signed in", exitCode: 1})
	p, err := New(Spec{ProviderID: "copilot", Delivery: DeliveryFlag, PromptFlag: "-p"}, bin, "copilot")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error for a non-zero exit")
	}
	if !strings.Contains(err.Error(), "not signed in") {
		t.Errorf("error should surface stderr, got %v", err)
	}
}

func TestComplete_EmptyOutputErrors(t *testing.T) {
	bin, _, _ := fakeBin(t, fakeOpts{stdout: ""})
	p, err := New(Spec{ProviderID: "opencode", Delivery: DeliveryArg, BaseArgs: []string{"run"}}, bin, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}); err == nil {
		t.Fatal("expected an error for empty CLI output")
	}
}

func TestComplete_Timeout(t *testing.T) {
	bin, _, _ := fakeBin(t, fakeOpts{stdout: "late", sleep: 2 * time.Second})
	p, err := New(Spec{ProviderID: "cursor", Delivery: DeliveryFlag, PromptFlag: "-p", Timeout: 500 * time.Millisecond}, bin, "cursor-agent")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected a timeout error, got %v", err)
	}
}

func TestNew_BinaryNotFound(t *testing.T) {
	if _, err := New(Spec{ProviderID: "copilot"}, "definitely-not-a-real-binary-xyz", "copilot"); err == nil {
		t.Fatal("expected an error when the binary is not on PATH")
	}
}

func TestName(t *testing.T) {
	bin, _, _ := fakeBin(t, fakeOpts{stdout: "x"})
	p, err := New(Spec{ProviderID: "opencode", Delivery: DeliveryArg}, bin, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "opencode" {
		t.Errorf("Name()=%q", p.Name())
	}
}

func TestFlattenPrompt(t *testing.T) {
	got := FlattenPrompt([]llm.Message{
		{Role: llm.RoleSystem, Content: "be terse"},
		{Role: llm.RoleUser, Content: "find auth"},
		{Role: llm.RoleAssistant, Content: "looking"},
		{Role: llm.RoleTool, ToolName: "search", Content: "found x"},
	})
	for _, want := range []string{"System instructions:\nbe terse", "User: find auth", "Assistant: looking", "Tool result (search):\nfound x"} {
		if !strings.Contains(got, want) {
			t.Errorf("flattened prompt missing %q\n--- got ---\n%s", want, got)
		}
	}
}
