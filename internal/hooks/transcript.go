package hooks

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// transcriptTailBytes bounds how much of the end of a (potentially large)
// session transcript we scan for the model — enough to span several
// recent turns without reading a multi-megabyte file on every tool call.
const transcriptTailBytes = 256 * 1024

// modelFromTranscript returns the model id of the most recent assistant
// turn in a Claude Code transcript JSONL file, or "" when it can't be
// determined. Each transcript line is a JSON record; assistant turns
// carry the model at message.model. The scan reads only the file's tail
// and walks lines from the end, so the freshest model — including one
// chosen by a mid-session /model switch — wins. Best-effort: any I/O or
// parse failure yields "".
func modelFromTranscript(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	start := int64(0)
	if fi.Size() > transcriptTailBytes {
		start = fi.Size() - transcriptTailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}

	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		// A tail read may begin mid-line; such a leading partial line
		// fails to parse and is simply skipped.
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var rec struct {
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if m := strings.TrimSpace(rec.Message.Model); m != "" {
			return m
		}
	}
	return ""
}
