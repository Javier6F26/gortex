package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestSwiftExtractor_ClosureCollection(t *testing.T) {
	const swift = `class Request {
    var validators: [() -> Void] = []

    func validate(closure: @escaping () -> Void) {
        validators.append(closure)
    }

    func didCompleteTask() {
        validators.forEach { $0() }
    }
}
`
	res, err := NewSwiftExtractor().Extract("Request.swift", []byte(swift))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*graph.Node{}
	for _, n := range res.Nodes {
		byName[n.Name] = n
	}

	validate := byName["validate"]
	if validate == nil {
		t.Fatal("method 'validate' not extracted")
	}
	if validate.Meta["cc_append_field"] != "validators" {
		t.Errorf("validate cc_append_field = %v, want validators", validate.Meta["cc_append_field"])
	}

	dispatch := byName["didCompleteTask"]
	if dispatch == nil {
		t.Fatal("method 'didCompleteTask' not extracted")
	}
	if dispatch.Meta["cc_dispatch_field"] != "validators" {
		t.Errorf("didCompleteTask cc_dispatch_field = %v, want validators", dispatch.Meta["cc_dispatch_field"])
	}
}
