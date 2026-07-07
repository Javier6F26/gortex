package goanalysis

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/semantic"
)

// LoadMode controls how deeply the go/types provider analyzes the code.
type LoadMode int

const (
	// ModeTypeCheck loads types only (~5-10s). Resolves all type information
	// and interface implementations but does not build a call graph.
	ModeTypeCheck LoadMode = iota

	// ModeCallGraph loads SSA and builds a VTA call graph (~15-30s).
	// Most precise but requires more time and memory.
	ModeCallGraph
)

// Provider uses Go's native toolchain (go/packages, go/types) for
// compiler-level precision on Go codebases.
type Provider struct {
	mode        LoadMode
	includeTest bool
	logger      *zap.Logger

	// Cached per-repo state from EnrichRepo — used by LookupTypeAtLine to
	// answer per-binding type queries from the contract pipeline without
	// re-loading packages. Keyed by the repo's absolute root so that, when
	// multiple repos are enriched (in any order, possibly concurrently) before
	// their contracts are extracted, each repo's contract pass still finds its
	// own loaded packages rather than the last writer's. Guarded by stateMu.
	//
	// A loaded stash holds the whole type-checked program (go/types.Info +
	// every file's AST) for one repo — on the order of 1-2 GB for a large
	// module. Retaining one per repo forever previously made the daemon's
	// RSS grow without bound. The stash is therefore bounded under stateMu:
	// an idle TTL (stashTTL) releases a repo's program once its contract
	// pass goes quiet, and a count ceiling (maxStashes) caps how many
	// coexist during a multi-repo warmup. A LookupTypeAtLine that misses an
	// evicted stash returns ("", false) — the contract pipeline then falls
	// back to its tree-sitter type tier, the intended graceful degradation.
	stateMu    sync.RWMutex
	stashes    map[string]*goStash // absRoot → loaded package state
	maxStashes int                 // count ceiling (env GORTEX_GOTYPES_MAX_STASHES)
	stashTTL   time.Duration       // idle release window (env GORTEX_GOTYPES_STASH_TTL)
	sweepOnce  sync.Once
	stopSweep  chan struct{}
}

// goStash is one repo's loaded go/packages state, retained for
// LookupTypeAtLine after EnrichRepo returns. lastUsed drives idle
// eviction; it is bumped under stateMu whenever a lookup touches the
// repo, so an actively-queried program is never released mid-contract.
type goStash struct {
	pkgs     []*packages.Package
	fset     *token.FileSet
	absRoot  string
	lastUsed time.Time
}

// Stash-retention bounds. Both overridable via env for operators who want
// a different memory/recompute trade-off.
const (
	defaultMaxStashes = 8
	defaultStashTTL   = 3 * time.Minute
)

// NewProvider creates a go/types provider.
func NewProvider(mode LoadMode, includeTest bool, logger *zap.Logger) *Provider {
	return &Provider{
		mode:        mode,
		includeTest: includeTest,
		logger:      logger,
		maxStashes:  envPositiveInt("GORTEX_GOTYPES_MAX_STASHES", defaultMaxStashes),
		stashTTL:    envPositiveDuration("GORTEX_GOTYPES_STASH_TTL", defaultStashTTL),
		stopSweep:   make(chan struct{}),
	}
}

func (p *Provider) Name() string        { return "go-types" }
func (p *Provider) Languages() []string { return []string{"go"} }

// Close stops the idle sweeper (idempotent) and drops every retained
// type-checked program so a torn-down provider holds no memory.
func (p *Provider) Close() error {
	if p.stopSweep != nil {
		select {
		case <-p.stopSweep:
		default:
			close(p.stopSweep)
		}
	}
	p.stateMu.Lock()
	p.stashes = nil
	p.stateMu.Unlock()
	return nil
}

// goToolchainOnce caches the one-time probe for the `go` command. The
// provider shells out through go/packages (`go list`), so without the
// toolchain on PATH it can only fail — and a shipped gortex binary often
// runs on a machine with no Go installed. Probing once lets the manager's
// Available() gate skip the provider instead of attempting a package load
// that fails on every index.
var (
	goToolchainOnce sync.Once
	goToolchainOK   bool
)

func goToolchainAvailable() bool {
	goToolchainOnce.Do(func() {
		_, err := exec.LookPath("go")
		goToolchainOK = err == nil
	})
	return goToolchainOK
}

