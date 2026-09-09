package dto

import (
	"errors"
	"strings"
	"testing"
)

func TestPublicContentDecodeStrictPresenceNullBounds(t *testing.T) {
	valid := `{"expected_revision":1,"home":"h","about":"a","terms":"t","privacy":"p","legal_reviewed":false}`
	if _, err := DecodeContentDraftSaveRequest(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"expected_revision":1,"home":null,"about":"a","terms":"t","privacy":"p","legal_reviewed":false}`,
		`{"expected_revision":1,"home":"h","about":"a","terms":"t","privacy":"p"}`,
		`{"expected_revision":1,"home":"h","about":"a","terms":"t","privacy":"p","legal_reviewed":false,"secret":"x"}`,
		valid + ` {}`,
	} {
		if _, err := DecodeContentDraftSaveRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	_, err := DecodeContentDraftSaveRequest(strings.NewReader(strings.Repeat(" ", PublicContentRequestBodyLimit+1)))
	if !errors.Is(err, ErrPublicContentRequestTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

func TestPublicContentDTOJSONContainsNoInternalOrRawFields(t *testing.T) {
	if got := string(MarshalPublicContentDraft(ContentDraft{Revision: 1})); strings.Contains(got, "payload") || strings.Contains(got, "secret") || strings.Contains(got, "raw") {
		t.Fatalf("unsafe projection %s", got)
	}
}
