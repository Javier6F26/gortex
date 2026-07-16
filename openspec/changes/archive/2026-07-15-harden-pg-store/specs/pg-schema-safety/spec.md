# pg-schema-safety

## ADDED Requirements

### Requirement: Schema version read errors propagate instead of triggering migration

`readSchemaVersion` SHALL return the underlying error when the `schema_version` query fails. `ensureSchema` SHALL fail `Open` on such an error rather than interpreting it as version 0 and entering the migration loop.

#### Scenario: Transient error reading schema_version at boot
- **WHEN** the `SELECT ... FROM schema_version` query fails during `Open` (connection blip, permission error)
- **THEN** `Open` SHALL return an error naming the schema-version read as the cause
- **AND** no DDL SHALL be executed

### Requirement: Migrations run under an advisory lock and idempotent DDL

Schema migrations SHALL execute inside a PostgreSQL advisory lock with a fixed, documented key, so concurrent `Open` calls serialize: the winner applies pending migrations, losers re-read the version after acquiring the lock and no-op when up to date. All `CREATE TABLE` / `CREATE INDEX` statements in the base schema DDL MUST use `IF NOT EXISTS`.

#### Scenario: N processes open an empty schema concurrently
- **WHEN** multiple processes call `Open` against a schema with no `schema_version` rows at the same time
- **THEN** exactly one SHALL apply the migrations
- **AND** every other process SHALL block on the advisory lock, re-check the version, apply nothing, and open successfully
- **AND** no process SHALL fail with `42P07` (relation already exists)

#### Scenario: Re-running the base DDL is harmless
- **WHEN** the base schema DDL is executed against a schema where the objects already exist
- **THEN** it SHALL complete without error

### Requirement: Read-only open mode never executes DDL and fails fast on version mismatch

`Config.ReadOnly` SHALL open the store without applying any migration. When the stored schema version differs from the binary's expected version (older or newer), `Open` SHALL fail with a typed error instructing the operator to run a writer-mode process first. In read-only mode every mutating store method SHALL refuse: methods with an error return SHALL return `ErrReadOnlyStore`; methods without one SHALL drop the write, log at WARN, and record it in store health state.

#### Scenario: Read-only open against an up-to-date schema
- **WHEN** a process opens the store with `ReadOnly: true` and the stored version equals the expected version
- **THEN** `Open` SHALL succeed without executing any DDL or write

#### Scenario: Read-only open against an outdated schema
- **WHEN** a process opens the store with `ReadOnly: true` and the stored version is behind the binary's expected version
- **THEN** `Open` SHALL fail with a typed version-mismatch error
- **AND** no migration SHALL be attempted

#### Scenario: Write attempted through a read-only store
- **WHEN** any mutating store method (e.g. `AddBatch`, `EvictFile`, `SetRepoIndexState`, `AppendContent`, `BulkUpsertEmbeddings`) is invoked on a read-only store
- **THEN** the write SHALL NOT reach PostgreSQL
- **AND** the method SHALL return `ErrReadOnlyStore` (or log-and-drop when its signature has no error return)
- **AND** the refusal SHALL be observable in store health state

#### Scenario: Optional capabilities remain intact in read-only mode
- **WHEN** the store is opened read-only
- **THEN** type assertions for read capabilities (`ContentSearcher` search methods, `VectorSearcher` search methods, `BFSCapable`, aggregators) SHALL continue to succeed and serve queries
