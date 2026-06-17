package languages

import (
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestSwiftExtractor_Depth(t *testing.T) {
	const swift = `import Foundation

class Service {
    public static func shared() -> Service {
        return Service()
    }
    func fetch(id: Int) async throws -> [User] {
        return []
    }
    func plain() {
    }
}
`
	res, err := NewSwiftExtractor().Extract("Service.swift", []byte(swift))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*graph.Node{}
	for _, n := range res.Nodes {
		byName[n.Name] = n
	}

	shared := byName["shared"]
	if shared == nil {
		t.Fatal("method 'shared' was not extracted")
	}
	if shared.Meta["is_static"] != true {
		t.Errorf("shared should be static: meta=%v", shared.Meta)
	}
	if shared.Meta["return_type"] != "Service" {
		t.Errorf("shared return_type = %v, want Service", shared.Meta["return_type"])
	}
	if sig, _ := shared.Meta["signature"].(string); !strings.Contains(sig, "-> Service") {
		t.Errorf("shared signature %q lacks the real return type", sig)
	}

	fetch := byName["fetch"]
	if fetch == nil {
		t.Fatal("method 'fetch' was not extracted")
	}
	if fetch.Meta["is_async"] != true {
		t.Errorf("fetch should be async: meta=%v", fetch.Meta)
	}
	if fetch.Meta["return_type"] != "[User]" {
		t.Errorf("fetch return_type = %v, want [User]", fetch.Meta["return_type"])
	}
	if sig, _ := fetch.Meta["signature"].(string); sig == "func fetch(...)" {
		t.Errorf("fetch signature is still a stub: %q", sig)
	}

	plain := byName["plain"]
	if plain == nil {
		t.Fatal("method 'plain' was not extracted")
	}
	if plain.Meta["is_async"] == true || plain.Meta["is_static"] == true {
		t.Errorf("plain should be neither async nor static: meta=%v", plain.Meta)
	}
	if _, ok := plain.Meta["return_type"]; ok {
		t.Errorf("plain should have no return_type: meta=%v", plain.Meta)
	}
}