func (p *Provider) Available() bool {
	// go/packages requires the Go toolchain; gate on a cached PATH probe
	// so a binary running without `go` skips this provider cleanly and
	// the go-ast-types supplemental provider serves Go instead.
	return goToolchainAvailable()
}

func (p *Provider) Enrich(g graph.Store, repoRoot string) (*semantic.EnrichResult, error) {
	return p.EnrichRepo(g, "", repoRoot)
}

// EnrichRepo runs the go/types enrichment pass with its graph scans scoped
// to repoPrefix (the multi-repo scope key; "" for a single-repo / in-memory
// graph). The go/packages load is already scoped to repoRoot; scoping the
// graph-side symbol count and implements-edge scan to one repo stops a
// multi-repo warmup from paying a whole-graph AllNodes / AllEdges walk per
// repo. Implementing this makes the provider a semantic.RepoScopedProvider,
// so the manager dispatches it per repo with the repo's prefix.
func (p *Provider) EnrichRepo(g graph.Store, repoPrefix, repoRoot string) (*semantic.EnrichResult, error) {
	start := time.Now()

	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("absolute path: %w", err)
	}

	// Load all packages with type information.
	pkgs, fset, err := p.loadPackages(absRoot)
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}

	// Stash the loaded state, keyed by this repo's root, so LookupTypeAtLine
	// can serve per-binding type queries from the contract pipeline without
	// paying the 5-10s loadPackages cost again — and so a later repo's enrich
	// does not clobber this repo's state before its contracts are extracted.
	p.stateMu.Lock()
	if p.stashes == nil {
		p.stashes = make(map[string]*goStash)
	}
	p.stashes[absRoot] = &goStash{pkgs: pkgs, fset: fset, absRoot: absRoot, lastUsed: time.Now()}
	p.evictLocked()
	p.stateMu.Unlock()
	// Release idle programs in the background so a quiet daemon doesn't hold
	// every enriched repo's type tables until the next enrich.
	p.startSweeper()

	result := &semantic.EnrichResult{
		Provider: p.Name(),
		Language: "go",
	}

	// Serialise the graph-touching work below on the backend resolve mutex —
	// the same lock every other edge-mutating pass holds — so this pass can run
	// concurrently with other repos' enrichment. loadPackages (the expensive
	// go/packages load) already ran above, outside the lock, so it still
	// overlaps across repos; only the in-memory graph build is serialised.
	rmu := g.ResolveMutex()
	rmu.Lock()
	defer rmu.Unlock()

	// Build symbol map: go/types objects → Gortex node IDs.
	symMap := semantic.NewSymbolMap()
	objToNode := make(map[types.Object]string) // types.Object → Gortex node ID

	// Phase 1: Map definitions.
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}

		for ident, obj := range pkg.TypesInfo.Defs {
			if obj == nil || ident.Pos() == token.NoPos {
				continue
			}

			pos := fset.Position(ident.Pos())
			relPath := relativePath(pos.Filename, absRoot)
			if relPath == "" {
				continue
			}

			node := semantic.MatchNodeByFileLine(g, relPath, pos.Line)
			if node == nil {
				node = semantic.MatchNodeByNameInFile(g, ident.Name, relPath)
			}
			if node != nil {
				objID := objectID(obj)
				symMap.Add(objID, node.ID)
				objToNode[obj] = node.ID
				result.SymbolsCovered++
			}
		}
	}

	// Count total Go symbols in this repo via the indexed repo-scoped scan
	// rather than a whole-graph AllNodes walk (which, in a multi-repo graph,
	// also wrongly counted every other repo's Go nodes against this repo's
	// coverage).
	for _, n := range repoGoNodes(g, repoPrefix) {
		if n.Kind != graph.KindFile && n.Kind != graph.KindImport {
			result.SymbolsTotal++
		}
	}
	if result.SymbolsTotal > 0 {
		result.CoveragePercent = float64(result.SymbolsCovered) / float64(result.SymbolsTotal) * 100
	}

	// Externals attribution: every Use of an external symbol becomes
	// an EdgeCalls / EdgeReferences targeting a freshly materialised
	// `ext::go:<importPath>::<name>` node, which itself carries an
	// EdgeDependsOnModule to the owning KindModule. Previously the
	// resolver left these calls pointing at stub strings
	// (`stdlib::fmt::Println`, `dep::github.com/.../foo::Bar`) that no
	// node holds; goanalysis upgrades them to real graph nodes with
	// LSP-grade origin.
	externals := newExternalsAttribution(g, pkgs, p.Name())

	// Phase 2: Process references — confirm/add edges. External symbols
	// are routed through externals.resolveSymbol so calls into stdlib
	// and module-cache packages land on real graph nodes rather than
	// the resolver's stub strings.
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}

		for ident, obj := range pkg.TypesInfo.Uses {
			if obj == nil || ident.Pos() == token.NoPos {
				continue
			}

			pos := fset.Position(ident.Pos())
			relPath := relativePath(pos.Filename, absRoot)
			if relPath == "" {
				continue
			}

			// Find the containing Gortex node (the caller).
			callerNode := findContainingFunc(g, pkgs, fset, absRoot, pos)
			if callerNode == nil {
				continue
			}

			// Find the target Gortex node (the definition being used).
			targetNodeID, ok := objToNode[obj]
			external := false
			if !ok {
				targetNodeID = externals.resolveSymbol(obj)
				if targetNodeID == "" {
					continue
				}
				external = true
			}

			if callerNode.ID == targetNodeID {
				continue
			}

			// External: claim a resolver-stub edge if one exists, else
			// add a fresh edge. Internal: confirm or add as before.
			if external {
				importPath := obj.Pkg().Path()
				if upgraded := externals.claimAndUpgradeStub(callerNode.ID, importPath, obj, targetNodeID, pos.Line); upgraded != nil {
					result.EdgesConfirmed++
					continue
				}
				existing := semantic.FindEdgeByTarget(g, callerNode.ID, targetNodeID)
				if existing != nil {
					if existing.Confidence < 1.0 {
						semantic.ConfirmEdge(existing, p.Name())
						result.EdgesConfirmed++
					}
					continue
				}
				kind := inferEdgeKindFromObj(obj)
				if kind != "" {
					semantic.AddSemanticEdge(g, callerNode.ID, targetNodeID, kind,
						relPath, pos.Line, p.Name())
					result.EdgesAdded++
				}
				continue
			}

			// Check if an edge already exists.
			existing := semantic.FindEdgeByTarget(g, callerNode.ID, targetNodeID)
			if existing != nil {
				if existing.Confidence < 1.0 {
					semantic.ConfirmEdge(existing, p.Name())
					result.EdgesConfirmed++
				}
			} else {
				// Determine edge kind.
				kind := inferEdgeKindFromObj(obj)
				if kind != "" {
					semantic.AddSemanticEdge(g, callerNode.ID, targetNodeID, kind,
						relPath, pos.Line, p.Name())
					result.EdgesAdded++
				}
			}
		}
	}

	// Stitch the externals counters into the standard result. NodesEnriched
	// previously only incremented for in-repo type-meta enrichment; here
	// we surface the synthetic external + module nodes the externals
	// pass added so callers can see the full graph delta in one number.
	result.EdgesAdded += externals.edgesAdded + externals.edgesUpgraded
	result.NodesEnriched += externals.nodesAdded

	// Phase 3: Interface implementations via go/types.
	result.EdgesConfirmed += p.enrichImplements(g, pkgs, objToNode)
	result.EdgesAdded += p.addMissingImplements(g, pkgs, objToNode, absRoot)

	// Phase 4: node-driven type stamping. go/types has a Def — hence an exact
	// type — for every function, method, field, parameter and local variable.
	// The bottleneck is attaching each Def to its graph node. Phase 1 maps a
	// Def with MatchNodeByFileLine, which returns the innermost node whose
	// RANGE contains the ident line, so a function's params and locals collapse
	// onto the enclosing function node and never receive their own type — that
	// is why the old Defs→node stamping reached only ~20% of locals and ~24% of
	// params. Here we index every named Def by (file, name) and, for each graph
	// node, pick the same-named Def whose ident sits closest to the node's own
	// declaration line (within its range). That attaches a local/param to ITS
	// node rather than its enclosing function, lifting coverage to what go/types
	// actually knows (every named symbol).
	//
	// EnrichNodeMeta mutates Node.Meta in place; on disk backends the node is a
	// per-call GetNode reconstruction, so collect every stamped node and
	// round-trip it through the store at the end (one AddBatch) or the
	// semantic_type / return_type stamps are silently discarded. See
	// semantic.EnrichNodeMeta.
	type defEntry struct {
		line int
		obj  types.Object
	}
	defsByName := make(map[string][]defEntry) // key: rel \x00 name
	relSet := make(map[string]struct{})
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for ident, obj := range pkg.TypesInfo.Defs {
			if obj == nil || ident.Pos() == token.NoPos || ident.Name == "" || ident.Name == "_" {
				continue
			}
			pos := fset.Position(ident.Pos())
			rel := relativePath(pos.Filename, absRoot)
			if rel == "" {
				continue
			}
			defsByName[rel+"\x00"+ident.Name] = append(defsByName[rel+"\x00"+ident.Name], defEntry{pos.Line, obj})
			relSet[rel] = struct{}{}
		}
	}
	var stampedNodes []*graph.Node
	for rel := range relSet {
		for _, node := range g.GetFileNodes(rel) {
			if node.Kind == graph.KindFile || node.Kind == graph.KindImport || node.Name == "" {
				continue
			}
			// Among same-named Defs in this file, pick the one whose ident line
			// is closest to the node's start line and falls within its range —
			// this distinguishes two locals of the same name in one function.
			best := types.Object(nil)
			bestDist := 1 << 30
			for _, e := range defsByName[rel+"\x00"+node.Name] {
				if e.line < node.StartLine-1 || e.line > node.EndLine+1 {
					continue
				}
				d := e.line - node.StartLine
				if d < 0 {
					d = -d
				}
				if d < bestDist {
					bestDist = d
					best = e.obj
				}
			}
			if best == nil {
				continue
			}

			didStamp := false
			if typeStr := types.TypeString(best.Type(), nil); typeStr != "" && typeStr != "invalid type" {
				semantic.EnrichNodeMeta(node, "semantic_type", typeStr, p.Name())
				result.NodesEnriched++
				didStamp = true
			}
			// Add return type for functions.
			if fn, ok := best.(*types.Func); ok {
				if sig, ok := fn.Type().(*types.Signature); ok && sig.Results().Len() > 0 {
					semantic.EnrichNodeMeta(node, "return_type", types.TypeString(sig.Results(), nil), p.Name())
					didStamp = true
				}
			}
			if didStamp {
				stampedNodes = append(stampedNodes, node)
			}
		}
	}
	if len(stampedNodes) > 0 {
		g.AddBatch(stampedNodes, nil)
	}

	result.DurationMs = time.Since(start).Milliseconds()
	return result, nil
}

