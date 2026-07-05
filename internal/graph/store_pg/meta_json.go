package store_pg

import (
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zzet/gortex/internal/graph"
)

// encodeMeta serialises a node/edge Meta map for storage in a JSONB column.
// pgx handles the JSONB encoding natively when the struct field implements
// json.Marshaler; we just return the raw JSON bytes and let pgx wrap them.
// An empty / nil Meta returns nil so the column stores NULL.
func encodeMeta(meta map[string]any) ([]byte, error) {
	if len(meta) == 0 {
		return nil, nil
	}
	return json.Marshal(meta)
}

// decodeMeta deserialises a Meta map from a JSONB column. pgx materialises
// JSONB values as raw JSON bytes; we unmarshal into map[string]any.
// A nil or empty blob returns nil.
func decodeMeta(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// promotedNodeMeta carries the columns that were promoted from the Meta blob
// into dedicated node columns for fast indexed access. Mirrors the SQLite
// backend's promotion scheme so Meta round-trips are self-consistent.
type promotedNodeMeta struct {
	sig        *string
	vis        *string
	doc        *string
	external   *int64
	returnType *string
	isAsync    *int64
	isStatic   *int64
	isAbstract *int64
	isExported *int64
	updatedAt  *int64
	dataClass  *string
}

// extractPromotedMeta strips well-known keys from Meta into the promoted
// struct and returns the residual Meta (what stays in the JSONB column).
// Matches the SQLite backend's extractPromotedMeta semantics.
func extractPromotedMeta(meta map[string]any) (promotedNodeMeta, map[string]any) {
	if meta == nil {
		return promotedNodeMeta{}, nil
	}
	var p promotedNodeMeta
	residual := make(map[string]any, len(meta))

	for k, v := range meta {
		switch k {
		case "signature":
			if s, ok := v.(string); ok {
				p.sig = &s
			} else {
				residual[k] = v
			}
		case "visibility":
			if s, ok := v.(string); ok {
				p.vis = &s
			} else {
				residual[k] = v
			}
		case "doc":
			if s, ok := v.(string); ok {
				p.doc = &s
			} else {
				residual[k] = v
			}
		case "external":
			if b, ok := v.(bool); ok {
				n := int64(0)
				if b {
					n = 1
				}
				p.external = &n
			} else if n, ok := v.(int64); ok {
				p.external = &n
			} else if f, ok := v.(float64); ok {
				n := int64(f)
				p.external = &n
			} else {
				residual[k] = v
			}
		case "return_type":
			if s, ok := v.(string); ok {
				p.returnType = &s
			} else {
				residual[k] = v
			}
		case "is_async":
			if b, ok := v.(bool); ok {
				n := int64(0)
				if b {
					n = 1
				}
				p.isAsync = &n
			} else if n, ok := v.(int64); ok {
				p.isAsync = &n
			} else {
				residual[k] = v
			}
		case "is_static":
			if b, ok := v.(bool); ok {
				n := int64(0)
				if b {
					n = 1
				}
				p.isStatic = &n
			} else if n, ok := v.(int64); ok {
				p.isStatic = &n
			} else {
				residual[k] = v
			}
		case "is_abstract":
			if b, ok := v.(bool); ok {
				n := int64(0)
				if b {
					n = 1
				}
				p.isAbstract = &n
			} else if n, ok := v.(int64); ok {
				p.isAbstract = &n
			} else {
				residual[k] = v
			}
		case "is_exported":
			if b, ok := v.(bool); ok {
				n := int64(0)
				if b {
					n = 1
				}
				p.isExported = &n
			} else if n, ok := v.(int64); ok {
				p.isExported = &n
			} else {
				residual[k] = v
			}
		case "updated_at":
			if f, ok := v.(float64); ok {
				n := int64(f)
				p.updatedAt = &n
			} else if n, ok := v.(int64); ok {
				p.updatedAt = &n
			} else {
				residual[k] = v
			}
		case "data_class":
			if s, ok := v.(string); ok {
				p.dataClass = &s
			} else {
				residual[k] = v
			}
		default:
			residual[k] = v
		}
	}
	return p, residual
}

// restorePromotedMeta merges the promoted column values back into the Meta
// map after reading a node row, so the in-memory graph.Node.Meta is complete.
func restorePromotedMeta(n *graph.Node, p promotedNodeMeta) {
	if n.Meta == nil {
		n.Meta = make(map[string]any)
	}
	if p.sig != nil {
		n.Meta["signature"] = *p.sig
	}
	if p.vis != nil {
		n.Meta["visibility"] = *p.vis
	}
	if p.doc != nil {
		n.Meta["doc"] = *p.doc
	}
	if p.external != nil {
		n.Meta["external"] = *p.external != 0
	}
	if p.returnType != nil {
		n.Meta["return_type"] = *p.returnType
	}
	if p.isAsync != nil {
		n.Meta["is_async"] = *p.isAsync != 0
	}
	if p.isStatic != nil {
		n.Meta["is_static"] = *p.isStatic != 0
	}
	if p.isAbstract != nil {
		n.Meta["is_abstract"] = *p.isAbstract != 0
	}
	if p.isExported != nil {
		n.Meta["is_exported"] = *p.isExported != 0
	}
	if p.updatedAt != nil {
		n.Meta["updated_at"] = *p.updatedAt
	}
	if p.dataClass != nil {
		n.Meta["data_class"] = *p.dataClass
	}
}

// pgx JSONB integration: store_pg works with raw JSON bytes and lets pgx
// handle the JSONB encoding. The pgtype.JSONB codec is registered by default
// in pgx and accepts raw JSON []byte.

// jsonbScanner implements the pgx Scanner interface for JSONB columns.
// It is used internally by scanNode / scanEdge to read meta JSONB into
// a map[string]any.
type jsonbScanner map[string]any

func (m *jsonbScanner) ScanBytes(data []byte) error {
	if data == nil || len(data) == 0 {
		*m = nil
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	*m = jsonbScanner(result)
	return nil
}

// Ensure pgtype interfaces are satisfied
var _ pgtype.BytesScanner = (*jsonbScanner)(nil)
