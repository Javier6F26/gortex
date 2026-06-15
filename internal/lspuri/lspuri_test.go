package lspuri

import (
	"path/filepath"
	"runtime"
	"testing"
)

// The drive-letter / separator handling is the OS-independent core of the
// Windows bug, exercised here via the pure helpers so these assertions run
// (and would have caught the bug) on the Linux/macOS CI runners — Windows
// only builds in CI, it does not run the test suite.

func TestSlashAbsToURI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/repo/Main.java", "file:///repo/Main.java"},          // POSIX
		{"C:/repo/Main.java", "file:///C:/repo/Main.java"},     // Windows drive (forward-slashed)
		{"/repo/a b.java", "file:///repo/a%20b.java"},          // space is percent-encoded
		{"D:/proj/src/X.java", "file:///D:/proj/src/X.java"},   // another drive
		{"", ""},
	}
	for _, c := range cases {
		if got := slashAbsToURI(c.in); got != c.want {
			t.Errorf("slashAbsToURI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestURIToSlashAbs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"file:///repo/Main.java", "/repo/Main.java"},            // POSIX
		{"file:///C:/repo/Main.java", "C:/repo/Main.java"},       // Windows: strip leading slash before drive
		{"file:///c:/repo/Main.java", "c:/repo/Main.java"},       // lowercase drive
		{"file:///repo/a%20b.java", "/repo/a b.java"},            // percent-decoded
		{"http://example.com/x", ""},                             // non-file scheme rejected
		{"", ""},
	}
	for _, c := range cases {
		if got := uriToSlashAbs(c.in); got != c.want {
			t.Errorf("uriToSlashAbs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRoundTrip_Windows proves the exact failure that broke jdtls on
// Windows is now fixed: a Windows file URI maps back to a repo-relative
// path. Asserts the OS-independent slash form so it runs on Linux CI.
func TestURIToSlashAbs_WindowsDriveRoundTrip(t *testing.T) {
	uri := slashAbsToURI("C:/repo/pkg/Service.java")
	if uri != "file:///C:/repo/pkg/Service.java" {
		t.Fatalf("built URI = %q", uri)
	}
	back := uriToSlashAbs(uri)
	if back != "C:/repo/pkg/Service.java" {
		t.Fatalf("round-trip = %q, want C:/repo/pkg/Service.java", back)
	}
}

// TestPathToURI_RoundTrip_NativeOS exercises the public funcs end to end
// on whatever OS the test runs, guarding the POSIX path from regression
// (and the full Windows path when run on Windows).
func TestPathToURI_RoundTrip_NativeOS(t *testing.T) {
	abs, err := filepath.Abs(filepath.Join("repo", "pkg", "File.java"))
	if err != nil {
		t.Fatal(err)
	}
	uri := PathToURI(abs)
	got := URIToAbsPath(uri)
	if got != abs {
		t.Errorf("round-trip on %s: PathToURI(%q) -> %q -> %q", runtime.GOOS, abs, uri, got)
	}
}

func TestURIToRepoRel(t *testing.T) {
	repo, _ := filepath.Abs("myrepo")
	inside, _ := filepath.Abs(filepath.Join("myrepo", "src", "Main.java"))
	if got := URIToRepoRel(PathToURI(inside), repo); got != "src/Main.java" {
		t.Errorf("URIToRepoRel inside = %q, want src/Main.java", got)
	}
	outside, _ := filepath.Abs(filepath.Join("other", "X.java"))
	if got := URIToRepoRel(PathToURI(outside), repo); got != "" {
		t.Errorf("URIToRepoRel outside = %q, want \"\"", got)
	}
}
