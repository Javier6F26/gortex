package store_pg

import (
	"context"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// DefaultPoolMaxConns is the default maximum number of connections in the pool.
// Set to NumCPU * 2 to provide concurrency for parallel enrichment, resolver
// passes, and background analysis while matching the SQLite backend's
// SetMaxOpenConns(runtime.NumCPU()) with headroom.
var DefaultPoolMaxConns = runtime.NumCPU() * 2

// DefaultPoolMaxConnLifetime is how long a connection lives before being
// recycled. 30 minutes matches the pgxpool default.
const DefaultPoolMaxConnLifetime = 30 * time.Minute

// DefaultPoolHealthCheckPeriod is how often the pool checks connection health.
const DefaultPoolHealthCheckPeriod = 30 * time.Second

// DefaultStatementTimeout bounds any single query so a reader cannot
// stall indefinitely (e.g. behind the bulk swap's ACCESS EXCLUSIVE lock).
const DefaultStatementTimeout = 30 * time.Second

// DefaultLockTimeout bounds how long a statement waits to acquire a lock
// before failing, so a reader blocked behind an exclusive lock fails fast
// (and is retried by the read-resilience path) instead of hanging.
const DefaultLockTimeout = 5 * time.Second

// Config holds the PostgreSQL connection configuration for the graph store.
type Config struct {
	// DSN is the PostgreSQL connection string.
	// Example: postgres://user:pass@host:5432/gortex
	DSN string

	// PoolMaxConns is the maximum number of connections in the pool.
	// Precedence: this field when > 0; otherwise a pool_max_conns
	// parameter in the DSN; otherwise DefaultPoolMaxConns.
	PoolMaxConns int

	// PoolMinConns is the minimum number of connections in the pool.
	// Precedence: this field when > 0; otherwise a pool_min_conns
	// parameter in the DSN; otherwise 0.
	PoolMinConns int

	// PoolMaxConnLifetime is the maximum age of a connection.
	// 0 means use DefaultPoolMaxConnLifetime.
	PoolMaxConnLifetime time.Duration

	// PoolHealthCheckPeriod is how often the pool checks connection health.
	// 0 means use DefaultPoolHealthCheckPeriod.
	PoolHealthCheckPeriod time.Duration

	// Schema is an optional search_path applied to every connection
	// (SET search_path TO <Schema>). It is either a single schema name or
	// a comma-separated list, e.g. "tenant_a,ext" when the pg_trgm/vector
	// extensions live in a shared schema. Entries are passed to PostgreSQL
	// verbatim (unquoted names fold to lowercase as usual). This is how
	// several tenants share one database with one schema each (see
	// docs/pg-setup.md). Empty means use the database default. Also used
	// by tests for per-test schema isolation.
	Schema string

	// StatementTimeout is the per-query timeout applied as the
	// statement_timeout runtime parameter. 0 means use
	// DefaultStatementTimeout. It is overridden by an explicit
	// statement_timeout in the DSN only when this field is 0.
	StatementTimeout time.Duration

	// LockTimeout is the lock-acquisition timeout applied as the
	// lock_timeout runtime parameter. 0 means use DefaultLockTimeout.
	// It is overridden by an explicit lock_timeout in the DSN only when
	// this field is 0.
	LockTimeout time.Duration

	// ReadOnly opens the store in read-only mode: Open never runs schema
	// migrations (it fails with SchemaVersionMismatchError when the stored
	// version differs from the expected one) and every mutating method
	// refuses. This is the foundation for follower daemons pointed at a
	// physical read replica.
	ReadOnly bool

	// Logger, when set, receives WARN logs for degraded reads and refused
	// writes. nil means logging is disabled.
	Logger *zap.Logger
}

// openPool creates a pgxpool from the configuration.
func (c *Config) openPool(ctx context.Context) (*pgxpool.Pool, error) {
	if c.DSN == "" {
		return nil, fmt.Errorf("store_pg: DSN is required")
	}

	maxLifetime := c.PoolMaxConnLifetime
	if maxLifetime == 0 {
		maxLifetime = DefaultPoolMaxConnLifetime
	}
	healthPeriod := c.PoolHealthCheckPeriod
	if healthPeriod == 0 {
		healthPeriod = DefaultPoolHealthCheckPeriod
	}

	poolCfg, err := pgxpool.ParseConfig(c.DSN)
	if err != nil {
		return nil, fmt.Errorf("store_pg: parse DSN: %w", err)
	}

	// Pool bounds. Precedence: an explicit Config field wins; otherwise a
	// pool_max_conns / pool_min_conns already present in the DSN (parsed by
	// pgxpool into poolCfg) is honored; otherwise the Gortex default
	// applies. Before this, the DSN values were parsed and then silently
	// overwritten, so operators had no lever short of the cpuset.
	poolCfg.MaxConns = int32(resolvePoolBound(c.PoolMaxConns, c.DSN, "pool_max_conns", int(poolCfg.MaxConns), DefaultPoolMaxConns))
	poolCfg.MinConns = int32(resolvePoolBound(c.PoolMinConns, c.DSN, "pool_min_conns", int(poolCfg.MinConns), 0))
	poolCfg.MaxConnLifetime = maxLifetime
	poolCfg.HealthCheckPeriod = healthPeriod

	// Apply statement/lock timeouts as connect-time runtime parameters.
	// Precedence: an explicit Config field wins; otherwise a value already
	// present in the DSN (parsed into RuntimeParams) is honored; otherwise
	// the default is applied. Values are sent to PostgreSQL as integer
	// milliseconds.
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	rp := poolCfg.ConnConfig.RuntimeParams
	setTimeoutParam(rp, "statement_timeout", c.StatementTimeout, DefaultStatementTimeout)
	setTimeoutParam(rp, "lock_timeout", c.LockTimeout, DefaultLockTimeout)

	// Tag connections so pg_stat_activity can attribute them per process
	// (and, with one schema per tenant, per tenant when the operator puts
	// application_name in the DSN). An explicit DSN value is preserved.
	if _, ok := rp["application_name"]; !ok {
		rp["application_name"] = defaultApplicationName(c.ReadOnly)
	}

	schemaName := strings.TrimSpace(c.Schema)
	if schemaName != "" {
		setCmd := "SET search_path TO " + searchPathList(schemaName)
		origAfterConnect := poolCfg.AfterConnect
		poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			if origAfterConnect != nil {
				if err := origAfterConnect(ctx, conn); err != nil {
					return err
				}
			}
			if _, err := conn.Exec(ctx, setCmd); err != nil {
				return err
			}
			return nil
		}
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("store_pg: create pool: %w", err)
	}

	// Verify the connection works
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store_pg: ping: %w", err)
	}

	return pool, nil
}

