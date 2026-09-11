package actionsecurity

import (
	"strings"
	"testing"
)

func TestPublicModelDeleteReasonCanonicalBoundary(t *testing.T) {
	if !ValidPublicModelDeleteReason(strings.Repeat("界", 128)) {
		t.Fatal("rejected 128-rune reason")
	}
	for name, value := range map[string]string{
		"129 runes": strings.Repeat("界", 129),
		"newline":   "line\nbreak",
		"control":   "bad\x7fvalue",
		"format":    "bad\u200bvalue",
		"padded":    " reason ",
		"invalid":   string([]byte{'x', 0xff}),
		"empty":     "",
	} {
		if ValidPublicModelDeleteReason(value) {
			t.Fatalf("accepted %s", name)
		}
	}
}

func TestPublicModelDeleteIntentUsesSharedReasonValidator(t *testing.T) {
	descriptor := descriptorFor(t, ActionPublicModelDelete)
	for _, reason := range []string{strings.Repeat("x", 129), " padded ", "bad\nreason", "bad\u200bvalue"} {
		if _, err := descriptor.Encode(PublicModelDeleteIntent{ModelGUID: 1, ExpectedRevision: 1, Reason: reason}); err == nil {
			t.Fatalf("encoded invalid reason %q", reason)
		}
	}
}
