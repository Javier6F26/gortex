package contracts

import "testing"

// TestHTTPExtractor_Express_InlineArrowAnchor verifies that a route whose
// handler is an inline arrow anchors its Contract SymbolID to the synthetic
// express-handler node the JS extractor materialises (the through-handler
// call-attribution anchor), so a trace from the route reaches the services
// the anonymous handler calls.
func TestHTTPExtractor_Express_InlineArrowAnchor(t *testing.T) {
	src := []byte("app.get('/u', (req, res) => { svc.list(); res.json({}); });\n")
	ext := &HTTPExtractor{}
	cs := ext.Extract("routes.js", src, nil, nil)

	var get *Contract
	for i := range cs {
		if cs[i].ID == "http::GET::/u" {
			get = &cs[i]
		}
	}
	if get == nil {
		t.Fatalf("no route contract for GET /u; got %+v", cs)
	}
	if get.SymbolID != "routes.js::express-handler@1" {
		t.Errorf("inline-arrow route SymbolID = %q (want routes.js::express-handler@1)", get.SymbolID)
	}
}

// TestHTTPExtractor_Express_NamedHandlerUnchanged confirms the inline anchor
// does not disturb named-handler binding.
func TestHTTPExtractor_Express_NamedHandlerUnchanged(t *testing.T) {
	src := []byte("app.get('/named', listUsers);\n")
	nodes := makeNodes("routes.js", []struct {
		name       string
		start, end int
	}{{"listUsers", 1, 1}})
	ext := &HTTPExtractor{}
	cs := ext.Extract("routes.js", src, nodes, nil)

	for _, c := range cs {
		if c.ID == "http::GET::/named" && c.SymbolID != "routes.js::listUsers" {
			t.Errorf("named handler SymbolID = %q (want routes.js::listUsers)", c.SymbolID)
		}
	}
}