// setTimeoutParam sets a millisecond timeout runtime parameter. An
// explicit configured value (cfgVal > 0) always wins; otherwise a value
// already present (from the DSN) is preserved; otherwise the default is
// applied.
func setTimeoutParam(rp map[string]string, key string, cfgVal, def time.Duration) {
	if cfgVal > 0 {
		rp[key] = fmt.Sprintf("%d", cfgVal.Milliseconds())
		return
	}
	if _, ok := rp[key]; ok {
		return
	}
	rp[key] = fmt.Sprintf("%d", def.Milliseconds())
}

// resolvePoolBound picks a pool bound. An explicit configured value
// (cfgVal > 0) always wins; otherwise, when the DSN itself carries the
// pgxpool parameter named key, the value pgxpool parsed from it
// (parsedVal) is honored; otherwise def applies.
func resolvePoolBound(cfgVal int, dsn, key string, parsedVal, def int) int {
	if cfgVal > 0 {
		return cfgVal
	}
	if dsnHasParam(dsn, key) && parsedVal > 0 {
		return parsedVal
	}
	return def
}

// dsnHasParam reports whether the DSN explicitly sets the connection
// parameter key, in either URL form (postgres://…?key=v) or keyword/value
// form (host=… key=v). pgxpool applies its own defaults for pool
// parameters that are absent, so presence — not value — is what decides
// whether the operator asked for something.
func dsnHasParam(dsn, key string) bool {
	dsn = strings.TrimSpace(dsn)
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return false
		}
		_, ok := u.Query()[key]
		return ok
	}
	for _, field := range strings.Fields(dsn) {
		k, _, found := strings.Cut(field, "=")
		if found && k == key {
			return true
		}
	}
	return false
}

// defaultApplicationName is the application_name reported to PostgreSQL
// when the DSN does not set one: "gortex" for writers, "gortex-follower"
// for read-only followers.
func defaultApplicationName(readOnly bool) string {
	if readOnly {
		return "gortex-follower"
	}
	return "gortex"
}

// searchPathList renders a Config.Schema value ("a" or "a, b") as the
// argument of SET search_path: entries are trimmed and joined, otherwise
// passed through verbatim so PostgreSQL applies its usual identifier
// rules (unquoted names fold to lowercase; callers who need a mixed-case
// schema quote it themselves, e.g. `"Tenant_A",ext`).
func searchPathList(schema string) string {
	parts := strings.Split(schema, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ", ")
}
