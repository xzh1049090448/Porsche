package service

import "testing"

func TestConversationGUIDRejectsNonDecimalOrNonPositiveValues(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "1.5", "not-a-guid"} {
		if _, err := parseConversationGUID(value); err == nil {
			t.Fatalf("parseConversationGUID(%q) accepted invalid value", value)
		}
	}
	guid, err := parseConversationGUID("9001")
	if err != nil || guid != 9001 {
		t.Fatalf("parseConversationGUID() = %d, %v", guid, err)
	}
}
