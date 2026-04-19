package contracts

import (
	"testing"
)

// Helpers -------------------------------------------------------------------

func makeContract(role Role, repo, contractID string, meta map[string]any) Contract {
	return Contract{
		ID:         contractID,
		Type:       ContractHTTP,
		Role:       role,
		RepoPrefix: repo,
		Meta:       meta,
	}
}

func buildRegistry(contracts ...Contract) *Registry {
	r := NewRegistry()
	for _, c := range contracts {
		r.Add(c)
	}
	return r
}

// shapeMap is a trivial in-memory ShapeLookup keyed by symbol ID.
type shapeMap map[string]*Shape

func (sm shapeMap) lookup(id string) *Shape { return sm[id] }

func findIssue(t *testing.T, issues []ContractIssue, kind string, field string) *ContractIssue {
	t.Helper()
	for i := range issues {
		if issues[i].Kind == kind && issues[i].Field == field {
			return &issues[i]
		}
	}
	kinds := make([]string, 0, len(issues))
	for _, is := range issues {
		kinds = append(kinds, is.Kind+"("+is.Field+")")
	}
	t.Fatalf("missing %s(%s) in %v", kind, field, kinds)
	return nil
}

// Tests ---------------------------------------------------------------------

func TestValidate_OrphanConsumer(t *testing.T) {
	reg := buildRegistry(
		makeContract(RoleConsumer, "web", "http::GET::/ghost", map[string]any{}),
	)
	got := Validate(reg, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 issue, got %d", len(got))
	}
	if got[0].Kind != IssueOrphanConsumer {
		t.Errorf("kind = %q", got[0].Kind)
	}
	if got[0].Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", got[0].Severity)
	}
}

func TestValidate_OrphanProvider(t *testing.T) {
	reg := buildRegistry(
		makeContract(RoleProvider, "api", "http::GET::/health", map[string]any{}),
	)
	got := Validate(reg, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 issue, got %d", len(got))
	}
	if got[0].Kind != IssueOrphanProvider || got[0].Severity != SeverityInfo {
		t.Errorf("kind=%s severity=%s", got[0].Kind, got[0].Severity)
	}
}

// Response: provider removed a field that consumer still reads — breaking.
func TestValidate_Response_FieldRemoved_Breaking(t *testing.T) {
	reg := buildRegistry(
		makeContract(RoleProvider, "api", "http::GET::/users", map[string]any{
			"response_type": "api/resp.go::UserResp",
		}),
		makeContract(RoleConsumer, "web", "http::GET::/users", map[string]any{
			"response_type": "web/types.ts::UserResp",
		}),
	)
	shapes := shapeMap{
		"api/resp.go::UserResp": {
			Kind: "struct",
			Fields: []ShapeField{
				{Name: "id", Type: "string", Required: true},
			},
		},
		"web/types.ts::UserResp": {
			Kind: "interface",
			Fields: []ShapeField{
				{Name: "id", Type: "string", Required: true},
				{Name: "email", Type: "string", Required: true}, // consumer still expects it
			},
		},
	}
	issues := Validate(reg, shapes.lookup)
	iss := findIssue(t, issues, IssueResponseFieldRemoved, "email")
	if iss.Severity != SeverityBreaking {
		t.Errorf("severity = %q, want breaking", iss.Severity)
	}
}

// Response: consumer optionally reads a missing field — downgraded warning.
func TestValidate_Response_FieldRemoved_Optional_Warning(t *testing.T) {
	reg := buildRegistry(
		makeContract(RoleProvider, "api", "http::GET::/users", map[string]any{
			"response_type": "api/resp.go::UserResp",
		}),
		makeContract(RoleConsumer, "web", "http::GET::/users", map[string]any{
			"response_type": "web/types.ts::UserResp",
		}),
	)
	shapes := shapeMap{
		"api/resp.go::UserResp": {
			Fields: []ShapeField{{Name: "id", Type: "string", Required: true}},
		},
		"web/types.ts::UserResp": {
			Fields: []ShapeField{
				{Name: "id", Type: "string", Required: true},
				{Name: "nickname", Type: "string", Required: false},
			},
		},
	}
	issues := Validate(reg, shapes.lookup)
	iss := findIssue(t, issues, IssueResponseFieldRemoved, "nickname")
	if iss.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning (optional field absent)", iss.Severity)
	}
}

// Response: provider emits extra field consumer doesn't read — info.
func TestValidate_Response_FieldAdded_Info(t *testing.T) {
	reg := buildRegistry(
		makeContract(RoleProvider, "api", "http::GET::/x", map[string]any{"response_type": "api/resp.go::X"}),
		makeContract(RoleConsumer, "web", "http::GET::/x", map[string]any{"response_type": "web/resp.ts::X"}),
	)
	shapes := shapeMap{
		"api/resp.go::X": {Fields: []ShapeField{
			{Name: "id", Type: "string", Required: true},
			{Name: "notes", Type: "string", Required: false},
		}},
		"web/resp.ts::X": {Fields: []ShapeField{{Name: "id", Type: "string", Required: true}}},
	}
	issues := Validate(reg, shapes.lookup)
	iss := findIssue(t, issues, IssueResponseFieldAdded, "notes")
	if iss.Severity != SeverityInfo {
		t.Errorf("severity = %q, want info", iss.Severity)
	}
}

