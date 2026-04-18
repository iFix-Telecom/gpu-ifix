package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// HashBody returns the hex SHA-256 of the canonicalized JSON representation
// of body. Canonicalization:
//   - Object keys sorted lexicographically (recursively)
//   - Arrays left in original order (order is semantic)
//   - Whitespace stripped (json.Marshal default)
//   - Numbers re-encoded via Go default (int/float ambiguity limited by
//     json decoder promoting all numbers to float64; good enough for
//     matching two copies of the SAME payload bit-for-bit after
//     canonicalization)
//
// The canonicalization makes the hash robust to clients re-serializing
// the same JSON with different key orderings — standard semantic, per
// CONTEXT.md D-C2.
func HashBody(body []byte) (string, error) {
	if len(body) == 0 {
		sum := sha256.Sum256(nil)
		return hex.EncodeToString(sum[:]), nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("idempotency: body not valid JSON: %w", err)
	}
	canonical, err := canonicalMarshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalMarshal writes a JSON value with sorted object keys. Arrays
// retain their order. Primitives delegate to json.Marshal.
func canonicalMarshal(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := []byte{'{'}
		for i, k := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			out = append(out, kb...)
			out = append(out, ':')
			vb, err := canonicalMarshal(t[k])
			if err != nil {
				return nil, err
			}
			out = append(out, vb...)
		}
		out = append(out, '}')
		return out, nil
	case []any:
		out := []byte{'['}
		for i, e := range t {
			if i > 0 {
				out = append(out, ',')
			}
			vb, err := canonicalMarshal(e)
			if err != nil {
				return nil, err
			}
			out = append(out, vb...)
		}
		out = append(out, ']')
		return out, nil
	default:
		return json.Marshal(v)
	}
}
