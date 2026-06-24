package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func expressInlineCall(edges []*graph.Edge, from, callee string) *graph.Edge {
	for _, e := range edges {
		if e.Meta == nil {
			continue
		}
		if _, ok := e.Meta["express_inline_handler"]; !ok {
			continue
		}
		if e.From == from && e.To == "unresolved::"+callee {
			return e
		}
	}
	return nil
}

func expressHasInlineCallTo(edges []*graph.Edge, callee string) bool {
	for _, e := range edges {
		if e.Meta != nil {
			if _, ok := e.Meta["express_inline_handler"]; ok && e.To == "unresolved::"+callee {
				return true
			}
		}
	}
	return false
}

func TestExpressInline_BodyCallsAttributed(t *testing.T) {
	src := `app.get('/u', (req, res) => {
  svc.list();
  res.json({ ok: true });
  next();
});
`
	res, err := NewJavaScriptExtractor().Extract("routes.js", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	handlerID := ExpressInlineHandlerNodeID("routes.js", 1)
	if expressInlineCall(res.Edges, handlerID, "list") == nil {
		t.Errorf("inline handler should call svc.list (callee list)")
	}
	// req/res helpers and next() are filtered out.
	if expressHasInlineCallTo(res.Edges, "json") {
		t.Errorf("res.json() must not produce an inline-handler call edge")
	}
	if expressHasInlineCallTo(res.Edges, "next") {
		t.Errorf("next() must not produce an inline-handler call edge")
	}
	// The synthetic handler node was materialised.
	var found bool
	for _, n := range res.Nodes {
		if n.ID == handlerID && n.Meta != nil && n.Meta["express_handler"] == true {
			found = true
		}
	}
	if !found {
		t.Errorf("synthetic express-handler node not materialised")
	}
}

func TestExpressInline_RouterAsyncHandler(t *testing.T) {
	src := `const router = express.Router();
router.post('/items/:id', async (req, res, next) => {
  const out = await itemService.create(req.body);
  res.status(201).json(out);
});
`
	res, err := NewJavaScriptExtractor().Extract("r.js", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if !expressHasInlineCallTo(res.Edges, "create") {
		t.Errorf("async router handler should call itemService.create")
	}
	if expressHasInlineCallTo(res.Edges, "status") || expressHasInlineCallTo(res.Edges, "json") {
		t.Errorf("res.status()/res.json() must be filtered out")
	}
}

func TestExpressInline_NamedHandlerNotAttributed(t *testing.T) {
	// A named handler is bound by the existing path; no inline body walk.
	src := `app.get('/named', listUsers);
`
	res, err := NewJavaScriptExtractor().Extract("n.js", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Edges {
		if e.Meta != nil {
			if _, ok := e.Meta["express_inline_handler"]; ok {
				t.Errorf("a named-handler route must not produce inline-handler edges")
			}
		}
	}
}

func TestExpressInline_NonRouteCallIgnored(t *testing.T) {
	// `.get` on a non-route receiver with a function arg must not be treated
	// as a route handler.
	src := `cache.get('key', () => compute());
`
	res, err := NewJavaScriptExtractor().Extract("c.js", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if expressHasInlineCallTo(res.Edges, "compute") {
		t.Errorf("cache.get is not a route — its callback must not be attributed")
	}
}

func TestTSExpressInline_Handler(t *testing.T) {
	src := `app.get('/u', (req: Request, res: Response) => { userService.list(); });
`
	res, err := NewTypeScriptExtractor().Extract("r.ts", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if !expressHasInlineCallTo(res.Edges, "list") {
		t.Errorf("TS inline handler should call userService.list")
	}
}
