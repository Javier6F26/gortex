package languages

import "testing"

// TestExtractJavaRNModuleNames_NestedClasses guards the slice-bounds bug
// (issue #54): a nested class declaration starts inside its enclosing
// class's body span, so the "annotation band since the previous class"
// lower bound could exceed the current class's start and panic.
func TestExtractJavaRNModuleNames_NestedClasses(t *testing.T) {
	src := []byte(`package example.repro;

class Parent {
    static class Builder<T extends Builder<T>> {
    }
}

public class MinimalNestedGenericBuilder {
    public static class Builder extends Parent.Builder<Builder> {
    }
}
`)
	got := extractJavaRNModuleNames(src) // must not panic
	for _, want := range []string{"Parent", "Builder", "MinimalNestedGenericBuilder"} {
		if got[want] != want {
			t.Errorf("module for %q = %q, want %q (default to class name)", want, got[want], want)
		}
	}
}

// TestExtractJavaRNModuleNames_NestedDoesNotStealOuterAnnotation makes
// sure a @ReactModule on an outer class is not mis-attributed to a later
// sibling once a nested class has rewound the scan window.
func TestExtractJavaRNModuleNames_AnnotationStillResolves(t *testing.T) {
	src := []byte(`package com.example;
import com.facebook.react.module.annotations.ReactModule;

class Outer {
    static class Inner {}
}

@ReactModule(name = "Calendar")
public class CalendarModule {
    public String getName() { return "Calendar"; }
}

public class Unrelated {}
`)
	got := extractJavaRNModuleNames(src)
	if got["CalendarModule"] != "Calendar" {
		t.Errorf("CalendarModule module = %q, want Calendar (the @ReactModule override)", got["CalendarModule"])
	}
	// The @ReactModule on CalendarModule must not bleed onto a later
	// sibling, and the nested Inner must default to its class name.
	if got["Unrelated"] != "Unrelated" {
		t.Errorf("Unrelated module = %q, want Unrelated", got["Unrelated"])
	}
	if got["Inner"] != "Inner" {
		t.Errorf("Inner module = %q, want Inner", got["Inner"])
	}
}

// TestJavaExtract_NestedGenericBuilder is the end-to-end form of the
// issue #54 reproducer: the whole extractor must not panic and must
// still emit the declared types.
func TestJavaExtract_NestedGenericBuilder(t *testing.T) {
	cases := map[string]string{
		"minimal": `package example.repro;

class Parent {
    static class Builder<T extends Builder<T>> {
    }
}

public class MinimalNestedGenericBuilder {
    public static class Builder extends Parent.Builder<Builder> {
    }
}
`,
		"factory_with_interface": `package example.repro;

interface Measurable {}

public class MetricFactory extends BaseFactory implements Measurable {
    public static class Builder extends BaseFactory.Builder<Builder> {
        Builder measurement(String m) { return this; }
    }

    public static Builder builder() {
        return new Builder().measurement("x");
    }
}
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := NewJavaExtractor().Extract(name+".java", []byte(src))
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			if len(res.Nodes) == 0 {
				t.Fatalf("expected at least the file node, got none")
			}
			names := map[string]bool{}
			for _, n := range res.Nodes {
				names[n.Name] = true
			}
			if !names["Builder"] {
				t.Errorf("expected a Builder type node; got names %v", names)
			}
		})
	}
}