func (p *Provider) EnrichFile(g graph.Store, repoRoot, filePath string) (*semantic.EnrichResult, error) {
	// go/types can do incremental loading per package, but for simplicity
	// we re-enrich the whole graph. The manager's debounce prevents thrashing.
	return nil, nil
}

// LookupTypeAtLine returns the resolved type name of the first
// short_var_declaration / var_spec / typed declaration whose start
// line matches `line` in the file at `filePath`. Returns ("", false)
// when:
//   - Enrich hasn't been called (no cached state)
//   - filePath isn't in any loaded package
//   - no typed declaration is found at `line`
//   - the type can't be resolved via go/types
//
// This is the lsp_resolved upgrade tier referenced in
// spec-contract-extraction.md §4.5: when the goanalysis provider
// has run, the contract pipeline can ask for compiler-grade type
// resolution at any line in the indexed source.
func (p *Provider) LookupTypeAtLine(filePath string, line int) (string, bool) {
	p.stateMu.Lock()
	p.evictLocked()
	stashes := make([]*goStash, 0, len(p.stashes))
	for _, s := range p.stashes {
		stashes = append(stashes, s)
	}
	p.stateMu.Unlock()
	if len(stashes) == 0 {
		return "", false
	}
	// Try every repo's stash; the file resolves under exactly one repo root.
	target := normalizeRelPath(filePath)
	for _, st := range stashes {
		if len(st.pkgs) == 0 || st.fset == nil || st.absRoot == "" {
			continue
		}
		for _, pkg := range st.pkgs {
			if pkg.TypesInfo == nil {
				continue
			}
			for _, syntax := range pkg.Syntax {
				if syntax == nil {
					continue
				}
				pos := st.fset.Position(syntax.Pos())
				if normalizeRelPath(relativePath(pos.Filename, st.absRoot)) != target {
					continue
				}
				// This file belongs to st: keep the repo's program warm so the
				// idle sweeper doesn't release it mid-contract-pass.
				p.touch(st)
				if t, ok := lookupTypeAtLineInFile(syntax, pkg.TypesInfo, st.fset, line); ok {
					return t, true
				}
			}
		}
	}
	return "", false
}

