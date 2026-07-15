## ADDED Requirements

### Requirement: Extract block_sequence items
The YAML extractor SHALL traverse `block_sequence` nodes and SHALL emit a `KindVariable` or `KindDoc` node for each `block_sequence_item`.

#### Scenario: Simple scalar sequence items extracted
- **WHEN** a YAML file contains:
  ```yaml
  branches:
    - main
    - development
  ```
- **THEN** the extractor SHALL emit nodes `file.yaml::branches::main` and `file.yaml::branches::development` as KindVariable, with an EdgeDefines from the parent key

#### Scenario: Sequence of mappings extracted
- **WHEN** a YAML file contains:
  ```yaml
  services:
    - name: ave-ai-vault
      repo: https://github.com/example/repo.git
  ```
- **THEN** the extractor SHALL emit a node `file.yaml::services::ave-ai-vault` with Meta containing `{"name": "ave-ai-vault", "repo_url": "https://github.com/example/repo.git"}`

### Requirement: Extract nested pairs inside sequence items
When a `block_sequence_item` contains a `block_mapping`, the YAML extractor SHALL extract each `block_mapping_pair` within it as metadata on the item node.

#### Scenario: Multi-key mapping item
- **WHEN** a sequence item has keys `name`, `repo`, `port`
- **THEN** each key-value pair SHALL be recorded in the item node's Meta map, keyed by the pair's key name

### Requirement: Preserve existing top-level extraction
The improved YAML extractor SHALL continue to extract all top-level `block_mapping_pair` keys as `KindVariable`, exactly as before.

#### Scenario: Existing extraction unchanged
- **WHEN** a YAML file `docker-compose.yml` with `name`, `version`, `services` keys is extracted
- **THEN** the result SHALL contain at least the same KindVariable nodes as before the improvement (verified by existing TestYAMLExtractor_TopLevelKeys test passing)

### Requirement: Stable node IDs
Node IDs for sequence items SHALL be deterministic and derived from the item's name key (for mappings) or its value (for scalars).

#### Scenario: Name-based ID for mapping items
- **WHEN** a sequence item mapping has a key `name: ave-ai-vault`
- **THEN** the node ID SHALL be `file.yaml::services::ave-ai-vault`

#### Scenario: Value-based ID for scalar items
- **WHEN** a sequence item is a scalar `main`
- **THEN** the node ID SHALL be `file.yaml::branches::main`
