package store_pg

import "testing"

func TestDSNHasParam(t *testing.T) {
	cases := []struct {
		dsn, key string
		want     bool
	}{
		{"postgres://u:p@h:5432/db?pool_max_conns=2", "pool_max_conns", true},
		{"postgres://u:p@h:5432/db?statement_timeout=1000&pool_max_conns=2", "pool_max_conns", true},
		{"postgres://u:p@h:5432/db?pool_min_conns=1", "pool_max_conns", false},
		{"postgres://u:p@h:5432/db", "pool_max_conns", false},
		{"host=h user=u pool_max_conns=3", "pool_max_conns", true},
		{"host=h user=u", "pool_max_conns", false},
		{"", "pool_max_conns", false},
	}
	for _, c := range cases {
		if got := dsnHasParam(c.dsn, c.key); got != c.want {
			t.Errorf("dsnHasParam(%q, %q) = %v, want %v", c.dsn, c.key, got, c.want)
		}
	}
}

func TestResolvePoolBound(t *testing.T) {
	dsnWith := "postgres://u:p@h/db?pool_max_conns=2"
	dsnWithout := "postgres://u:p@h/db"
	cases := []struct {
		name           string
		cfgVal         int
		dsn            string
		parsedVal, def int
		want           int
	}{
		{"config wins over DSN", 7, dsnWith, 2, 16, 7},
		{"DSN honored when config unset", 0, dsnWith, 2, 16, 2},
		{"default when neither", 0, dsnWithout, 4, 16, 16},
		{"pgx default ignored when DSN lacks the param", 0, dsnWithout, 8, 16, 16},
		{"min bound default zero", 0, dsnWithout, 0, 0, 0},
	}
	for _, c := range cases {
		if got := resolvePoolBound(c.cfgVal, c.dsn, "pool_max_conns", c.parsedVal, c.def); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSearchPathList(t *testing.T) {
	cases := []struct{ in, want string }{
		{"tenant_a", "tenant_a"},
		{"tenant_a,ext", "tenant_a, ext"},
		{" tenant_a , ext ", "tenant_a, ext"},
		{"gortex_test_1_2_TestX", "gortex_test_1_2_TestX"},
		{`"Tenant_A",ext`, `"Tenant_A", ext`},
		{"public", "public"},
		{"a,,b", "a, b"},
	}
	for _, c := range cases {
		if got := searchPathList(c.in); got != c.want {
			t.Errorf("searchPathList(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDefaultApplicationName(t *testing.T) {
	if got := defaultApplicationName(false); got != "gortex" {
		t.Errorf("writer: %q", got)
	}
	if got := defaultApplicationName(true); got != "gortex-follower" {
		t.Errorf("follower: %q", got)
	}
}
