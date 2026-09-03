package whitelabel

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/diagnostics"
)

func enrichObjectFailure(payload []byte, failure *diagnostics.ChunkFailure) {
	if failure.Reason == diagnostics.ChunkInvalidValue && failure.Field == diagnostics.ChunkObject && failure.Object != nil {
		failure.Object.FieldShape, failure.Object.KeyMatch = objectFieldDetails(payload)
	}
}

// objectFieldDetails scans only a traced object rejection. Token keys preserve
// duplicate occurrences; decoding complete values skips nested object names.
// It never reinterprets the string decoded by the original chunk validation.
func objectFieldDetails(payload []byte) (diagnostics.ObjectFieldShape, diagnostics.ObjectKeyMatch) {
	unknownShape, unknownKey := diagnostics.ObjectShapeUnknown, diagnostics.ObjectKeyUnknown
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return unknownShape, unknownKey
	}
	count := 0
	shape, keyMatch := diagnostics.ObjectShapeMissing, diagnostics.ObjectKeyNone
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return unknownShape, unknownKey
		}
		key, ok := token.(string)
		if !ok {
			return unknownShape, unknownKey
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return unknownShape, unknownKey
		}
		if !strings.EqualFold(key, "object") {
			continue
		}
		count++
		if count > 1 {
			shape, keyMatch = diagnostics.ObjectShapeAmbiguous, diagnostics.ObjectKeyMultiple
			continue
		}
		keyMatch = diagnostics.ObjectKeyCaseVariant
		if key == "object" {
			keyMatch = diagnostics.ObjectKeyCanonical
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			shape = diagnostics.ObjectShapeNull
			continue
		}
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil {
			return unknownShape, unknownKey
		}
		shape = diagnostics.ObjectShapeStringNonempty
		if decoded == "" {
			shape = diagnostics.ObjectShapeStringEmpty
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return unknownShape, unknownKey
	}
	if _, err := decoder.Token(); err != io.EOF {
		return unknownShape, unknownKey
	}
	return shape, keyMatch
}
