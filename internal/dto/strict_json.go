package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ValidateNoDuplicateJSON rejects duplicate decoded object keys at every
// nesting level. Depth counts open object/array containers: a scalar has depth
// zero, so maxDepth containers around one scalar are accepted.
func ValidateNoDuplicateJSON(raw []byte, maxDepth int) error {
	if maxDepth < 1 {
		return fmt.Errorf("invalid depth")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) error
	value = func(depth int) error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if depth >= maxDepth {
			return fmt.Errorf("json nesting too deep")
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("invalid object key")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate field")
				}
				seen[key] = struct{}{}
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return fmt.Errorf("invalid object")
			}
		case '[':
			for dec.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim(']') {
				return fmt.Errorf("invalid array")
			}
		default:
			return fmt.Errorf("unexpected delimiter")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
