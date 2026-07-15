## ADDED Requirements

### Requirement: Recursive CTE BFS traversal
The PostgreSQL backend SHALL implement `graph.BFSCapable` using recursive CTEs for single-round-trip breadth-first traversal.

#### Scenario: Forward BFS returns outgoing reachable nodes
- **WHEN** a forward BFS is seeded with a set of node IDs and a set of edge kinds
- **THEN** the backend returns all reachable nodes via outgoing edges of the specified kinds, at their minimum hop depth

#### Scenario: Backward BFS returns incoming callers
- **WHEN** a backward BFS is seeded with a set of node IDs
- **THEN** the backend returns all nodes that can reach the seeds via incoming edges

#### Scenario: Depth-limited BFS stops at max depth
- **WHEN** a BFS is called with maxDepth = 3
- **THEN** no node beyond 3 hops from any seed appears in the result

#### Scenario: Kind-filtered BFS only follows matching edges
- **WHEN** a BFS specifies edge kinds [EdgeCalls]
- **THEN** only edges with kind = EdgeCalls are followed; edges with other kinds are ignored

#### Scenario: Duplicate seeds and cycles do not produce duplicate hops
- **WHEN** duplicate seeds are provided or the graph contains cycles
- **THEN** each node appears at most once, at its minimum depth

### Requirement: Reachable forward by kinds
The PostgreSQL backend SHALL implement `graph.ReachableForwardByKinds` for computing the closure of nodes reachable from a seed set via outgoing edges of specified kinds.

#### Scenario: Reachable set includes seeds and transitively-reachable nodes
- **WHEN** reachableFromTests searches for all symbols covered by a test function
- **THEN** the result includes the test function itself plus every symbol reachable via calls/references edges, transitively

### Requirement: Class hierarchy traversal
The PostgreSQL backend SHALL implement `graph.ClassHierarchyTraverser` for computing inheritance subgraphs (up and down) in single round-trips.

#### Scenario: Upward traversal finds parent types
- **WHEN** querying the class hierarchy of a struct type
- **THEN** the backend returns its parent types via extends/implements edges

#### Scenario: Downward traversal finds subtypes
- **WHEN** querying the class hierarchy of an interface
- **THEN** the backend returns all implementing types via incoming extends/implements edges

### Requirement: Frontier expansion
The PostgreSQL backend SHALL implement `graph.FrontierExpander` for batched BFS frontier expansion, returning adjacent edges and neighbor nodes in one round-trip.

#### Scenario: Frontier expansion returns edge+neighbor pairs
- **WHEN** a BFS frontier needs to expand N source nodes
- **THEN** the backend returns all matching adjacent edges plus the neighbor nodes in a single query
