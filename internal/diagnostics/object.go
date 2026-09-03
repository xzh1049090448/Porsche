package diagnostics

// ObjectDetail contains only closed classifications, never upstream values or keys.
type ObjectDecodedKind string

const (
	ObjectDecodedEmpty               ObjectDecodedKind = "empty"
	ObjectDecodedKnownChatCompletion ObjectDecodedKind = "known_chat_completion"
	ObjectDecodedOther               ObjectDecodedKind = "other"
	ObjectDecodedUnknown             ObjectDecodedKind = "unknown"
)

type ObjectFieldShape string

const (
	ObjectShapeMissing        ObjectFieldShape = "missing"
	ObjectShapeNull           ObjectFieldShape = "null"
	ObjectShapeStringEmpty    ObjectFieldShape = "string_empty"
	ObjectShapeStringNonempty ObjectFieldShape = "string_nonempty"
	ObjectShapeAmbiguous      ObjectFieldShape = "ambiguous"
	ObjectShapeUnknown        ObjectFieldShape = "unknown"
)

type ObjectKeyMatch string

const (
	ObjectKeyCanonical   ObjectKeyMatch = "canonical"
	ObjectKeyCaseVariant ObjectKeyMatch = "case_variant"
	ObjectKeyMultiple    ObjectKeyMatch = "multiple"
	ObjectKeyNone        ObjectKeyMatch = "none"
	ObjectKeyUnknown     ObjectKeyMatch = "unknown"
)

type ObjectDetail struct {
	DecodedKind ObjectDecodedKind `json:"decoded_kind"`
	FieldShape  ObjectFieldShape  `json:"field_shape"`
	KeyMatch    ObjectKeyMatch    `json:"key_match"`
}

func safeObjectDetail(detail ObjectDetail) ObjectDetail {
	switch detail.DecodedKind {
	case ObjectDecodedEmpty, ObjectDecodedKnownChatCompletion, ObjectDecodedOther, ObjectDecodedUnknown:
	default:
		detail.DecodedKind = ObjectDecodedUnknown
	}
	switch detail.FieldShape {
	case ObjectShapeMissing, ObjectShapeNull, ObjectShapeStringEmpty, ObjectShapeStringNonempty, ObjectShapeAmbiguous, ObjectShapeUnknown:
	default:
		detail.FieldShape = ObjectShapeUnknown
	}
	switch detail.KeyMatch {
	case ObjectKeyCanonical, ObjectKeyCaseVariant, ObjectKeyMultiple, ObjectKeyNone, ObjectKeyUnknown:
	default:
		detail.KeyMatch = ObjectKeyUnknown
	}
	return detail
}
