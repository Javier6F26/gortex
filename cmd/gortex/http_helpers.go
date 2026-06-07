package main

import (
	"fmt"
	"net"
	"path/filepath"

	"github.com/zzet/gortex/internal/server"
)

// httpTokenRequirementError returns a refusal when an HTTP surface is
// bound to a non-localhost address without an auth token, and nil
// otherwise. Localhost binds may run unauthenticated; externally
// reachable binds must carry a token (flag or $GORTEX_DAEMON_HTTP_TOKEN).
func httpTokenRequirementError(addr, token string) error {
	if !isLocalhostBind(addr) && token == "" {
		return fmt.Errorf("--http-addr %q is non-localhost; --http-auth-token (or $GORTEX_DAEMON_HTTP_TOKEN) is required", addr)
	}
	return nil
}

// isLocalhostBind reports whether a bind address is a loopback host, used
// to decide whether the HTTP surface may run without an auth token. The
// address may carry a port (e.g. "127.0.0.1:7411", "localhost:7411",
// "[::1]:7411"); a wildcard bind ("0.0.0.0:7411", ":7411") or any
// non-loopback host is treated as externally reachable and so NOT
// localhost (a token is then required).
func isLocalhostBind(bind string) bool {
	host := bind
	if h, _, err := net.SplitHostPort(bind); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// resolveServerID loads or creates the per-machine server id. When
// cacheDir is empty the id lives alongside other gortex cache files
// (~/.gortex/cache/server.id); otherwise cacheDir/server.id.
func resolveServerID(cacheDir string) (string, error) {
	path := filepath.Join(cacheDir, "server.id")
	if cacheDir == "" {
		def, err := server.DefaultServerIDPath()
		if err != nil {
			return "", err
		}
		path = def
	}
	return server.LoadOrCreateServerID(path)
}
