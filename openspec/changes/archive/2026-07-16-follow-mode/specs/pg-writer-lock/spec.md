# pg-writer-lock

## ADDED Requirements

### Requirement: Indexing daemons hold an exclusive schema writer lock

A non-follow daemon using the PostgreSQL backend SHALL acquire a session-scoped PostgreSQL advisory lock — keyed on a fixed constant combined with the schema identity — after opening the pool and before any store write. If the lock cannot be acquired within a bounded timeout, startup SHALL fail with an error stating that another writer holds the schema. The lock SHALL be held for the daemon's lifetime and releases automatically on process exit or crash (session scope). Follow-mode daemons SHALL NOT acquire the writer lock.

#### Scenario: Second writer refused
- **WHEN** a writer daemon holds the lock and a second non-follow daemon starts against the same DSN and schema
- **THEN** the second daemon SHALL fail startup with the writer-conflict error
- **AND** the shared schema SHALL receive writes from only the first daemon

#### Scenario: Crash releases the lock
- **WHEN** the writer process crashes
- **THEN** the advisory lock SHALL be released by PostgreSQL when the session drops
- **AND** a subsequently started writer SHALL acquire it and boot normally

#### Scenario: Followers are unaffected by the lock
- **WHEN** a writer holds the lock and N follow-mode daemons are running
- **THEN** the followers SHALL serve reads normally without attempting to acquire the lock

#### Scenario: Distinct schemas do not contend
- **WHEN** two writers index two different schemas on the same PostgreSQL server
- **THEN** both SHALL acquire their locks and run concurrently
