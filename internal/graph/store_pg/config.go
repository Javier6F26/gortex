package store_pg

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// Config holds the PostgreSQL connection configuration for the graph store.
type Config struct {
	// DSN is the PostgreSQL connection string.
	// Example: postgres://user:pass@host:5432/gortex
	DSN string

	// PoolMaxConns is the maximum number of connections in the pool.
	// 0 means use DefaultPoolMaxConns.
	PoolMaxConns int

	// PoolMinConns is the minimum number of connections in the pool.
	PoolMinConns int

	// PoolMaxConnLifetime is the maximum age of a connection.
	// 0 means use DefaultPoolMaxConnLifetime.
	PoolMaxConnLifetime time.Duration

	// PoolHealthCheckPeriod is how often the pool checks connection health.
	// 0 means use DefaultPoolHealthCheckPeriod.
	PoolHealthCheckPeriod time.Duration

	// Schema is an optional PostgreSQL schema name to set as the first
	// entry in search_path for every connection. Used by tests for
	// per-test schema isolation. Empty means use the database default.
	Schema string
}

// openPool creates a pgxpool from the configuration.
func (c *Config) openPool(ctx context.Context) (*pgxpool.Pool, error) {
	if c.DSN == "" {
		return nil, fmt.Errorf("store_pg: DSN is required")
	}

	maxConns := c.PoolMaxConns
	if maxConns == 0 {
		maxConns = DefaultPoolMaxConns
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

	poolCfg.MaxConns = int32(maxConns)
	poolCfg.MinConns = int32(c.PoolMinConns)
	poolCfg.MaxConnLifetime = maxLifetime
	poolCfg.HealthCheckPeriod = healthPeriod

	schemaName := c.Schema
	if schemaName != "" {
		setCmd := fmt.Sprintf("SET search_path TO %s", schemaName)
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
