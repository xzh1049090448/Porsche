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

func TestExactPublicAdministrationIntentsBindEveryMutationField(t *testing.T) {
	tests := []struct {
		action Action
		intent any
		fields int
	}{
		{ActionPublicModelDelete, PublicModelDeleteIntent{ModelGUID: 11, ExpectedRevision: 3, Reason: "retired"}, 3},
		{ActionPublicPricingPublish, PublicPricingPublishIntent{ExpectedRevision: 4}, 1},
		{ActionPublicPricingRestore, PublicPricingRestoreIntent{ReleaseGUID: 12, ExpectedRevision: 5}, 2},
		{ActionPublicContentPublish, PublicContentPublishIntent{PriceReleaseGUID: 13, ExpectedRevision: 6}, 2},
		{ActionPublicContentRestore, PublicContentRestoreIntent{ReleaseGUID: 14, ExpectedRevision: 7}, 2},
	}
	for _, tc := range tests {
		encoded, err := descriptorFor(t, tc.action).Encode(tc.intent)
		if err != nil {
			t.Fatalf("action %d: %v", tc.action, err)
		}
		if got := len(decodeFields(t, encoded)); got != tc.fields {
			t.Fatalf("action %d encoded %d fields, want %d", tc.action, got, tc.fields)
		}
	}
}

func TestExactPublicAdministrationIntentsRejectMissingBindings(t *testing.T) {
	tests := []struct {
		action Action
		intent any
	}{
		{ActionPublicModelDelete, PublicModelDeleteIntent{ModelGUID: 1, ExpectedRevision: 1}},
		{ActionPublicPricingPublish, PublicPricingPublishIntent{}},
		{ActionPublicPricingRestore, PublicPricingRestoreIntent{ReleaseGUID: 1}},
		{ActionPublicContentPublish, PublicContentPublishIntent{ExpectedRevision: 1}},
		{ActionPublicContentRestore, PublicContentRestoreIntent{ReleaseGUID: 1}},
	}
	for _, tc := range tests {
		if _, err := descriptorFor(t, tc.action).Encode(tc.intent); err == nil {
			t.Fatalf("action %d accepted incomplete binding", tc.action)
		}
	}
}
