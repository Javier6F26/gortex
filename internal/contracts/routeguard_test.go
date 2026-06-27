package contracts

import "testing"

func TestIsLikelyHTTPRouteLiteral(t *testing.T) {
	cases := []struct {
		name    string
		literal string
		callee  string
		want    bool
	}{
		// --- Acceptance table ---
		{"plain route", "/api/users", "GET", true},
		{"versioned register", "/v1/x", "register", true},
		{"etc passwd", "/etc/passwd", "open", false},
		{"users home", "/Users/zzet/x", "open", false},
		{"ssh config home-relative", "~/.ssh/config", "open", false},
		{"bare config yaml", "config.yaml", "load", false},
		{"api schema json", "/api/schema.json", "GET", true},
		{"os.path.join callee", "/a", "os.path.join", false},

		// --- URL schemes ---
		{"http url", "http://x/y", "fetch", true},
		{"https url", "https://x", "fetch", true},
		{"file url", "file:///etc/x", "open", false},
		{"s3 url", "s3://bucket/k", "get", false},
		{"postgres url", "postgres://db/x", "connect", false},

		// --- Servable vs filesystem extensions ---
		{"config app json no marker", "/config/app.json", "load", false},
		{"health json marker", "/health.json", "GET", true},
		{"var log app log", "/var/log/app.log", "open", false},
		{"config app cfg", "/config/app.cfg", "load", false},
		{"config app toml", "/config/app.toml", "load", false},
		{"secret pem", "/secrets/server.pem", "read", false},
		{"sqlite db", "/data/app.sqlite", "open", false},
		{"api yaml marker", "/apis/openapi.yaml", "GET", true},
		{"metrics marker", "/metrics", "GET", true},
		{"graphql marker", "/graphql", "POST", true},

		// --- First-segment exact matching ---
		{"etcd not a root", "/etcd/keys", "get", true},
		{"versioned v10", "/v10/users", "GET", true},
		{"userspace not Users", "/userspace/x", "GET", true},

		// --- Hidden config segments ---
		{"aws creds rooted", "/some/path/.aws/credentials", "read", false},
		{"kube config rooted", "/x/.kube/config", "read", false},

		// --- Acceptable: js asset starts with slash, unknown ext ---
		{"js asset", "/app.js", "serve", true},

		// --- String-manipulation callees reject routey literals ---
		{"split callee", "/api/users", "split", false},
		{"bare join callee", "/api/users", "join", false},
		{"filepath.Join callee", "/api/users", "filepath.Join", false},
		{"path.Join callee", "/v1/x", "path.Join", false},
		{"path.join lower callee", "/v1/x", "path.join", false},

		// --- Non-rooted rejects ---
		{"relative file", "app.js", "import", false},
		{"dotfile relative", ".env", "load", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLikelyHTTPRouteLiteral(tc.literal, tc.callee); got != tc.want {
				t.Errorf("IsLikelyHTTPRouteLiteral(%q, %q) = %v, want %v",
					tc.literal, tc.callee, got, tc.want)
			}
		})
	}
}
