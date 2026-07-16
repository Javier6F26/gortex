# import-path-resolution

## ADDED Requirements

### Requirement: Ambiguous names resolve to the nearest candidate
When multiple symbols share the requested name, `find_import_path` SHALL prefer the candidate closest to the requesting file — same package, then same service/root, then same repo — before any cross-service candidate. It SHALL also report `already_imported: true` when the requesting file already imports the resolved (or a same-named nearer) symbol.

#### Scenario: Same-package candidate wins
- **WHEN** `find_import_path` is called with `name=GortexManager` and `path=<repo>/knowledge/core/kernel.py`, and both `<repo>/knowledge/core/gortex_manager.py::GortexManager` and `<repo>/workspace/core/gortex_manager.py::GortexManager` exist
- **THEN** the resolved symbol is the `knowledge/core` one, not the `workspace/core` one

#### Scenario: Existing import is detected
- **WHEN** the requesting file already imports the resolved symbol
- **THEN** the response carries `already_imported: true`
