package search

import (
	"slices"
	"testing"
)

func TestEquivalenceTable_CuratedLookup(t *testing.T) {
	tbl := NewEquivalenceTable(nil)

	// A word resolves to its class siblings, never itself.
	got := tbl.Expand("login")
	if len(got) == 0 {
		t.Fatal("Expand(login) returned no siblings")
	}
	if slices.Contains(got, "login") {
		t.Error("Expand must not include the query token itself")
	}
	for _, want := range []string{"auth", "authentication", "signin", "credential"} {
		if !slices.Contains(got, want) {
			t.Errorf("Expand(login) missing curated sibling %q; got %v", want, got)
		}
	}

	// Case-insensitive.
	if len(tbl.Expand("LOGIN")) != len(got) {
		t.Error("Expand should be case-insensitive")
	}

	// A word in no class returns nil.
	if tbl.Expand("zzzznotaword") != nil {
		t.Error("Expand of an unknown token should return nil")
	}

	// delete/remove are siblings.
	if !slices.Contains(tbl.Expand("delete"), "remove") {
		t.Error("delete should expand to remove")
	}
	if !slices.Contains(tbl.Expand("remove"), "delete") {
		t.Error("remove should expand to delete (symmetric)")
	}
}

func TestEquivalenceTable_RepoExtra(t *testing.T) {
	// A repo-custom class is added and its label joins the class.
	tbl := NewEquivalenceTable(map[string][]string{
		"widget": {"gadget", "gizmo"},
	})
	got := tbl.Expand("gadget")
	if !slices.Contains(got, "gizmo") || !slices.Contains(got, "widget") {
		t.Errorf("repo-extra class not wired: Expand(gadget) = %v", got)
	}

	// A repo extra whose label is a curated word extends that class.
	tbl2 := NewEquivalenceTable(map[string][]string{
		"auth": {"oauth", "sso"},
	})
	authSibs := tbl2.Expand("oauth")
	if !slices.Contains(authSibs, "login") {
		t.Errorf("repo extra keyed on a curated word should extend the curated class; got %v", authSibs)
	}
}

func TestEquivalenceTable_NilSafe(t *testing.T) {
	var tbl *EquivalenceTable
	if tbl.Expand("auth") != nil {
		t.Error("nil EquivalenceTable.Expand should return nil")
	}
	if tbl.ExpandRelated("auth") != nil {
		t.Error("nil EquivalenceTable.ExpandRelated should return nil")
	}
	if tbl.ClassCount() != 0 {
		t.Error("nil EquivalenceTable.ClassCount should be 0")
	}
	if tbl.RelatedClassCount() != 0 {
		t.Error("nil EquivalenceTable.RelatedClassCount should be 0")
	}
}

// TestEquivalenceTable_ExpandRelated confirms the concept-relatedness
// thesaurus bridges adjacent concepts (auth -> token / session)
// WITHOUT folding them into the synonym class.
func TestEquivalenceTable_ExpandRelated(t *testing.T) {
	tbl := NewEquivalenceTable(nil)

	// "auth" relates to the token and session classes.
	rel := tbl.ExpandRelated("auth")
	if len(rel) == 0 {
		t.Fatal("ExpandRelated(auth) returned no related concepts")
	}
	for _, want := range []string{"token", "jwt", "session"} {
		if !slices.Contains(rel, want) {
			t.Errorf("ExpandRelated(auth) missing related concept %q; got %v", want, rel)
		}
	}

	// Critical precision guard: the related concepts must NOT be
	// synonyms of "auth" -- Expand (the union-find synonym channel)
	// must still treat them as separate classes.
	authSyn := tbl.Expand("auth")
	for _, related := range []string{"token", "session", "jwt"} {
		if slices.Contains(authSyn, related) {
			t.Errorf("synonym Expand(auth) must NOT contain related concept %q -- "+
				"the thesaurus must stay separate from the union-find classes", related)
		}
	}

	// The relation is symmetric: "token" relates back to "auth".
	if !slices.Contains(tbl.ExpandRelated("token"), "auth") {
		t.Errorf("ExpandRelated(token) should relate back to auth; got %v", tbl.ExpandRelated("token"))
	}

	// The query token itself is never returned.
	if slices.Contains(tbl.ExpandRelated("auth"), "auth") {
		t.Error("ExpandRelated must not include the query token itself")
	}

	// A word in no class has no related concepts.
	if tbl.ExpandRelated("zzznotaword") != nil {
		t.Error("ExpandRelated of an unknown word should be nil")
	}
}

// TestEquivalenceTable_RelatedSeparateFromClasses is the structural
// invariant: adding the thesaurus must not have merged any concepts
// into one giant class. The class count stays at the curated baseline
// and the token / auth classes remain distinct.
func TestEquivalenceTable_RelatedSeparateFromClasses(t *testing.T) {
	tbl := NewEquivalenceTable(nil)
	if tbl.RelatedClassCount() == 0 {
		t.Fatal("the curated thesaurus should produce related-class edges")
	}
	// "auth" and "token" must be in DIFFERENT union-find classes.
	authIdx, ok1 := tbl.member["auth"]
	tokenIdx, ok2 := tbl.member["token"]
	if !ok1 || !ok2 {
		t.Fatal("auth and token must both be curated class members")
	}
	if authIdx == tokenIdx {
		t.Error("auth and token must stay in distinct classes -- the thesaurus must not merge them")
	}
}
