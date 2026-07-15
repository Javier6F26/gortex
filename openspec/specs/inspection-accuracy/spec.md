# inspection-accuracy Specification

## Purpose
TBD - created by archiving change fix-follower-correctness-and-docs-search. Update Purpose after archive.
## Requirements
### Requirement: dead_code ignores dunder methods
The `dead_code` inspection SHALL NOT report methods whose names match the Python dunder pattern (`__<name>__`, e.g. `__call__`, `__aenter__`, `__aexit__`, `__post_init__`) as dead on the basis of zero incoming graph references, because such methods are invoked implicitly by the runtime or by protocols the graph does not model.

#### Scenario: ASGI and context-manager dunders are not flagged
- **WHEN** the `dead_code` inspection runs over a codebase containing `__call__`, `__aenter__`, `__aexit__`, and `__post_init__` methods with zero incoming references
- **THEN** none of those methods appear among the violations

#### Scenario: Ordinary unreferenced methods still flagged
- **WHEN** the same inspection encounters a non-dunder method with zero incoming references
- **THEN** it is still reported as dead code

