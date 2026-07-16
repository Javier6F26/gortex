# pg-read-resilience Specification

## Purpose
PostgreSQL read-path behavior under transient failures: bounded retry, no process panics, uniform degraded-result semantics, health surfacing, and connection timeout defaults. Established by `harden-pg-store`.

## Requirements
### Requirement: Read-path queries never panic on transient PostgreSQL errors

No `graph.Store` read method on the PostgreSQL backend SHALL panic because of a PostgreSQL error. Transient errors (connection failure class 08, `57P01` admin_shutdown, `40001` serialization_failure, `40P01` deadlock_detected, `57014` query_canceled, and standby recovery-conflict cancellations) SHALL be retried with bounded exponential backoff. On retry exhaustion the method SHALL log at WARN, record the failure in the store health state, and return its zero value (empty slice / nil node / zero count).

#### Scenario: Recovery-conflict cancellation on a read replica
- **WHEN** a follower daemon runs `GetRepoNodes` against a hot standby and the query is cancelled with a recovery-conflict error
- **THEN** the store SHALL retry the query up to the attempt limit
- **AND** if a retry succeeds the caller SHALL receive the full result with no observable difference
- **AND** the daemon process SHALL NOT panic

#### Scenario: Primary failover during query iteration
- **WHEN** the connection drops mid-iteration (`rows.Err()` non-nil) during `AllEdges`
- **THEN** the store SHALL treat it identically to a query-start failure: retry, then degrade
- **AND** the process SHALL NOT panic

### Requirement: Degraded reads are uniform and observable

A read that fails after retries SHALL behave identically regardless of where the error occurred (query start vs row iteration): logged at WARN with a query tag, counted in store health state, and returned as the method's zero value. The store SHALL expose a health accessor reporting the count and last error of degraded reads, suitable for surfacing via daemon health endpoints.

#### Scenario: Same fault, same behavior
- **WHEN** the same transient fault is injected at query start in one call and at row iteration in another
- **THEN** both calls SHALL return the zero value after retry exhaustion
- **AND** both SHALL increment the degraded-read counter

#### Scenario: Health accessor reflects degradation
- **WHEN** at least one read has degraded since the store was opened
- **THEN** the health accessor SHALL report a non-zero degraded count and the most recent error

### Requirement: Connection pool enforces statement and lock timeouts

The PostgreSQL pool SHALL set `statement_timeout` and `lock_timeout` runtime parameters by default (overridable via configuration), so a reader blocked behind an exclusive lock or a runaway query fails within a bounded time instead of stalling indefinitely.

#### Scenario: Reader blocked behind an exclusive table lock
- **WHEN** a reader query waits on a table held under ACCESS EXCLUSIVE longer than the lock timeout
- **THEN** PostgreSQL SHALL cancel the wait with a lock-timeout error
- **AND** the store SHALL handle it via the transient-error retry/degrade path

#### Scenario: Operator overrides timeouts
- **WHEN** the configuration specifies custom statement/lock timeout values
- **THEN** the pool SHALL apply the configured values instead of the defaults
