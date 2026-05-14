package indexer

import (
	"path/filepath"
	"strings"
)

// IsTestFile returns true when the file's name or directory matches a
// recognised test convention from the table below. False positives
// here are downgraded downstream by the symbol-name filter
// (IsTestSymbol).
//
// Recognised conventions:
//
//	*_test.go                          (Go)
//	*.test.{ts,tsx,js,jsx,mts,cts}     (TS/JS via Jest/Vitest convention)
//	*.spec.{ts,tsx,js,jsx,mts,cts}     (TS/JS spec convention)
//	test_*.py / *_test.py              (Python)
//	*_test.dart                        (Dart)
//	*_spec.rb / *_test.rb              (Ruby)
//	*Test.java / *Tests.java           (JUnit / Spring)
//	*Test.kt  / *Tests.kt              (Kotlin)
//	*Tests.cs                          (C# xUnit/NUnit)
//	*Tests.swift                       (Swift)
//	*Test.php / *test.php              (PHPUnit / Pest)
//	files under __tests__/, tests/,
//	  test/, spec/                     (any language using these dirs)
func IsTestFile(path string) bool {
	if path == "" {
		return false
	}
	// Directory-based hints first — covers projects that don't follow
	// the per-file naming convention.
	dir := filepath.ToSlash(path)
	for _, marker := range []string{"/__tests__/", "/tests/", "/test/", "/spec/"} {
		if strings.Contains(dir, marker) {
			return true
		}
	}
	if strings.HasPrefix(dir, "tests/") || strings.HasPrefix(dir, "test/") || strings.HasPrefix(dir, "spec/") {
		return true
	}

	base := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(base))
	stem := strings.TrimSuffix(base, ext)

	switch ext {
	case ".go":
		return strings.HasSuffix(stem, "_test")
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs":
		return strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec")
	case ".py":
		return strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, "_test")
	case ".dart":
		return strings.HasSuffix(stem, "_test")
	case ".rb":
		return strings.HasSuffix(stem, "_spec") || strings.HasSuffix(stem, "_test")
	case ".java", ".kt":
		return strings.HasSuffix(stem, "Test") || strings.HasSuffix(stem, "Tests")
	case ".cs":
		return strings.HasSuffix(stem, "Tests") || strings.HasSuffix(stem, "Test")
	case ".swift":
		return strings.HasSuffix(stem, "Tests")
	case ".php":
		return strings.HasSuffix(stem, "Test") || strings.HasSuffix(stem, "test")
	}
	return false
}

// TestRole classifies a function/method name by its language's test
// convention and returns the specific role — "test", "benchmark",
// "fuzz", or "example" — or "" when the name matches no convention.
// For languages where test runners pick up by annotation (Java @Test,
// Rust #[test]) or by file membership alone (TS/JS), the name carries
// no role signal; callers fall back to IsTestFile and treat such
// symbols as a plain "test".
func TestRole(name, language string) string {
	if name == "" {
		return ""
	}
	switch language {
	case "go":
		switch {
		case hasTestPrefix(name, "Benchmark"):
			return "benchmark"
		case hasTestPrefix(name, "Fuzz"):
			return "fuzz"
		case hasTestPrefix(name, "Example"):
			return "example"
		case hasTestPrefix(name, "Test"):
			return "test"
		}
	case "python":
		if strings.HasPrefix(name, "test_") || strings.HasPrefix(name, "Test") {
			return "test"
		}
	case "ruby":
		if strings.HasPrefix(name, "test_") {
			return "test"
		}
	}
	return ""
}

// IsTestSymbol returns true when a function/method name looks like a
// test entry point per its language's convention. It is a back-compat
// wrapper over TestRole — callers that need the specific role should
// use TestRole directly.
func IsTestSymbol(name, language string) bool {
	return TestRole(name, language) != ""
}

func hasTestPrefix(name string, prefixes ...string) bool {
	for _, p := range prefixes {
		if !strings.HasPrefix(name, p) {
			continue
		}
		// Must be followed by an uppercase letter or end of name —
		// "Testing" is not a Go test fn but "TestFoo" is. "Test" alone
		// is not picked up by `go test` either; require a suffix.
		if len(name) == len(p) {
			return false
		}
		c := name[len(p)]
		if c >= 'A' && c <= 'Z' {
			return true
		}
	}
	return false
}
