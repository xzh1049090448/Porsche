package publiccontent

import "testing"

func TestParseDecimalCanonicalizesExactValues(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "0", want: "0.00000000"},
		{raw: "12.34567890", want: "12.34567890"},
		{raw: "3.21", want: "3.21000000"},
		{raw: "999999999999.99999999", want: "999999999999.99999999"},
	}

	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			got, err := ParseDecimal(test.raw)
			if err != nil {
				t.Fatalf("ParseDecimal(%q): %v", test.raw, err)
			}
			if got.String() != test.want {
				t.Fatalf("ParseDecimal(%q) = %q, want %q", test.raw, got.String(), test.want)
			}
		})
	}
}

func TestParseDecimalRejectsNonCanonicalOrOutOfRangeValues(t *testing.T) {
	for _, raw := range []string{
		"", "-0.1", "+1", "01", "1.", ".1", "1.123456789",
		"1000000000000", "999999999999.999999999", "1e3", " 1", "1 ",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseDecimal(raw); err == nil {
				t.Fatalf("ParseDecimal(%q) succeeded", raw)
			}
		})
	}
}
