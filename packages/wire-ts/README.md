# @gortex/wire

TypeScript decoder for the GCX1 compact wire format — the opt-in
compact response encoding for Gortex MCP tools. See
[`docs/wire-format.md`][spec] in the Gortex repo for the full
specification.

[spec]: https://github.com/zzet/gortex/blob/main/docs/wire-format.md

## Install

```sh
npm install @gortex/wire
```

## Usage

```ts
import { decode, parseHeader, decodeAll } from "@gortex/wire";

// Raw GCX payload produced by a Gortex MCP tool with format:"gcx".
const payload = await mcpCallAsText("search_symbols", { query: "foo", format: "gcx" });

// Single-section:
const { header, rows } = decodeAll(payload);
console.log(header.tool, header.fields, rows.length);
rows.forEach((r) => console.log(r.id, r.kind, r.name));

// Multi-section (e.g. get_callers emits .nodes + .edges):
for (const section of decode(payload)) {
  console.log(section.header.tool);
  for (const row of section.rows) {
    // row is a plain object keyed by the declared field names.
  }
}
```

## API

- `decode(payload: string): Iterable<Section>` — lazy iterator over
  every section in the payload. Throws on malformed headers or
  overlong rows.
- `decodeAll(payload: string): Section` — convenience wrapper that
  returns the first section fully. Throws if the payload contains
  more than one section.
- `parseHeader(line: string): Header` — parses a single header line
  in isolation.
- Types: `Header`, `Section`, `Row`.

## Relation to the Go encoder

This package is decoder-only. Gortex servers emit GCX via the
MIT-licensed [`github.com/zzet/gortex/pkg/wire`](https://github.com/zzet/gortex/tree/main/pkg/wire)
Go module and the per-tool hand-tuned encoders in
`internal/mcp/gcx.go`; this package decodes what they produce.
Per-tool field layouts are documented in the spec linked above.

## Version

Implements `GCX1` (wire-format.md §Versioning). Unknown version
tags throw `UnsupportedVersionError` so callers can fall back to
JSON transparently.
