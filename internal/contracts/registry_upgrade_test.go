package contracts

import "testing"

func TestUpgradeBareTypeRefs_ResolvesSingleMatch(t *testing.T) {
	r := NewRegistry()
	r.AddAll([]Contract{{
		ID:   "http::POST::/users",
		Type: ContractHTTP,
		Role: RoleProvider,
		Meta: map[string]any{
			"request_type":  "CreateReq",       // bare
			"response_type": "pkg/api.go::Foo", // already a symbol ID
		},
	}}, "api")

	r.UpgradeBareTypeRefs(func(name, repoHint string) []string {
		if name == "CreateReq" {
			return []string{"api/models/req.go::CreateReq"}
		}
		return nil
	})

	c := r.All()[0]
	if got := c.Meta["request_type"]; got != "api/models/req.go::CreateReq" {
		t.Errorf("request_type: want upgraded, got %q", got)
	}
	if got := c.Meta["response_type"]; got != "pkg/api.go::Foo" {
		t.Errorf("response_type: want left alone, got %q", got)
	}
}

func TestUpgradeBareTypeRefs_LeavesAmbiguousUntouched(t *testing.T) {
	r := NewRegistry()
	r.AddAll([]Contract{{
		ID:   "http::GET::/x",
		Type: ContractHTTP,
		Role: RoleProvider,
		Meta: map[string]any{
			"response_type": "User",
		},
	}}, "api")

	r.UpgradeBareTypeRefs(func(name, repoHint string) []string {
		return []string{
			"api/models/a.go::User",
			"other/b.go::User",
		}
	})

	c := r.All()[0]
	if got := c.Meta["response_type"]; got != "User" {
		t.Errorf("response_type: want bare 'User' (ambiguous), got %q", got)
	}
}

func TestUpgradeBareTypeRefs_PrefersSameRepoHint(t *testing.T) {
	r := NewRegistry()
	r.AddAll([]Contract{{
		ID:   "http::POST::/x",
		Type: ContractHTTP,
		Role: RoleProvider,
		Meta: map[string]any{
			"request_type": "Shared",
		},
	}}, "svcA")

	// Lookup returns only same-repo match (svcA), mirroring the
	// indexer's filter logic which returns `same` when populated.
	r.UpgradeBareTypeRefs(func(name, repoHint string) []string {
		if repoHint == "svcA" {
			return []string{"svcA/models.go::Shared"}
		}
		return []string{"svcB/models.go::Shared", "svcC/models.go::Shared"}
	})

	c := r.All()[0]
	if got := c.Meta["request_type"]; got != "svcA/models.go::Shared" {
		t.Errorf("request_type: want svcA upgrade, got %q", got)
	}
}
