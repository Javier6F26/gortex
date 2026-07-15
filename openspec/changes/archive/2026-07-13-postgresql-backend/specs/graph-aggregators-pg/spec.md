## ADDED Requirements

### Requirement: Server-side edge kind counts
The PostgreSQL backend SHALL implement `graph.InEdgeCounter` and `graph.EdgeKindCounter` as server-side GROUP BY queries instead of materializing the full edge table.

#### Scenario: In-edge counts by kind are returned as a map
- **WHEN** a handler requests in-edge counts filtered by specific edge kinds
- **THEN** the backend returns a map of node ID to incoming-edge count, computed entirely server-side

#### Scenario: Edge kind counts are returned as a kind→count map
- **WHEN** handleGetSurprisingConnections requests per-kind edge counts
- **THEN** the backend returns one row per distinct edge kind with its count, without shipping every edge row to Go

### Requirement: Server-side node kind and degree aggregations
The PostgreSQL backend SHALL implement `graph.NodeIDsByKinds`, `graph.NodeDegreeByKinds`, `graph.NodesInFilesByKindFinder`, and `graph.NodesByKindsScanner` as server-side queries.

#### Scenario: Node IDs by kind returns only IDs
- **WHEN** a ranking path requests node IDs for function/method kinds
- **THEN** the backend returns only the ID column — not full Node structs — to minimize cgo allocation overhead

#### Scenario: Node degree by kind returns per-node degree counts
- **WHEN** handleGetKnowledgeGaps requests degree counts for function nodes
- **THEN** the backend returns in/out edge counts grouped by node, filtered by kind server-side

#### Scenario: Nodes in files by kind returns matching nodes
- **WHEN** handleFindDeclaration requests nodes of specific kinds in specific files
- **THEN** the backend returns matching rows without scanning the full node table

### Requirement: Server-side import and file aggregations
The PostgreSQL backend SHALL implement `graph.FileImportAggregator`, `graph.FileImporters`, `graph.FileSymbolNamesByPaths`, `graph.CrossRepoEdgeAggregator`, and `graph.InDegreeForNodes` as server-side queries.

#### Scenario: Import counts by file are returned as a map
- **WHEN** mostImportedFiles requests the top imported files
- **THEN** the backend returns per-target-file import counts from a single GROUP BY query

#### Scenario: Importers of a file return matching rows
- **WHEN** check_references requests files that import a given file
- **THEN** the backend returns all EdgeImports rows whose target matches, via a server-side join

#### Scenario: Cross-repo edge counts return per-repo-pair aggregations
- **WHEN** get_architecture requests cross-repo edge counts
- **THEN** the backend returns aggregated (kind, from_repo, to_repo) counts from a single GROUP BY query

### Requirement: Server-side node fan counts
The PostgreSQL backend SHALL implement `graph.NodeFanAggregator` and `graph.NodeDegreeAggregator` for computing per-node fan-in/fan-out and degree counts server-side.

#### Scenario: Fan counts return pre-filtered results
- **WHEN** FindHotspots requests fan-in/fan-out counts for specific edge kinds
- **THEN** the backend returns per-node fan counts without materializing the full edge set