// touch bumps a stash's lastUsed under the lock so the idle sweeper
// treats an actively-queried repo as live.
func (p *Provider) touch(st *goStash) {
	p.stateMu.Lock()
	st.lastUsed = time.Now()
	p.stateMu.Unlock()
}

// evictLocked releases stashes idle past stashTTL and, if still over the
// count ceiling, the least-recently-used ones. The caller holds stateMu.
// Dropping a stash frees one repo's whole type-checked program for GC.
func (p *Provider) evictLocked() {
	if len(p.stashes) == 0 {
		return
	}
	ttl := p.stashTTL
	if ttl <= 0 {
		ttl = defaultStashTTL
	}
	now := time.Now()
	for root, st := range p.stashes {
		if now.Sub(st.lastUsed) > ttl {
			delete(p.stashes, root)
		}
	}
	max := p.maxStashes
	if max <= 0 {
		max = defaultMaxStashes
	}
	for len(p.stashes) > max {
		var lruRoot string
		var lruTime time.Time
		for root, st := range p.stashes {
			if lruRoot == "" || st.lastUsed.Before(lruTime) {
				lruRoot, lruTime = root, st.lastUsed
			}
		}
		if lruRoot == "" {
			break
		}
		delete(p.stashes, lruRoot)
	}
}

