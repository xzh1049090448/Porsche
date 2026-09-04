package actionsecurity

import "testing"

func TestPublicIntentCanonicalFieldsAndNoNormalization(t *testing.T) {
	reason := "Re\u0301ason"
	encoded, err := descriptorFor(t, ActionPublicContentPublish).Encode(PublishIntent{ContentType: " Page ", VersionGUID: 10, ExpectedBaseVersion: 9, Reason: reason})
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeFields(t, encoded)
	if len(fields) != 4 || string(fields[0].value) != " Page " || string(fields[3].value) != reason {
		t.Fatalf("public fields were normalized: %#v", fields)
	}

	rollback, err := descriptorFor(t, ActionPublicContentRollback).Encode(RollbackIntent{ContentType: "page", VersionGUID: 8, ExpectedCurrentVersion: 10, Reason: "rollback"})
	if err != nil || len(decodeFields(t, rollback)) != 4 {
		t.Fatalf("rollback encoding failed: %v", err)
	}
}

func TestPublicIntentRejectsNonCanonicalOrEmptyFields(t *testing.T) {
	invalid := []struct {
		action Action
		intent any
	}{
		{ActionPublicContentPublish, PublishIntent{ContentType: "", VersionGUID: 1, ExpectedBaseVersion: 1, Reason: "case"}},
		{ActionPublicContentPublish, PublishIntent{ContentType: "page", VersionGUID: 0, ExpectedBaseVersion: 1, Reason: "case"}},
		{ActionPublicContentPublish, PublishIntent{ContentType: "page", VersionGUID: 1, ExpectedBaseVersion: 0, Reason: "case"}},
		{ActionPublicContentRollback, RollbackIntent{ContentType: "page", VersionGUID: 1, ExpectedCurrentVersion: 0, Reason: "case"}},
		{ActionPublicContentRollback, RollbackIntent{ContentType: "page", VersionGUID: 1, ExpectedCurrentVersion: 2, Reason: ""}},
	}
	for i, tc := range invalid {
		if _, err := descriptorFor(t, tc.action).Encode(tc.intent); err == nil {
			t.Errorf("invalid public case %d accepted", i)
		}
	}
}
