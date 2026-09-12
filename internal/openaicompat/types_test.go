package openaicompat

import "testing"

func TestPublicErrorClassifications(t *testing.T) {
	cases := []struct {
		err  *Error
		code string
		want int
	}{
		{InvalidRequest(), "invalid_request", 400},
		{UnsupportedParameter(), "unsupported_parameter", 400},
		{RequestTooLarge(), "request_too_large", 413},
	}
	for _, tc := range cases {
		if tc.err.Code != tc.code || tc.err.Status != tc.want {
			t.Fatalf("error=%#v", tc.err)
		}
	}
}
