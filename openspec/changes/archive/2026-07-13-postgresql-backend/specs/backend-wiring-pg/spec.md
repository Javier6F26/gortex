## ADDED Requirements

### Requirement: CLI flag for PostgreSQL backend selection
The `gortex daemon start` command SHALL accept `--backend postgres` to select the PostgreSQL store, and `--pg-dsn <dsn>` (or reusing `--backend-path`) to specify the connection string.

#### Scenario: Daemon starts with PostgreSQL backend
- **WHEN** a user runs `gortex daemon start --backend postgres --pg-dsn postgres://user:pass@host:5432/gortex`
- **THEN** the daemon opens a store_pg connection to that PostgreSQL database and serves graph queries from it

#### Scenario: Missing DSN returns an error
- **WHEN** a user runs `gortex daemon start --backend postgres` without providing a DSN
- **THEN** the daemon returns an error message indicating the DSN is required

#### Scenario: Connection failure is surfaced at startup
- **WHEN** the PostgreSQL server is unreachable or credentials are invalid
- **THEN** the daemon fails fast with the connection error, not during warmup

### Requirement: Connection pooling
The PostgreSQL backend SHALL use `pgxpool` for connection management with configurable pool size, connection lifetime, and health checks.

#### Scenario: Pool is configured at Open
- **WHEN** the store is opened with a DSN
- **THEN** a pgxpool is created with MaxConns = NumCPU * 2 (or overridden by flag)

#### Scenario: Idle connections are recycled
- **WHEN** connections remain idle past MaxConnLifetime
- **THEN** they are closed and replaced by the pool health checker

### Requirement: Schema migration on Open
The PostgreSQL backend SHALL apply schema migrations automatically when the store is opened, using an inline versioning table.

#### Scenario: Fresh database gets full schema
- **WHEN** the store is opened against an empty database
- **THEN** all tables, indexes, and extensions are created

#### Scenario: Existing database with older schema is migrated
- **WHEN** the store is opened against a database with an older schema version
- **THEN** only the missing migrations are applied, in order

#### Scenario: Incompatible schema is detected but not auto-wiped
- **WHEN** the store is opened against a newer schema version
- **THEN** the store returns an error (the daemon must reindex or the database must be rebuilt)

### Requirement: Cross-process store lock is skipped for PostgreSQL
The PostgreSQL backend SHALL NOT attempt a file-based `flock` for store locking.

#### Scenario: No flock is acquired for PostgreSQL
- **WHEN** the daemon starts with `--backend postgres`
- **THEN** no cross-process file lock is acquired; PostgreSQL handles concurrent access internally

### Requirement: Backend snapshot path for PostgreSQL
The `normalizeBackendTag` function SHALL return `"postgres"` when given the postgres backend name.

#### Scenario: Snapshot path uses postgres tag
- **WHEN** generating paths for the postgres backend
- **THEN** the snapshot path includes the "postgres" tag, keeping it separate from sqlite and memory snapshot files

### Requirement: `gortex repos` command supports PostgreSQL
The `gortex repos` command SHALL be able to read repo index state from a PostgreSQL database when the daemon uses a PostgreSQL backend.

#### Scenario: Repos command connects via DSN
- **WHEN** a user runs `gortex repos` against a daemon using PostgreSQL
- **THEN** the command reads index state from the database (via the daemon's IPC or by connecting directly to the DSN)
