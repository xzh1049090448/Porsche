package dto

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPublicContentPublishAndRestoreStrictBodies(t *testing.T) {
	pub := `{"expected_revision":1,"price_release_guid":"9"}`
	if _, err := DecodeContentPublicationRequest(strings.NewReader(pub)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"expected_revision":1,"price_release_guid":null}`, `{"expected_revision":1}`, `{"expected_revision":0,"price_release_guid":"9"}`, `{"expected_revision":1,"expected_revision":1,"price_release_guid":"9"}`, pub + ` {}`, `{"expected_revision":1,"price_release_guid":"9","unknown":1}`} {
		if _, err := DecodeContentPublicationRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("publish accepted %s", raw)
		}
	}
	restore := `{"expected_revision":1}`
	if _, err := DecodeContentRestoreRequest(strings.NewReader(restore)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"expected_revision":null}`, `{}`, `{"expected_revision":0}`, `{"expected_revision":1,"expected_revision":1}`, restore + ` {}`, `{"expected_revision":1,"unknown":1}`} {
		if _, err := DecodeContentRestoreRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("restore accepted %s", raw)
		}
	}
	for _, tc := range []struct {
		raw    string
		decode func(io.Reader) error
	}{{pub, func(r io.Reader) error { _, e := DecodeContentPublicationRequest(r); return e }}, {restore, func(r io.Reader) error { _, e := DecodeContentRestoreRequest(r); return e }}} {
		exact := tc.raw + strings.Repeat(" ", PublicContentRequestBodyLimit-len(tc.raw))
		if err := tc.decode(strings.NewReader(exact)); err != nil {
			t.Fatalf("exact limit err=%v", err)
		}
		if err := tc.decode(strings.NewReader(exact + " ")); !errors.Is(err, ErrPublicContentRequestTooLarge) {
			t.Fatalf("max+1 err=%v", err)
		}
	}
}

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
