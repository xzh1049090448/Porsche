package service

import "testing"

// The platform compare endpoint must reject an excessive fan-out locally so a
// malformed client request cannot create unbounded upstream work or charges.
func TestValidateCompareModelsRejectsMoreThanThree(t *testing.T) {
	if err := validateCompareModels([]string{"model-a", "model-b", "model-c", "model-d"}); err == nil {
		t.Fatal("expected four compare models to be rejected")
	}
}

func TestValidateCompareModelsAcceptsOneToThreeDistinctModels(t *testing.T) {
	for _, ids := range [][]string{{"model-a"}, {"model-a", "model-b"}, {"model-a", "model-b", "model-c"}} {
		if err := validateCompareModels(ids); err != nil {
			t.Fatalf("ids %#v rejected: %v", ids, err)
		}
	}
}