// startSweeper lazily launches one background goroutine that periodically
// releases idle stashes, so a daemon that has gone quiet drops its
// retained type-checked programs instead of holding them until the next
// enrich. Stopped by Close.
func (p *Provider) startSweeper() {
	p.sweepOnce.Do(func() {
		if p.stopSweep == nil {
			p.stopSweep = make(chan struct{})
		}
		interval := p.stashTTL
		if interval <= 0 {
			interval = defaultStashTTL
		}
		if interval /= 2; interval < 30*time.Second {
			interval = 30 * time.Second
		}
		stop := p.stopSweep
		go func() {
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					p.stateMu.Lock()
					p.evictLocked()
					p.stateMu.Unlock()
				}
			}
		}()
	})
}

// envPositiveInt / envPositiveDuration read an operator override, falling
// back to def when the variable is unset or unparseable.
func envPositiveInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envPositiveDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// lookupTypeAtLineInFile walks the file's AST and returns the type
// name of the first declaration at `line` whose LHS the type info
// table has a type for.
func lookupTypeAtLineInFile(file *ast.File, info *types.Info, fset *token.FileSet, line int) (string, bool) {
	var found string
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil || found != "" {
			return false
		}
		startLine := fset.Position(n.Pos()).Line
		if startLine != line {
			// Keep descending if this node spans the target.
			endLine := fset.Position(n.End()).Line
			return startLine <= line && endLine >= line
		}
		// We're at the target line. Try to extract a type from the
		// most common declaration shapes.
		switch d := n.(type) {
		case *ast.AssignStmt:
			if name := typeNameFromAssign(d, info); name != "" {
				found = name
			}
		case *ast.GenDecl:
			if name := typeNameFromGenDecl(d, info); name != "" {
				found = name
			}
		case *ast.DeclStmt:
			if gd, ok := d.Decl.(*ast.GenDecl); ok {
				if name := typeNameFromGenDecl(gd, info); name != "" {
					found = name
				}
			}
		}
		return found == ""
	})
	return found, found != ""
}

// typeNameFromAssign reads the LHS type from a short var declaration
// (`x := f()` or `x := Foo{...}`). Returns the underlying named
// type's name.
func typeNameFromAssign(stmt *ast.AssignStmt, info *types.Info) string {
	if len(stmt.Lhs) == 0 || len(stmt.Rhs) == 0 {
		return ""
	}
	for i, lhs := range stmt.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || ident.Name == "_" {
			continue
		}
		obj := info.Defs[ident]
		if obj == nil {
			obj = info.Uses[ident]
		}
		if obj != nil {
			if name := unwrapTypeName(obj.Type()); name != "" {
				return name
			}
		}
		// Fall back to the RHS expression's type.
		var rhs ast.Expr
		if i < len(stmt.Rhs) {
			rhs = stmt.Rhs[i]
		} else if len(stmt.Rhs) == 1 {
			rhs = stmt.Rhs[0]
		}
		if rhs != nil {
			if t, ok := info.Types[rhs]; ok && t.Type != nil {
				if name := unwrapTypeName(t.Type); name != "" {
					return name
				}
			}
		}
	}
	return ""
}

