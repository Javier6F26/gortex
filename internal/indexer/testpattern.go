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

// IsTestSymbol returns true when a function/method name looks like a
// test entry point per its language's convention. For languages where
// test runners pick up by annotation (Java @Test, Rust #[test]) or by
// being inside a test file (TS/JS), we conservatively rely on
// IsTestFile — return true for any function in a test file.
func IsTestSymbol(name, language string) bool {
	if name == "" {
		return false
	}
	switch language {
	case "go":
		return hasTestPrefix(name, "Test", "Benchmark", "Fuzz", "Example")
	case "python":
		return strings.HasPrefix(name, "test_") || strings.HasPrefix(name, "Test")
	case "ruby":
		return strings.HasPrefix(name, "test_")
	case "rust":
		// Rust tests are annotation-driven (#[test]); fall back to the
		// in-test-file rule for cheap detection without resolving
		// annotations.
		return false
	case "java", "kotlin", "csharp", "swift":
		// Annotation-driven — caller should rely on IsTestFile.
		return false
	}
	return false
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