// Request: provider requires a field the consumer doesn't send — breaking.
func TestValidate_Request_RequiredFieldMissing_Breaking(t *testing.T) {
	reg := buildRegistry(
		makeContract(RoleProvider, "api", "http::POST::/users", map[string]any{
			"request_type": "api/req.go::CreateUser",
		}),
		makeContract(RoleConsumer, "web", "http::POST::/users", map[string]any{
			"request_type": "web/req.ts::CreateUser",
		}),
	)
	shapes := shapeMap{
		"api/req.go::CreateUser": {Fields: []ShapeField{
			{Name: "email", Type: "string", Required: true},
			{Name: "password", Type: "string", Required: true},
		}},
		"web/req.ts::CreateUser": {Fields: []ShapeField{
			{Name: "email", Type: "string", Required: true},
			// missing password
		}},
	}
	issues := Validate(reg, shapes.lookup)
	iss := findIssue(t, issues, IssueRequestFieldRequired, "password")
	if iss.Severity != SeverityBreaking {
		t.Errorf("severity = %q, want breaking", iss.Severity)
	}
}

// Request: consumer sends a field the provider doesn't accept — info.
func TestValidate_Request_ExtraField_Info(t *testing.T) {
	reg := buildRegistry(
		makeContract(RoleProvider, "api", "http::POST::/x", map[string]any{"request_type": "api/req.go::X"}),
		makeContract(RoleConsumer, "web", "http::POST::/x", map[string]any{"request_type": "web/req.ts::X"}),
	)
	shapes := shapeMap{
		"api/req.go::X": {Fields: []ShapeField{{Name: "id", Type: "string", Required: true}}},
		"web/req.ts::X": {Fields: []ShapeField{
			{Name: "id", Type: "string", Required: true},
			{Name: "debug", Type: "boolean"},
		}},
	}
	issues := Validate(reg, shapes.lookup)
	iss := findIssue(t, issues, IssueRequestFieldExtra, "debug")
	if iss.Severity != SeverityInfo {
		t.Errorf("severity = %q, want info", iss.Severity)
	}
}

// Shared field with different types across provider / consumer.
func TestValidate_TypeChanged_Breaking(t *testing.T) {
	reg := buildRegistry(
		makeContract(RoleProvider, "api", "http::GET::/thing", map[string]any{"response_type": "api/resp.go::Thing"}),
		makeContract(RoleConsumer, "web", "http::GET::/thing", map[string]any{"response_type": "web/resp.ts::Thing"}),
	)
	shapes := shapeMap{
		"api/resp.go::Thing": {Fields: []ShapeField{{Name: "id", Type: "int64", Required: true}}},
		"web/resp.ts::Thing": {Fields: []ShapeField{{Name: "id", Type: "string", Required: true}}},
	}
	issues := Validate(reg, shapes.lookup)
	iss := findIssue(t, issues, IssueResponseFieldTypeChanged, "id")
	if iss.Severity != SeverityBreaking {
		t.Errorf("severity = %q, want breaking", iss.Severity)
	}
	if iss.Details == "" {
		t.Error("expected details with provider/consumer types")
	}
}

// Shape comparison should be tolerant: `*User` (Go pointer) ≡ `User | null` (TS)
// ≡ `User | None` (Python). Otherwise every cross-language contract would
// look broken.
func TestValidate_TypesCompatible_AcrossLanguages(t *testing.T) {
	cases := [][2]string{
		{"*User", "User | null"},
		{"User", "User | None"},
		{"[]User", "Array<User>"},
		{"List<User>", "User[]"},
		{"api.User", "User"},
	}
	for _, c := range cases {
		if !typesCompatible(c[0], c[1]) {
			t.Errorf("typesCompatible(%q, %q) = false, want true", c[0], c[1])
		}
	}
}

// Missing shape on one side → warning that diff was skipped, not breaking.
func TestValidate_TypeUnknown_Warning(t *testing.T) {
	reg := buildRegistry(
		makeContract(RoleProvider, "api", "http::GET::/x", map[string]any{"response_type": "api/resp.go::X"}),
		makeContract(RoleConsumer, "web", "http::GET::/x", map[string]any{"response_type": "web/resp.ts::X"}),
	)
	// Only provider shape is known; consumer type is a symbol ID we
	// can't resolve (cross-repo, not re-indexed).
	shapes := shapeMap{
		"api/resp.go::X": {Fields: []ShapeField{{Name: "id", Type: "string", Required: true}}},
	}
	issues := Validate(reg, shapes.lookup)
	found := false
	for _, is := range issues {
		if is.Kind == IssueResponseTypeUnknown && is.Severity == SeverityWarning {
			found = true
		}
	}
	if !found {
		t.Errorf("expected response_type_unknown warning, got %#v", issues)
	}
}

// Matched pair with identical shapes → zero issues.
func TestValidate_MatchingShapes_NoIssues(t *testing.T) {
	reg := buildRegistry(
		makeContract(RoleProvider, "api", "http::POST::/users", map[string]any{
			"request_type":  "api/req.go::User",
			"response_type": "api/resp.go::User",
		}),
		makeContract(RoleConsumer, "web", "http::POST::/users", map[string]any{
			"request_type":  "web/req.ts::User",
			"response_type": "web/resp.ts::User",
		}),
	)
	shape := &Shape{Fields: []ShapeField{
		{Name: "id", Type: "string", Required: true},
		{Name: "email", Type: "string", Required: true},
	}}
	shapes := shapeMap{
		"api/req.go::User":  shape,
		"api/resp.go::User": shape,
		"web/req.ts::User":  shape,
		"web/resp.ts::User": shape,
	}
	issues := Validate(reg, shapes.lookup)
	if len(issues) != 0 {
		t.Errorf("want 0 issues, got %d: %#v", len(issues), issues)
	}
}