// typeNameFromGenDecl handles `var x Foo` / `var x = Foo{...}`.
func typeNameFromGenDecl(decl *ast.GenDecl, info *types.Info) string {
	for _, spec := range decl.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range vs.Names {
			if name.Name == "_" {
				continue
			}
			obj := info.Defs[name]
			if obj != nil {
				if t := unwrapTypeName(obj.Type()); t != "" {
					return t
				}
			}
			if vs.Type != nil {
				if t, ok := info.Types[vs.Type]; ok && t.Type != nil {
					if u := unwrapTypeName(t.Type); u != "" {
						return u
					}
				}
			}
			if i < len(vs.Values) {
				if t, ok := info.Types[vs.Values[i]]; ok && t.Type != nil {
					if u := unwrapTypeName(t.Type); u != "" {
						return u
					}
				}
			}
		}
	}
	return ""
}

// unwrapTypeName strips slice/pointer/array wrappers and returns the
// underlying named type's bare name. Returns "" for primitives,
// interfaces, and untyped expressions.
func unwrapTypeName(t types.Type) string {
	if t == nil {
		return ""
	}
	for {
		switch x := t.(type) {
		case *types.Pointer:
			t = x.Elem()
		case *types.Slice:
			t = x.Elem()
		case *types.Array:
			t = x.Elem()
		default:
			named, ok := t.(*types.Named)
			if !ok {
				return ""
			}
			return named.Obj().Name()
		}
	}
}

// normalizeRelPath collapses a/./b → a/b and uses forward slashes,
// so OS-dependent path separators don't trip the comparison.
func normalizeRelPath(p string) string {
	if p == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// loadPackages loads all Go packages in the given directory with type information.
func (p *Provider) loadPackages(dir string) ([]*packages.Package, *token.FileSet, error) {
	mode := packages.NeedName |
		packages.NeedFiles |
		packages.NeedImports |
		packages.NeedDeps |
		packages.NeedTypes |
		packages.NeedTypesInfo |
		packages.NeedSyntax |
		// NeedModule populates pkg.Module so the externals pass can
		// classify imports as stdlib (Module == nil), module_cache
		// (Module != nil && !Main), or main (Module.Main). Without
		// it the loader returns nil for every Module field and we
		// can't tell stdlib calls from internal-package calls.
		packages.NeedModule

	cfg := &packages.Config{
		Mode:  mode,
		Dir:   dir,
		Tests: p.includeTest,
		Fset:  token.NewFileSet(),
	}

	patterns := []string{"./..."}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, nil, err
	}

	// Filter out packages with errors (they may have partial type info).
	var valid []*packages.Package
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			p.logger.Debug("package has errors, using partial info",
				zap.String("pkg", pkg.PkgPath),
				zap.Int("errors", len(pkg.Errors)),
			)
		}
		if pkg.TypesInfo != nil {
			valid = append(valid, pkg)
		}
	}

	return valid, cfg.Fset, nil
}

// repoGoNodes returns the repo's Go-language nodes via the indexed
// GetRepoNodes scan, falling back to a language-filtered AllNodes pass for
// the embedded single-repo ("") path where GetRepoNodes can come back empty.
func repoGoNodes(g graph.Store, repoPrefix string) []*graph.Node {
	filter := func(nodes []*graph.Node) []*graph.Node {
		out := make([]*graph.Node, 0, len(nodes))
		for _, n := range nodes {
			if n.Language == "go" && n.RepoPrefix == repoPrefix {
				out = append(out, n)
			}
		}
		return out
	}
	out := filter(g.GetRepoNodes(repoPrefix))
	if len(out) == 0 && repoPrefix == "" {
		return filter(g.AllNodes())
	}
	return out
}

// enrichImplements confirms existing EdgeImplements edges using go/types.
func (p *Provider) enrichImplements(g graph.Store, pkgs []*packages.Package, objToNode map[types.Object]string) int {
	confirmed := 0

	// Collect all interfaces from the loaded packages.
	ifaceTypes := make(map[string]*types.Interface) // Gortex node ID → interface type
	for obj, nodeID := range objToNode {
		if tn, ok := obj.(*types.TypeName); ok {
			if iface, ok := tn.Type().Underlying().(*types.Interface); ok {
				ifaceTypes[nodeID] = iface
			}
		}
	}

	// Check existing EdgeImplements edges. Iterate the kind-indexed edge set
	// (not a whole-graph AllEdges scan, but still graph-wide for this kind) so
	// a cross-repo implements edge — concrete type in another repo, interface
	// in this repo's loaded packages — is still confirmed, matching the
	// original behavior.
	for e := range g.EdgesByKind(graph.EdgeImplements) {
		fromNode := g.GetNode(e.From)
		if fromNode == nil || fromNode.Language != "go" {
			continue
		}
		if e.Confidence >= 1.0 {
			continue
		}

		// If we have type info for both sides, verify.
		if _, ok := ifaceTypes[e.To]; ok {
			semantic.ConfirmEdge(e, p.Name())
			confirmed++
		}
	}

	return confirmed
}

// addMissingImplements discovers interface implementations that tree-sitter missed.
func (p *Provider) addMissingImplements(g graph.Store, pkgs []*packages.Package, objToNode map[types.Object]string, absRoot string) int {
	added := 0

	// Collect interfaces and concrete types.
	type ifaceEntry struct {
		nodeID string
		iface  *types.Interface
	}
	type concreteEntry struct {
		nodeID string
		typ    types.Type
		obj    types.Object
	}

	var ifaces []ifaceEntry
	var concretes []concreteEntry

	for obj, nodeID := range objToNode {
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		if iface, ok := tn.Type().Underlying().(*types.Interface); ok {
			ifaces = append(ifaces, ifaceEntry{nodeID: nodeID, iface: iface})
		} else {
			concretes = append(concretes, concreteEntry{nodeID: nodeID, typ: tn.Type(), obj: obj})
		}
	}

	// Check each (concrete, interface) pair.
	for _, c := range concretes {
		for _, i := range ifaces {
			if c.nodeID == i.nodeID {
				continue
			}
			// Check both T and *T.
			if types.Implements(c.typ, i.iface) || types.Implements(types.NewPointer(c.typ), i.iface) {
				existing := semantic.FindMatchingEdge(g, c.nodeID, i.nodeID, graph.EdgeImplements)
				if existing == nil {
					cNode := g.GetNode(c.nodeID)
					if cNode != nil {
						semantic.AddSemanticEdge(g, c.nodeID, i.nodeID, graph.EdgeImplements,
							cNode.FilePath, cNode.StartLine, p.Name())
						added++
					}
				}
			}
		}
	}

	return added
}

// findContainingFunc finds the Gortex function/method node that contains the given position.
func findContainingFunc(g graph.Store, pkgs []*packages.Package, fset *token.FileSet, absRoot string, pos token.Position) *graph.Node {
	relPath := relativePath(pos.Filename, absRoot)
	if relPath == "" {
		return nil
	}

	nodes := g.GetFileNodes(relPath)
	var best *graph.Node
	bestSize := int(^uint(0) >> 1)
	for _, n := range nodes {
		if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
			continue
		}
		if n.StartLine <= pos.Line && pos.Line <= n.EndLine {
			size := n.EndLine - n.StartLine
			if size < bestSize {
				best = n
				bestSize = size
			}
		}
	}
	return best
}

// inferEdgeKindFromObj determines the edge kind from a go/types object.
func inferEdgeKindFromObj(obj types.Object) graph.EdgeKind {
	switch obj.(type) {
	case *types.Func:
		return graph.EdgeCalls
	case *types.TypeName:
		return graph.EdgeReferences
	case *types.Var:
		return graph.EdgeReferences
	case *types.Const:
		return graph.EdgeReferences
	default:
		return ""
	}
}

// objectID creates a stable string ID for a go/types object.
func objectID(obj types.Object) string {
	if obj.Pkg() != nil {
		return obj.Pkg().Path() + "." + obj.Name()
	}
	return obj.Name()
}

// relativePath converts an absolute file path to a repo-relative path.
func relativePath(absPath, repoRoot string) string {
	// Skip files outside the repo (stdlib, dependencies).
	if !strings.HasPrefix(absPath, repoRoot) {
		return ""
	}
	rel, err := filepath.Rel(repoRoot, absPath)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

// Ensure ast is used.
var _ = (*ast.File)(nil)
